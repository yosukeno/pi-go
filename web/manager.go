package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/wangy/pi-go/agent"
	"github.com/wangy/pi-go/config"
	"github.com/wangy/pi-go/llm"
	"github.com/wangy/pi-go/memory"
	"github.com/wangy/pi-go/session"
	"github.com/wangy/pi-go/skills"
	"github.com/wangy/pi-go/tools"
)

// Timeouts that replace "the client hung up" as the way a run ends.
//
// The CLI can rely on a person being present; a server cannot. Since a run
// deliberately survives its originating connection, these are the only things
// standing between a stuck loop and a process that never lets go of it.
const (
	DefaultRunTimeout  = 30 * time.Minute
	DefaultIdleTimeout = 15 * time.Minute
)

var (
	// ErrRunActive is the 409 case: one run at a time per session.
	ErrRunActive = errors.New("a run is already in progress for this session")
	ErrNotFound  = errors.New("session not found")
)

type Config struct {
	// Cwd is the server root, fixed at startup. A session's own working
	// directory is the root or one of its subdirectories (see Create): the path
	// guard is per-session, but it never bites anywhere outside this tree.
	Cwd        string
	SessionDir string
	Model      string
	Retries    int
	MaxTurns   int

	// Skills are already discovered by the caller. Loading them here would mean
	// re-scanning per session for no benefit: Cwd is fixed for the whole server,
	// so the set cannot differ between sessions.
	Skills []skills.Skill
	// Memory is the agent's note store, already resolved by the caller for the same
	// reason Skills is: Cwd is fixed for the whole server, so re-resolving per session
	// could only produce a view that disagrees with the one a session already recorded.
	//
	// Nil is valid and means -no-memory: Store's methods all tolerate a nil receiver,
	// so there is no branch here.
	Memory *memory.Store

	GateTimeout time.Duration
	RunTimeout  time.Duration
	IdleTimeout time.Duration

	// ContextEdit is the raw -context-edit spec, resolved per session against that
	// session's model. Kept as the spec rather than a resolved config because "auto"
	// means a fraction of the model's window and the browser can switch models mid-session,
	// so a value resolved once at startup would be wrong after the first switch.
	// Empty means "auto".
	ContextEdit string

	// NewClient overrides how an LLM client is built. It exists so tests can
	// drive a whole run without a network, which is the only way to verify the
	// things that matter here: that a run outlives its connection and that an
	// unanswered approval turns into a tool result instead of a dead loop.
	NewClient func(config.Resolved, func(llm.RetryInfo)) llm.Client
}

// Manager is the session registry.
//
// agent.Agent is not safe for concurrent use — its message slice is appended by
// the run goroutine and SetClient writes a field unsynchronised — so each session
// owns one Agent, one tool registry (the path guard is per-registry) and at most
// one run. Concurrency across sessions is unrestricted; within a session it is
// one.
type Manager struct {
	cfg Config

	// Derived from cfg.Skills once, because every session needs the same three
	// projections of it and rebuilding them per session is pure waste.
	skillRoots   []string
	memoryRoots  []string
	skillSection string
	skillNames   []string

	// journal is the workspace-wide file pre-image store: one per server
	// because Cwd is one per server, shared by every session so the
	// workspace-changes view sees all of them.
	journal *tools.DirJournal

	// shadow is the per-workspace checkpoint store for rewinding files. Nil
	// when git (or the dir) is unavailable: rewind then degrades to
	// conversation-only, which is what it was before checkpoints existed.
	shadow *ShadowRepo

	mu       sync.Mutex
	sessions map[string]*Session
	closed   bool

	stop chan struct{}
	wg   sync.WaitGroup
}

