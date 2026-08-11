package web

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pi-go/agent"
	"github.com/yosukeno/pi-go/config"
	"github.com/yosukeno/pi-go/llm"
	"github.com/yosukeno/pi-go/session"
)

// Session is one conversation: an Agent, its transcript on disk, its event Hub,
// and its approval gate.
type Session struct {
	ID string

	mgr    *Manager
	hub    *Hub
	policy *Policy
	gate   *WebGate

	// mu guards everything an HTTP request and the run goroutine both touch.
	// Note what it does not guard: the Hub and the gate have their own locks and
	// never call back into Session, so a request can publish or answer an
	// approval while a run holds this.
	mu        sync.Mutex
	agent     *agent.Agent
	store     *session.Store
	cfg       config.Resolved
	persisted int
	// cwd is the session's working directory, resolved and validated at
	// creation. The file tools' path guard and the terminal's shell both root
	// here.
	cwd string
	// term is the lazily spawned shell; nil until the panel first attaches.
	term *Terminal
	// recorded is how much of the agent's running totals is already on disk, so each
	// run records only what it added. Touched only from finish(), under the same
	// conditions that make reading the agent's counters safe.
	recorded session.Recorded
	active   *activeRun
	lastUsed time.Time
}

type activeRun struct {
	id     string
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *Session) Hub() *Hub       { return s.hub }
func (s *Session) Gate() *WebGate  { return s.gate }
func (s *Session) Policy() *Policy { return s.policy }

func (s *Session) Model() config.Resolved {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

func (s *Session) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.Path()
}

// Workspace is the session's working directory relative to the server root,
// slash-separated; "" means the root itself.
func (s *Session) Workspace() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta := s.store.Meta()
	if meta == nil {
		return ""
	}
	rel, err := filepath.Rel(s.mgr.Cwd(), meta.Cwd)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}

// SetInfo appends sidebar edits (pin state, custom title) as a meta record.
// Under s.mu for the same reason finish() persists under it: the run goroutine
// may be mid-append, and store.append is not internally locked.
func (s *Session) SetInfo(title *string, pinned *bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.AppendMeta(&session.Meta{Title: title, Pinned: pinned})
}

func (s *Session) Active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active != nil
}

// Terminal returns the session's shell, spawning it on first use and
// respawning after the user exits it. The shell outlives page reloads and
// session switches — it dies only with the session itself.
func (s *Session) Terminal() (*Terminal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term != nil && s.term.Exited() {
		s.term = nil
	}
	if s.term == nil {
		t, err := startTerminal(s.cwd, 80, 24)
		if err != nil {
			return nil, err
		}
		s.term = t
	}
	return s.term, nil
}

// closeTerminal kills the shell, if one was ever spawned. Called wherever the
// session itself goes away — eviction, deletion, server shutdown — so a
// forgotten session never leaves a shell (and its children) behind.
func (s *Session) closeTerminal() {
	s.mu.Lock()
	t := s.term
	s.term = nil
	s.mu.Unlock()
	if t != nil {
		t.Kill()
	}
}

func (s *Session) touch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastUsed = time.Now()
}

func (s *Session) idleSince() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastUsed
}