func NewManager(cfg Config) (*Manager, error) {
	if cfg.Cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		cfg.Cwd = wd
	}
	abs, err := filepath.Abs(cfg.Cwd)
	if err != nil {
		return nil, err
	}
	cfg.Cwd = abs
	if cfg.SessionDir == "" {
		cfg.SessionDir = session.DefaultDir()
	}
	if cfg.GateTimeout <= 0 {
		cfg.GateTimeout = DefaultGateTimeout
	}
	if cfg.RunTimeout <= 0 {
		cfg.RunTimeout = DefaultRunTimeout
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	// Fail at startup rather than on the first prompt: an unusable model or a
	// missing key should not look like a broken UI. The same reasoning covers a
	// timeout configuration that cannot hold — see CheckApprovalBudget for why that
	// one is otherwise invisible.
	if err := CheckApprovalBudget(cfg.RunTimeout, cfg.GateTimeout, tools.DefaultSubagentConcurrency); err != nil {
		return nil, err
	}
	if _, err := config.Resolve(cfg.Model); err != nil {
		return nil, err
	}

	m := &Manager{
		cfg:          cfg,
		skillRoots:   skills.Roots(cfg.Skills),
		memoryRoots:  cfg.Memory.Roots(),
		skillSection: skills.PromptSection(cfg.Skills),
		skillNames:   skills.Names(cfg.Skills),
		journal:      tools.NewDirJournal(filepath.Join(cfg.SessionDir, "journal", journalDirKey(cfg.Cwd)), cfg.Cwd),
		sessions:     make(map[string]*Session),
		stop:         make(chan struct{}),
	}
	// The shadow repo sits next to the journal. Failure (no git, an
	// unwritable dir) turns checkpointing off, not the server: rewind still
	// works, just without file restoration.
	if shadow, err := NewShadowRepo(filepath.Join(cfg.SessionDir, "checkpoints", journalDirKey(cfg.Cwd)), cfg.Cwd); err != nil {
		fmt.Fprintf(os.Stderr, "checkpointing unavailable: %v\n", err)
	} else {
		m.shadow = shadow
	}
	m.wg.Add(1)
	go m.reap()
	return m, nil
}

// journalDirKey keeps one journal per workspace under the session dir. The
// hash is of the path only because "same cwd" is the identity that matters.
func journalDirKey(cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	return hex.EncodeToString(sum[:])[:16]
}

func (m *Manager) Cwd() string { return m.cfg.Cwd }

// Journal is the workspace file pre-image store, for the /api/workspace
// handlers. Every session's registry writes through it.
func (m *Manager) Journal() *tools.DirJournal { return m.journal }

// Skills are the skills in effect for every session on this server.
func (m *Manager) Skills() []skills.Skill { return m.cfg.Skills }

// Close cancels every run and releases every session.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	list := make([]*Session, 0, len(m.sessions))
	for id, s := range m.sessions {
		list = append(list, s)
		delete(m.sessions, id)
	}
	m.mu.Unlock()

	close(m.stop)
	for _, s := range list {
		s.Cancel()
		s.wait()
		s.closeTerminal()
		s.hub.Close()
	}
	m.wg.Wait()
}

// List summarises the sessions on disk.
func (m *Manager) List() ([]session.Info, error) {
	return session.List(m.cfg.SessionDir)
}

// UpdateInfo applies sidebar edits (pin, rename) to a session. They are
// appended as a meta record, not patched over the creation record: the
// transcript stays append-only, and the next List merges the latest values.
func (m *Manager) UpdateInfo(id string, title *string, pinned *bool) error {
	sess, err := m.Get(id)
	if err != nil {
		return err
	}
	return sess.SetInfo(title, pinned)
}

// Create starts a new session. workspace is a directory relative to the
// server root ("" or "." picks the root itself); it becomes the session's
// working directory, which the path guard then confines the file tools to.
func (m *Manager) Create(model, workspace string) (*Session, error) {
	if model == "" {
		model = m.cfg.Model
	}
	cfg, err := config.Resolve(model)
	if err != nil {
		return nil, err
	}
	cwd, err := m.resolveWorkspace(workspace)
	if err != nil {
		return nil, err
	}
	store, err := session.Create(m.cfg.SessionDir, cwd, cfg.Model, m.skillNames...)
	if err != nil {
		return nil, err
	}
	s := m.newSession(store, cfg, nil, cwd, llm.Usage{}, llm.Usage{})

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("server is shutting down")
	}
	m.sessions[s.ID] = s
	return s, nil
}

// resolveWorkspace turns a client-supplied relative directory into an absolute
// path inside the server root. Anything escaping the root — absolute paths or
// ".." — is rejected: the session cwd is where the path guard bites, so
// letting it leave the root would move the guard with it.
func (m *Manager) resolveWorkspace(rel string) (string, error) {
	if rel == "" || rel == "." {
		return m.cfg.Cwd, nil
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("workspace must be relative to the server root: %q", rel)
	}
	// Join cleans the result, so the prefix check below is what keeps
	// "a/../../elsewhere" from escaping.
	abs := filepath.Join(m.cfg.Cwd, rel)
	if !insideRoot(m.cfg.Cwd, abs) {
		return "", fmt.Errorf("workspace escapes the server root: %q", rel)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("workspace %q: %w", rel, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("workspace is not a directory: %q", rel)
	}
	return abs, nil
}

// sessionCwd validates the working directory a transcript was created with. A
// hand-edited session file could point anywhere on disk, and honouring it
// would move the path guard outside the server root — so anything suspicious
// quietly falls back to the root.
func (m *Manager) sessionCwd(meta *session.Meta) string {
	if meta == nil || meta.Cwd == "" {
		return m.cfg.Cwd
	}
	abs, err := filepath.Abs(meta.Cwd)
	if err != nil || !insideRoot(m.cfg.Cwd, abs) {
		return m.cfg.Cwd
	}
	return abs
}

// insideRoot reports whether abs is the root itself or lives underneath it.
func insideRoot(root, abs string) bool {
	return abs == root || strings.HasPrefix(abs, root+string(filepath.Separator))
}

// Get returns a live session, loading it from disk when it is not in memory.
func (m *Manager) Get(id string) (*Session, error) {
	if !validID(id) {
		return nil, ErrNotFound
	}
	m.mu.Lock()
	if s, ok := m.sessions[id]; ok {
		s.touch()
		m.mu.Unlock()
		return s, nil
	}
	m.mu.Unlock()

	path := filepath.Join(m.cfg.SessionDir, id+".jsonl")
	if _, err := os.Stat(path); err != nil {
		return nil, ErrNotFound
	}
	store, err := session.Open(path)
	if err != nil {
		return nil, err
	}
	// A recovered transcript that says nothing is indistinguishable from a healthy
	// one. The browser has no place to show this yet, so it goes to the server
	// console, which is where an operator would look for it.
	for _, d := range store.Diagnostics() {
		fmt.Fprintf(os.Stderr, "session %s: %s\n", id, d)
	}
	cfg, err := config.Resolve(m.modelOf(id))
	if err != nil {
		return nil, err
	}
	usage, delegated := store.UsageTotals()
	s := m.newSession(store, cfg, store.TimedMessages(""), m.sessionCwd(store.Meta()), usage, delegated)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("server is shutting down")
	}
	// Another request may have loaded the same session while this one was reading
	// the file. Whoever registered first wins, so a session never has two Agents.
	if existing, ok := m.sessions[id]; ok {
		s.hub.Close()
		existing.touch()
		return existing, nil
	}
	m.sessions[id] = s
	return s, nil
}