// Start kicks off one turn in the background and returns immediately.
//
// The returned run outlives this request by design: POST /messages and GET
// /stream are separate endpoints precisely so that the connection which started
// the work is not the only one that can watch it. Closing the browser mid-run
// leaves the agent running, which is the behaviour a coding agent needs — a
// reload should not abandon a half-finished edit.
func (s *Session) Start(prompt string) (string, error) {
	s.mu.Lock()
	if s.active != nil {
		s.mu.Unlock()
		return "", ErrRunActive
	}
	runID := randomID(4)
	// The hard timeout is the ceiling that replaces "the client disconnected".
	// It reaches the HTTP request to the model and every bash child process
	// through the same context.
	ctx, cancel := context.WithTimeout(context.Background(), s.mgr.cfg.RunTimeout)
	run := &activeRun{id: runID, cancel: cancel, done: make(chan struct{})}
	s.active = run
	s.lastUsed = time.Now()
	a, model, provider := s.agent, s.cfg.Model, s.cfg.Provider
	head := s.store.Head()
	s.mu.Unlock()

	// The persister writes the run to disk as it happens: the prompt now (the
	// loop does not announce it), then each turn as its events pass through
	// consume. A kill mid-run loses only the in-flight turn; finish() reconciles
	// whatever the persister failed on, since persisted counts only landed
	// appends. The append runs under s.mu, the store's locking protocol — see
	// SetInfo.
	persister := agent.NewTurnPersister(func(m llm.Message) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := s.store.Append(m); err != nil {
			return err
		}
		s.persisted++
		return nil
	}, func(err error) {
		s.hub.Publish(Event{Type: EvError, Error: "incremental persist failed; the run is still saved when it ends: " + err.Error()})
	})
	persister.Add(llm.UserText(prompt))

	// The checkpoint must land before the run's first write: it is the state
	// a later rewind to this prompt restores to, and the transcript head at
	// this moment is its name. Failure degrades to conversation-only rewind
	// for this point — it never blocks the run.
	if s.mgr.shadow != nil {
		if err := s.mgr.shadow.Checkpoint(head); err != nil {
			fmt.Fprintf(os.Stderr, "session %s: checkpoint failed: %v\n", s.ID, err)
		}
	}

	s.hub.Publish(Event{Type: EvRunStart, RunID: runID, Model: model, Provider: provider})
	// Published rather than merely appended, so a second tab sees the prompt
	// appear as it happens.
	s.hub.Publish(Event{Type: EvUserMessage, Text: prompt})

	go func() {
		defer close(run.done)
		defer cancel()
		s.consume(a.Run(ctx, prompt), persister)
		s.finish()
	}()
	return runID, nil
}

// consume forwards loop events to the Hub, feeding the persister first: a
// message enters the transcript at the moment it enters the conversation, not
// at the end of the run. This is the third consumer of the same event stream,
// after the terminal renderer and the tests.
func (s *Session) consume(events <-chan agent.Event, persister *agent.TurnPersister) {
	ended := false
	for e := range events {
		persister.OnEvent(e)
		we, ok := fromAgentEvent(e)
		if !ok {
			continue
		}
		s.hub.Publish(we)
		switch we.Type {
		case EvTurnStart:
			s.spendAutoTurn()
		case EvRunEnd:
			ended = true
		}
	}
	if !ended {
		// Belt and braces. The loop always ends with one, but a client that never
		// sees run_end would show a turn in progress forever, and a stuck spinner
		// is a worse failure than a slightly redundant event.
		s.hub.Publish(Event{Type: EvRunEnd, StopReason: "aborted"})
	}
}

// spendAutoTurn counts down a turn-limited auto mode and announces the reversion.
// Letting it lapse silently is the failure this exists to prevent: the gate would
// re-arm without anything on screen changing.
func (s *Session) spendAutoTurn() {
	from, state, reverted := s.policy.TurnStarted()
	if !reverted {
		return
	}
	s.hub.Publish(Event{
		Type: EvPolicyReverted, Policy: &state,
		From: string(from), To: state.Mode, Reason: "the auto-mode turn budget ran out",
	})
}

// finish persists whatever the run added and clears the active slot.
//
// Reading the agent's messages here is safe because the event channel is closed,
// which happens after the loop's final append. Doing it mid-run would be a data
// race, which is why persistence is per-run rather than per-turn.
func (s *Session) finish() {
	s.mu.Lock()
	msgs := s.agent.Messages()
	var err error
	if len(msgs) > s.persisted {
		err = s.store.AppendAll(msgs[s.persisted:])
		s.persisted = len(msgs)
	}
	// Recorded here for the same reason the messages are: the channel is closed, so
	// reading the agent's counters is safe on this goroutine and nowhere else.
	if err == nil {
		err = s.recordCostLocked(msgs)
	}
	s.active = nil
	s.lastUsed = time.Now()
	s.mu.Unlock()

	if err != nil {
		s.hub.Publish(Event{Type: EvError, Error: "failed to persist session: " + err.Error()})
	}
}

// recordCostLocked writes the tokens spent since the last such record, and must be
// called with s.mu held and no run in flight — it reads the agent's counters, which
// the run goroutine writes without a lock.
//
// Shared by finish and Compact rather than copied into both, because they both move
// s.recorded: a second copy of this would eventually double-count a call or skip
// one, and the symptom would be a cost report that is quietly wrong rather than a
// failure anyone notices. The CLI keeps one such closure for the same reason.
func (s *Session) recordCostLocked(msgs []llm.Message) error {
	st, ok := session.UsageDelta(&s.recorded, s.agent.Usage(), s.agent.Delegated())
	if !ok {
		return nil
	}
	// A snapshot rather than a delta, so it is attached here rather than threaded
	// through UsageDelta; see the matching note in main.go.
	comp := session.Compose(msgs, s.agent.OverheadTokens())
	comp.Measured = s.agent.LastInput()
	comp.Cleared = s.agent.ClearedFromPrompt()
	st.Composition = &comp
	return s.store.AppendStats(st)
}

// errMessageUnknown is rewind's not-found: the timeline id the client sent is
// not a user message this session still has.
var errMessageUnknown = errors.New("message not found on the timeline")

// errFilesUnavailable is rewind's "you asked for file restoration but this
// point has no checkpoint": the snapshot predates the feature, or it failed
// to land when the run started. The conversation can still rewind; the files
// cannot follow.
var errFilesUnavailable = errors.New("no file checkpoint for that point")

// RewindPreview reports what restoring files for a rewind to messageID would
// do, without touching anything. available is false when the point has no
// checkpoint to restore to.
func (s *Session) RewindPreview(messageID string) (changes []FileChange, available bool, err error) {
	if s.mgr.shadow == nil {
		return nil, false, nil
	}
	k, ok := s.hub.UserMessageOrdinal(messageID)
	if !ok {
		return nil, false, errMessageUnknown
	}
	s.mu.Lock()
	point, ok := s.store.RewindPoint(k)
	s.mu.Unlock()
	if !ok {
		return nil, false, errMessageUnknown
	}
	changes, available = s.mgr.shadow.Preview(point)
	return changes, available, nil
}

// Rewind abandons the branch from a user message onwards: the message itself,
// every turn that answered it, and everything after, all become unreachable
// from the session's head. Nothing is deleted — the transcript is append-only,
// so the abandoned records stay in the file as an unreachable branch — but the
// agent, the usage counters and every client's next snapshot all continue
// from the fork. A run in flight blocks it, the same way it blocks SetModel:
// the loop would go on appending to a branch nobody can see.
//
// files asks for the workspace to be restored to the checkpoint taken when
// that message was sent — modified files go back, deleted ones return, ones
// created afterwards are removed. The restore runs before the fork and under
// the same lock, so a rewind is all-or-nothing: a failed restore leaves the
// conversation alone, and no run can slip into the gap between the two.
func (s *Session) Rewind(messageID string, files bool) error {
	k, ok := s.hub.UserMessageOrdinal(messageID)
	if !ok {
		return errMessageUnknown
	}

	s.mu.Lock()
	if s.active != nil {
		s.mu.Unlock()
		return ErrRunActive
	}
	point, ok := s.store.RewindPoint(k)
	if !ok {
		// The hub and the store count by the same rule, so this is a drift
		// bug, not user error — but a failed rewind must change nothing.
		s.mu.Unlock()
		return errMessageUnknown
	}
	if files {
		if s.mgr.shadow == nil {
			s.mu.Unlock()
			return errFilesUnavailable
		}
		if err := s.mgr.shadow.Restore(point); err != nil {
			s.mu.Unlock()
			if errors.Is(err, errNoCheckpoint) {
				return errFilesUnavailable
			}
			return err
		}
	}
	if err := s.store.Fork(point); err != nil {
		s.mu.Unlock()
		return err
	}
	history := s.store.TimedMessages("")
	usage, delegated := s.store.UsageTotals()
	msgs := make([]llm.Message, 0, len(history))
	for _, t := range history {
		msgs = append(msgs, t.Message)
	}
	s.agent.SetMessages(msgs)
	s.agent.SetUsage(usage, delegated)
	// The persistence baselines move with the agent: without that the next
	// finish() would append the whole surviving branch as if it were new, and
	// UsageDelta would re-record the surviving turns' tokens as a delta.
	s.persisted = len(msgs)
	s.recorded = session.Recorded{Usage: usage, Delegated: delegated}
	s.lastUsed = time.Now()
	s.mu.Unlock()

	// Reset before publishing, so the event only ever meets rebuilt state.
	s.hub.Reset(history, usage)
	s.hub.Publish(Event{Type: EvRewound})
	return nil
}