// Delete removes a session file. An in-progress run blocks it: deleting the
// transcript of something still writing to it loses data for no reason.
func (m *Manager) Delete(id string) error {
	if !validID(id) {
		return ErrNotFound
	}
	m.mu.Lock()
	s, live := m.sessions[id]
	if live && s.Active() {
		m.mu.Unlock()
		return ErrRunActive
	}
	if live {
		delete(m.sessions, id)
	}
	m.mu.Unlock()

	if live {
		s.closeTerminal()
		s.hub.Close()
	}
	if err := os.Remove(filepath.Join(m.cfg.SessionDir, id+".jsonl")); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// modelOf recovers the model recorded in a session's metadata, falling back to
// the server default. A session written by an older build, or with a model that
// has since left the catalog, still opens.
func (m *Manager) modelOf(id string) string {
	list, err := session.List(m.cfg.SessionDir)
	if err != nil {
		return m.cfg.Model
	}
	for _, info := range list {
		if info.ID == id && info.Model != "" {
			if _, err := config.Resolve(info.Model); err == nil {
				return info.Model
			}
		}
	}
	return m.cfg.Model
}

// newSession wires a transcript store into a live Session. cwd is the
// session's working directory — the server root or one of its subdirectories —
// and it is what the tool registry's path guard confines file tools to. The
// journal stays the shared root one: its paths are root-relative, so writes
// from any workspace subdirectory still land in the same workspace view.
//
// usage/delegated are the totals recovered from the transcript's stats
// records (zero for a fresh session). They seed three places that must agree:
// the agent's counters, the hub's snapshot, and the recorded baseline that
// keeps the next finish() from re-recording history as a delta.
func (m *Manager) newSession(store *session.Store, cfg config.Resolved, history []session.Timed, cwd string, usage, delegated llm.Usage) *Session {
	hub := NewHub()
	hub.Seed(history)
	hub.SetRunInfo(cfg.Model, cfg.Provider, cfg.ContextWindow)
	hub.SetUsage(usage)

	policy := NewPolicy()
	s := &Session{
		ID:        strings.TrimSuffix(filepath.Base(store.Path()), ".jsonl"),
		mgr:       m,
		hub:       hub,
		policy:    policy,
		gate:      NewWebGate(hub, policy, m.cfg.GateTimeout),
		store:     store,
		cfg:       cfg,
		cwd:       cwd,
		persisted: len(history),
		recorded:  session.Recorded{Usage: usage, Delegated: delegated},
		lastUsed:  time.Now(),
	}
	editCfg, _ := agent.ParseContextEdit(m.cfg.ContextEdit, cfg.ContextWindow)
	s.agent = agent.New(agent.Config{
		Client: s.client(cfg),
		// The subagent tool is registered here too, not only in the terminal: the
		// browser is where an approval gate exists, so it is the mode where
		// delegating work is most useful and most in need of supervision.
		Registry: tools.New(tools.Options{
			Cwd:       cwd,
			ReadRoots: m.skillRoots,
			// A browser session is always top level, so it always gets memory, for the
			// same reason it always gets the task list. Children are separate processes
			// and decide in toolOptions, where the depth is known.
			WriteRoots: m.memoryRoots,
			// A browser session is always top level, so it always gets the task list.
			// Children are spawned as separate processes and decide for themselves in
			// toolOptions, where the depth is known.
			Todo: true,
			// The journal is workspace-wide; ForSession stamps who touched first.
			Journal: m.journal.ForSession(s.ID),
			Subagent: &tools.Subagent{
				// MaxTurns is deliberately not inherited: see Subagent.MaxTurns. This
				// session's turn limit bounds this session's run.
				Cwd: cwd, Model: cfg.Model,
				// An explore child may be configured onto a different model than this
				// session's. Resolved from cfg rather than the manager's default, so
				// that switching models mid-session moves the subagent with it.
				ExploreModel: config.SubagentModel(cfg.Model),
				ExplorePool:  explorePool(),
				// The child's calls come back through the same gate the user is
				// already watching, marked with the subagent they came from. Passing
				// the gate rather than a copy of the policy is what makes "a
				// subagent never exceeds its parent" structural.
				Review: subagentReview(s.gate, s.hub),
			},
		}),
		SystemPrompt: agent.SystemPrompt(cwd, m.skillSection, m.cfg.Memory.PromptSection()),
		MaxTurns:     m.cfg.MaxTurns,
		Gate:         s.gate,
		// Half the run may go to waiting for approvals; the other half is for the
		// work. Without a cap, a batch of unanswered calls at GateTimeout apiece
		// outlasts RunTimeout and the run's own deadline kills the whole turn.
		ReviewBudget: m.cfg.RunTimeout / 2,
		// C1 RFC: stop detection - default disabled for web UI
		StagnationThreshold: 0,
		TokenBudget:         0,
		CostBudget:          0,
		TimeBudget:          0,
		// The CLI's checkpoint interval (agent.DefaultSoftTurns) is a flag
		// default, not a Config one: pinned to 0 here like the budgets.
		SoftTurns: 0,
		// Resolved per session against this session's model, because "auto" is half
		// that model's window. A bad spec was already rejected at startup, so an error
		// here can only mean the catalogue lost the model; clearing off is the safe
		// reading of that, and it is what ParseContextEdit returns anyway.
		ContextEdit: editCfg,
	})
	msgs := make([]llm.Message, 0, len(history))
	for _, t := range history {
		msgs = append(msgs, t.Message)
	}
	s.agent.SetMessages(msgs)
	s.agent.SetUsage(usage, delegated)
	hub.SetOverhead(s.agent.OverheadTokens())
	hub.SetClearTrigger(s.agent.ContextEditTrigger())
	return s
}

// explorePool adapts the configured subagent pool into the tool's plain shape;
// package tools stays ignorant of the catalogue.
func explorePool() []tools.ExploreTarget {
	pool := config.SubagentPool()
	out := make([]tools.ExploreTarget, 0, len(pool))
	for _, m := range pool {
		out = append(out, tools.ExploreTarget{Provider: m.Provider, Model: m.ID})
	}
	return out
}

// client builds an LLM client whose retry notices reach the browser. A
// rate-limited turn should look like waiting, not like a hang.
func (s *Session) client(cfg config.Resolved) llm.Client {
	onRetry := func(r llm.RetryInfo) {
		s.hub.Publish(Event{
			Type: EvRetry, Attempt: r.Attempt, Max: r.Max,
			DelayMS: r.Delay.Milliseconds(), Reason: r.Reason,
		})
	}
	if s.mgr.cfg.NewClient != nil {
		return s.mgr.cfg.NewClient(cfg, onRetry)
	}
	return llm.New(llm.Options{
		BaseURL:    cfg.BaseURL,
		APIKey:     cfg.APIKey,
		Model:      cfg.Model,
		MaxTokens:  cfg.MaxTokens,
		MaxRetries: s.mgr.cfg.Retries,
		OnRetry:    onRetry,
	})
}

// reap evicts idle sessions and gives up on runs that will never finish.
func (m *Manager) reap() {
	defer m.wg.Done()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.evictIdle(m.cfg.IdleTimeout)
		}
	}
}

// evictAll drops every idle session from memory. It is the eviction path with
// the clock taken out, which is what makes the reload-from-disk behaviour
// testable without waiting out a timeout.
func (m *Manager) evictAll() { m.evictIdle(0) }

func (m *Manager) evictIdle(maxIdle time.Duration) {
	var evicted []*Session
	m.mu.Lock()
	for id, s := range m.sessions {
		// Only a session with nothing running and nobody watching is a candidate.
		// Its state is on disk, so the next request rebuilds it transparently.
		if s.Active() || s.hub.Subscribers() > 0 {
			continue
		}
		if time.Since(s.idleSince()) < maxIdle {
			continue
		}
		delete(m.sessions, id)
		evicted = append(evicted, s)
	}
	m.mu.Unlock()

	for _, s := range evicted {
		s.closeTerminal()
		s.hub.Close()
	}
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validID keeps a session id from becoming a path. It is joined with the session
// directory, so anything that could escape it has to be rejected here.
func validID(id string) bool {
	return id != "" && len(id) <= 128 && idPattern.MatchString(id) &&
		!strings.Contains(id, "..") && !strings.ContainsAny(id, `/\`)
}

func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