// Compact replaces the conversation with a summary of it, writing the result as a
// fresh branch the same way Rewind does: the original records stay in the file and
// become unreachable, so the transcript is still append-only and the full history is
// still auditable.
//
// Unlike every other operation here, this one holds a network call in the middle, so
// it cannot run under s.mu — the snapshot endpoint takes that lock and the browser
// would freeze for as long as the summary takes. The lock is therefore taken twice:
// once to establish that no run is in flight, and once to apply the result. Between
// them the agent's own Compact refuses if the conversation moved, so the worst case
// is a wasted call and an unchanged session rather than a summary describing a
// history that no longer exists.
func (s *Session) Compact(ctx context.Context) (agent.CompactResult, error) {
	s.mu.Lock()
	if s.active != nil {
		s.mu.Unlock()
		return agent.CompactResult{}, ErrRunActive
	}
	a := s.agent
	s.mu.Unlock()

	res, err := a.Compact(ctx)
	if err != nil {
		return res, err
	}

	s.mu.Lock()
	if s.active != nil {
		// A run started while the summary was being written and has already appended
		// to the replaced history. The agent's own race check cannot see this one,
		// because from its side the conversation is whatever it now holds. Refusing
		// here would be too late to undo the swap, so the honest move is to persist
		// what the agent actually has: the fork below writes the current history
		// whatever it is, which keeps the file and the agent in agreement.
		//
		// Recorded rather than prevented, because the alternative is holding the lock
		// across the call and freezing the UI for every session that never compacts.
		s.mu.Unlock()
		return res, ErrRunActive
	}
	if err := s.store.Fork(""); err != nil {
		s.mu.Unlock()
		return res, err
	}
	msgs := a.Messages()
	if err := s.store.AppendAll(msgs); err != nil {
		s.mu.Unlock()
		return res, err
	}
	// The persistence baseline moves with the agent, for the same reason it does in
	// Rewind: the next finish() slices the history at this offset, and a stale one
	// past the end of a much shorter history is a panic.
	s.persisted = len(msgs)
	// The summarising call is spend that already happened, so it is recorded now
	// rather than left for the next finish(): a session compacted and then closed or
	// evicted would otherwise lose it. The CLI's /compact does the same.
	if err := s.recordCostLocked(msgs); err != nil {
		s.mu.Unlock()
		return res, err
	}
	history := s.store.TimedMessages("")
	usage, _ := s.store.UsageTotals()
	s.lastUsed = time.Now()
	s.mu.Unlock()

	// Reset before publishing, so the event only ever meets rebuilt state.
	s.hub.Reset(history, usage)
	s.hub.Publish(Event{Type: EvCompacted})
	return res, nil
}

// Steer delivers a message into the run that is in flight. It reports whether
// there was one to deliver.
//
// The publish happens in the loop, not here: the message enters the conversation
// at a turn boundary the loop chooses, and putting it on the timeline any earlier
// would show it in a place the model never saw it.
func (s *Session) Steer(text string) bool {
	s.mu.Lock()
	a := s.agent
	s.mu.Unlock()
	// The agent's own lock decides, so there is no window between "is a run
	// active" and "accept this message".
	return a.Steer(text)
}

// Cancel stops the current run. It reports whether there was one.
//
// Cancellation goes through the run's context, so it reaches the in-flight HTTP
// request and any bash child process. That is the reason the UI cancels through
// this endpoint instead of aborting its own SSE connection: dropping a socket
// leaves `go test` running.
func (s *Session) Cancel() bool {
	s.mu.Lock()
	run := s.active
	s.mu.Unlock()
	if run == nil {
		return false
	}
	run.cancel()
	return true
}

func (s *Session) wait() {
	s.mu.Lock()
	run := s.active
	s.mu.Unlock()
	if run != nil {
		<-run.done
	}
}

// SetModel switches models between runs, keeping the transcript. Every provider
// here speaks the same wire format and thinking is not replayed, so there is
// nothing model-specific left to translate.
//
// It refuses during a run: SetClient writes an Agent field with no
// synchronisation, and the value is read by the loop on every turn.
func (s *Session) SetModel(name string) (config.Resolved, error) {
	cfg, err := config.Resolve(name)
	if err != nil {
		return config.Resolved{}, err
	}

	s.mu.Lock()
	if s.active != nil {
		s.mu.Unlock()
		return config.Resolved{}, ErrRunActive
	}
	s.agent.SetClient(s.client(cfg))
	// The clearing threshold follows the model: "auto" is half its window, and the
	// catalogue spans 200K to 1M. A bad spec was rejected at startup, so the error
	// here can only be an unknown window, which ParseContextEdit answers with "off".
	editCfg, _ := agent.ParseContextEdit(s.mgr.cfg.ContextEdit, cfg.ContextWindow)
	s.agent.SetContextEdit(editCfg)
	// The gauge measures its bands against the trigger, so it has to move with the
	// model too — otherwise switching from a 262K model to a 1M one leaves the browser
	// colouring against a threshold the session no longer uses.
	s.hub.SetClearTrigger(editCfg.Trigger)
	s.cfg = cfg
	s.lastUsed = time.Now()
	s.mu.Unlock()

	s.hub.Publish(Event{
		Type: EvModelChanged, Model: cfg.Model, Provider: cfg.Provider,
		ContextWindow: cfg.ContextWindow,
	})
	return cfg, nil
}

// SetPolicy changes the approval mode and broadcasts it, so a second tab sees the
// auto-mode banner immediately.
func (s *Session) SetPolicy(mode Mode, turns int) PolicyState {
	state := s.policy.Set(mode, turns)
	s.hub.Publish(Event{Type: EvPolicyChanged, Policy: &state, By: "user"})
	return state
}

// AllowTool and AllowCommand install session-scoped standing grants.
func (s *Session) AllowTool(name string) PolicyState {
	state := s.policy.AllowTool(name)
	s.hub.Publish(Event{Type: EvPolicyChanged, Policy: &state, By: "user"})
	return state
}

func (s *Session) AllowCommand(cmd string) PolicyState {
	state := s.policy.AllowCommand(cmd)
	s.hub.Publish(Event{Type: EvPolicyChanged, Policy: &state, By: "user"})
	return state
}

// Deliberately no Usage accessor here. One existed, took s.mu, and returned
// s.agent.Usage() — but s.mu does not guard the Agent's fields, and the loop
// writes its usage totals from the run goroutine with no lock at all. Nothing
// called it, so -race never saw the race that wiring it up would have created.
//
// The token numbers the UI needs already travel as events: per-turn usage on
// message, the accumulated total on run_end. Anyone adding an endpoint here
// should read agent.Agent.Usage's contract first.
