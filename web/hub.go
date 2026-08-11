package web

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/wangy/pi-go/llm"
	"github.com/wangy/pi-go/session"
)

// subBuffer is how far one subscriber may fall behind before it is dropped.
//
// Dropping is safe because of the log: the client notices the stream ended,
// reconnects with ?from=<lastSeq>, and is caught up from there. That turns the
// classic slow-consumer problem into something that needs no policy at all.
const subBuffer = 256

// maxLog bounds the replay log. Overflowing it costs a reconnecting client the
// incremental path, not its data: it falls back to a snapshot, which is rebuilt
// from state that is already correct.
const maxLog = 2000

// maxPendingOutput bounds the live output kept per running call.
//
// It has to be bounded here as well as at the source: the tool stops forwarding
// after its own budget, but this copy goes into every snapshot, so an unbounded
// one would make reconnecting more expensive the longer a command has been
// running.
const maxPendingOutput = 32 << 10

// maxPendingFrames bounds the structured progress events kept per running call.
//
// A count, not a byte budget: these are whole events, so dropping the oldest costs
// one line of history while cutting bytes would hand a client half a frame. Sized
// for what a delegated run actually produces — a few events per turn, and a
// subagent that has produced hundreds is looping, in which case the recent ones are
// the interesting ones anyway.
const maxPendingFrames = 400

// maxIncomingHead and maxIncomingTail bound the raw argument preview kept per
// incoming call: the head is where the path field lives, the tail is the line
// preview. Like maxPendingOutput this copy goes into every snapshot, so it must
// not grow with how much the model streams.
const (
	maxIncomingHead = 4096
	maxIncomingTail = 8192
)

// tailOf keeps the last n bytes, cutting on a line boundary when there is one
// nearby so the first visible line is not half a line.
func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < 200 {
		s = s[i+1:]
	}
	return s
}

// countContentNewlines counts the `\n` escape pairs of a raw JSON-arguments
// fragment — the newlines of the file being written. A `\\` pair (an escaped
// backslash) makes the following n literal text, not a newline, so escapes
// are tracked properly; carry holds a fragment's trailing lone backslash for
// the next fragment, because `\n` can be split across chunks.
func countContentNewlines(carry *bool, text string) int {
	lines := 0
	i := 0
	if *carry {
		if len(text) == 0 {
			return 0
		}
		if text[0] == 'n' {
			lines++
		}
		*carry = false
		i = 1
	}
	for ; i < len(text); i++ {
		if text[i] != '\\' {
			continue
		}
		if i+1 >= len(text) {
			*carry = true
			break
		}
		if text[i+1] == 'n' {
			lines++
		}
		i++ // the pair is consumed whatever it escapes
	}
	return lines
}

// Hub is one session's event log, live state, and fan-out.
//
// Everything a client can observe is derived here by folding published events,
// so a snapshot and a live stream cannot disagree: they are the same state
// machine read at different times.
type Hub struct {
	mu      sync.Mutex
	seq     int64
	log     []Event
	subs    map[int64]chan Event
	nextSub int64
	closed  bool

	msgs    []Message
	results map[string]ToolResult
	live    Live
	usage   llm.Usage
	policy  PolicyState
	run     RunInfo
	// context is the latest turn's prompt size, which is what fills the window.
	context int64
	// overhead is the fixed per-request cost (system prompt + tool schemas),
	// estimated once at session creation. It feeds the context meter's
	// breakdown display and nothing else.
	overhead int64
	// clearTrigger is the prompt size at which context clearing starts, or 0 when it
	// is off. Sent to clients because the gauge's warning bands are meaningless
	// without it: clearing holds occupancy just below this, so fixed percentages of
	// the window would leave the gauge permanently amber.
	clearTrigger int64

	msgSeq int
}

func NewHub() *Hub {
	return &Hub{
		subs:    make(map[int64]chan Event),
		results: make(map[string]ToolResult),
		policy:  PolicyState{Mode: string(ModeStandard)},
	}
}

// Publish assigns a sequence number, folds the event into the session state, and
// fans it out. It is the only writer of Hub state.
func (h *Hub) Publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}

	h.seq++
	e.Seq = h.seq
	if e.TS == 0 {
		e.TS = nowMS()
	}
	h.apply(&e)

	if logged(e.Type) {
		h.log = append(h.log, e)
		if len(h.log) > maxLog {
			h.log = h.log[len(h.log)-maxLog:]
		}
	}

	for id, ch := range h.subs {
		select {
		case ch <- e:
		default:
			// Too far behind. Close and forget it; it will come back with ?from=.
			close(ch)
			delete(h.subs, id)
		}
	}
}

// apply folds one event into the derived state, and fills in the fields the Hub
// owns rather than the publisher (message ids).
func (h *Hub) apply(e *Event) {
	switch e.Type {
	case EvRunStart:
		h.live = Live{RunID: e.RunID, Active: true}
		h.run.Active = true
		h.run.RunID = e.RunID
		if e.Model != "" {
			h.run.Model, h.run.Provider = e.Model, e.Provider
		}

	case EvUserMessage:
		h.msgSeq++
		e.MessageID = fmt.Sprintf("u%d", h.msgSeq)
		e.Role = string(llm.RoleUser)
		h.msgs = append(h.msgs, Message{
			ID:      e.MessageID,
			Role:    string(llm.RoleUser),
			Content: []llm.Block{{Type: llm.BlockText, Text: e.Text}},
			TS:      e.TS,
		})

	case EvTurnStart:
		// One turn produces exactly one assistant message, so this is where its
		// id is minted. Deltas inherit it, which is how the browser knows which
		// bubble to append to.
		h.msgSeq++
		h.live.Turn = e.Turn
		h.live.MessageID = fmt.Sprintf("m%d", h.msgSeq)
		h.live.Text, h.live.Thinking = "", ""
		e.MessageID = h.live.MessageID

	case EvToken:
		e.MessageID = h.live.MessageID
		h.live.Text += e.Text

	case EvThinking:
		e.MessageID = h.live.MessageID
		h.live.Thinking += e.Text

	case EvMessage:
		// Input is prompt_tokens, which already includes the cached prefix
		// (cached_tokens is a subset of it in this protocol), so it is the whole
		// context size on its own.
		if e.Usage != nil && e.Usage.Input > 0 {
			h.context = e.Usage.Input
		}
		if h.live.MessageID == "" {
			h.msgSeq++
			h.live.MessageID = fmt.Sprintf("m%d", h.msgSeq)
		}
		e.MessageID = h.live.MessageID
		h.msgs = append(h.msgs, Message{
			ID: e.MessageID, Role: e.Role, Content: e.Content, TS: e.TS,
		})
		// The settled message supersedes the accumulated text, so the live copy
		// is dropped rather than left to be rendered twice.
		h.live.Text, h.live.Thinking = "", ""

	case EvToolStart:
		// The arguments are settled and the pending-tool card takes over, so the
		// incoming preview goes away — otherwise the same call would render twice.
		h.live.Incoming = dropIncoming(h.live.Incoming, e.CallID)
		h.live.PendingTools = append(h.live.PendingTools, PendingTool{
			CallID: e.CallID, Name: e.Name, Args: e.Args, StartedAt: e.TS,
		})

	case EvToolArgs:
		// Folded into the incoming list rather than logged, so a client that
		// reconnects mid-generation still sees the preview. Entries are ordered
		// by arrival: the first event for a call creates its entry.
		i := -1
		for j := range h.live.Incoming {
			if h.live.Incoming[j].CallID == e.CallID {
				i = j
				break
			}
		}
		if i < 0 {
			h.live.Incoming = append(h.live.Incoming, IncomingCall{CallID: e.CallID, TS: e.TS})
			i = len(h.live.Incoming) - 1
		}
		inc := &h.live.Incoming[i]
		// The name-bearing event normally arrives first; this covers a provider
		// that names the call only after some arguments.
		if inc.Name == "" {
			inc.Name = e.Name
		}
		inc.Bytes += len(e.Text)
		inc.Lines += countContentNewlines(&inc.escPending, e.Text)
		if len(inc.Head) < maxIncomingHead {
			// A byte-wise cut is fine: the head is display-only, never parsed.
			inc.Head += e.Text[:min(maxIncomingHead-len(inc.Head), len(e.Text))]
		}
		inc.Tail = tailOf(inc.Tail+e.Text, maxIncomingTail)

	case EvToolPartial:
		// Folded into the pending call rather than logged, so a client that
		// reconnects mid-command still sees the output so far. Keeping the tail
		// matches what the settled result will contain: the last lines are the ones
		// that say how it is going.
		for i := range h.live.PendingTools {
			if h.live.PendingTools[i].CallID != e.CallID {
				continue
			}
			h.live.PendingTools[i].Output = tailOf(h.live.PendingTools[i].Output+e.Text, maxPendingOutput)
			if len(e.Frame) > 0 {
				f := append(h.live.PendingTools[i].Frames, e.Frame)
				if len(f) > maxPendingFrames {
					// Copied rather than resliced: keeping the tail of the old array
					// would pin the whole backing store for the life of the call.
					f = append([]json.RawMessage(nil), f[len(f)-maxPendingFrames:]...)
				}
				h.live.PendingTools[i].Frames = f
			}
		}

	case EvToolEnd:
		// Defensive for the incoming preview: every settled call passed through
		// tool_start, which already dropped it, but a stray entry must not linger.
		h.live.Incoming = dropIncoming(h.live.Incoming, e.CallID)
		h.live.PendingTools = dropTool(h.live.PendingTools, e.CallID)
		h.results[e.CallID] = ToolResult{
			CallID: e.CallID, Name: e.Name, Text: e.Text,
			IsError: e.IsError, Details: e.Details,
		}

	case EvGateRequest:
		h.live.PendingGates = append(h.live.PendingGates, PendingGate{
			GateID: e.GateID, CallID: e.CallID, Tool: e.Name,
			Args: e.Args, Deadline: e.Deadline, Danger: e.Danger,
		})

	case EvGateDeadline:
		for i := range h.live.PendingGates {
			if h.live.PendingGates[i].GateID == e.GateID {
				h.live.PendingGates[i].Deadline = e.Deadline
			}
		}

	case EvGateResolved:
		h.live.PendingGates = dropGate(h.live.PendingGates, e.GateID)

	case EvPolicyChanged, EvPolicyReverted:
		if e.Policy != nil {
			h.policy = *e.Policy
		}

	case EvModelChanged:
		h.run.Model, h.run.Provider = e.Model, e.Provider
		if e.ContextWindow > 0 {
			h.run.ContextWindow = e.ContextWindow
		}

	case EvRunEnd:
		if e.Usage != nil {
			h.usage = *e.Usage
		}
		h.live = Live{}
		h.run.Active = false
		h.run.RunID = ""
	}
}

// Seed loads a session's persisted history into the derived state. It must be
// called before the first run, while nothing else can be publishing.
//
// tool_result blocks are lifted out of their user messages into the results
// table so that a resumed session has exactly the shape a live one does. Their
// tool name is recovered from the assistant message that requested them, since
// the result block itself does not carry it.
func (h *Hub) Seed(history []session.Timed) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seedLocked(history)
}

func (h *Hub) seedLocked(history []session.Timed) {
	names := make(map[string]string)
	for _, m := range history {
		var kept []llm.Block
		for _, b := range m.Message.Content {
			switch b.Type {
			case llm.BlockToolUse:
				names[b.ID] = b.Name
				kept = append(kept, b)
			case llm.BlockToolResult:
				h.results[b.ToolUseID] = ToolResult{
					CallID: b.ToolUseID, Name: names[b.ToolUseID],
					Text: b.Text, IsError: b.IsError,
					// Passed through as raw JSON rather than decoded into a
					// concrete type: the Hub has no reason to know which tool
					// produced which shape, and json.RawMessage re-marshals
					// verbatim. This is what makes a reloaded diff identical to a
					// live one instead of missing.
					Details: detailsOrNil(b.Details),
				}
			default:
				kept = append(kept, b)
			}
		}
		if len(kept) == 0 {
			continue
		}
		h.msgSeq++
		prefix := "m"
		if m.Message.Role == llm.RoleUser {
			prefix = "u"
		}
		h.msgs = append(h.msgs, Message{
			ID: fmt.Sprintf("%s%d", prefix, h.msgSeq), Role: string(m.Message.Role), Content: kept,
			TS: m.Time,
		})
	}
}

// Reset rebuilds the derived state from a rewound branch. Everything the
// timeline shows is replaced: messages, results, live state, and the context
// occupancy. The replay log goes with them — its events describe the
// abandoned branch, so a reconnecting client must fall back to a snapshot
// (canReplay now fails), which carries exactly this state. Subscriptions
// survive: they belong to the session, not to the branch. The sequence
// counter keeps climbing, since SSE resume semantics depend on it never
// going backwards.
func (h *Hub) Reset(history []session.Timed, usage llm.Usage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = nil
	h.results = make(map[string]ToolResult)
	h.live = Live{}
	h.context = 0
	h.usage = usage
	h.msgSeq = 0
	h.log = nil
	h.seedLocked(history)
}

// UserMessageOrdinal reports which visible user message id is, counting only
// user messages and starting at 1. The store counts records by the same rule
// (session.Store.RewindPoint), which is what lets a rewind line the two up
// without either side knowing the other's ids.
func (h *Hub) UserMessageOrdinal(id string) (int, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.msgs {
		if m.Role != string(llm.RoleUser) {
			continue
		}
		n++
		if m.ID == id {
			return n, true
		}
	}
	return 0, false
}

// detailsOrNil keeps an absent payload absent. Handing an empty json.RawMessage
// to an `any` field would serialise as the invalid value “ rather than being
// omitted, which breaks the whole response and not just this one field.
func detailsOrNil(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

// SetRunInfo records the model a session will use, so a client that connects
// before the first run still knows what it is talking to.
func (h *Hub) SetRunInfo(model, provider string, contextWindow int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.run.Model, h.run.Provider, h.run.ContextWindow = model, provider, contextWindow
}

// SetOverhead records the fixed prompt overhead, so a snapshot can split the
// context occupancy into overhead and conversation. Set once at session
// creation, before the first subscriber.
func (h *Hub) SetOverhead(tokens int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.overhead = tokens
}

// SetClearTrigger records the prompt size at which context clearing begins, so a
// snapshot can tell the gauge what to measure its warning bands against. Zero means
// clearing is off.
//
// Unlike SetOverhead this is not set once: "auto" is a fraction of the model's
// window, so switching models moves it. SetModel updates it for that reason.
func (h *Hub) SetClearTrigger(tokens int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clearTrigger = tokens
}

// SetUsage seeds the running cost total recovered from the transcript's stats
// records on reopen. Set once, before the first subscriber — same rule as
// SetOverhead.
func (h *Hub) SetUsage(u llm.Usage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.usage = u
}

// Seq is the last assigned sequence number.
func (h *Hub) Seq() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seq
}

// Snapshot returns the current state without subscribing.
func (h *Hub) Snapshot() Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshotLocked()
}

func (h *Hub) snapshotLocked() Snapshot {
	results := make(map[string]ToolResult, len(h.results))
	for k, v := range h.results {
		results[k] = v
	}
	msgs := append([]Message(nil), h.msgs...)
	if msgs == nil {
		msgs = []Message{}
	}
	return Snapshot{
		Seq:            h.seq,
		Messages:       msgs,
		Results:        results,
		Live:           h.live.clone(),
		Run:            h.run,
		Policy:         h.policy,
		Usage:          h.usage,
		ContextTokens:  h.context,
		OverheadTokens: h.overhead,
		ClearTrigger:   h.clearTrigger,
	}
}

// Subscribe registers a listener and returns whatever it needs to catch up
// first. Backlog and registration happen under one lock, which is what makes the
// handoff lossless in both directions: no event can slip through between the two.
//
// A negative from asks for a snapshot. A non-negative one asks for the events
// after that sequence number, and silently degrades to a snapshot when the log no
// longer reaches back that far.
func (h *Hub) Subscribe(from int64) (backlog []Event, ch <-chan Event, cancel func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if from >= 0 && h.canReplay(from) {
		for _, e := range h.log {
			if e.Seq > from {
				backlog = append(backlog, e)
			}
		}
	} else {
		snap := h.snapshotLocked()
		backlog = []Event{{Seq: snap.Seq, Type: EvSnapshot, TS: nowMS(), Snapshot: &snap}}
	}

	if h.closed {
		// Nothing more will arrive; hand back a closed channel so the caller can
		// still deliver the backlog and then finish.
		dead := make(chan Event)
		close(dead)
		return backlog, dead, func() {}
	}

	h.nextSub++
	id := h.nextSub
	sub := make(chan Event, subBuffer)
	h.subs[id] = sub
	return backlog, sub, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if c, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(c)
		}
	}
}

// canReplay reports whether the log still covers everything after from.
func (h *Hub) canReplay(from int64) bool {
	if from >= h.seq {
		return true // Already current; nothing to replay.
	}
	if len(h.log) == 0 {
		// No logged events at all. Only safe if the client has seen everything
		// that could have been logged.
		return from >= h.seq
	}
	return h.log[0].Seq <= from+1
}

// Subscribers is the current listener count, which is what idle eviction keys
// off.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Close ends every subscription. Called when a session is evicted from memory.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for id, ch := range h.subs {
		close(ch)
		delete(h.subs, id)
	}
}

func dropTool(list []PendingTool, callID string) []PendingTool {
	for i := range list {
		if list[i].CallID == callID {
			return append(list[:i:i], list[i+1:]...)
		}
	}
	return list
}

func dropIncoming(list []IncomingCall, callID string) []IncomingCall {
	for i := range list {
		if list[i].CallID == callID {
			return append(list[:i:i], list[i+1:]...)
		}
	}
	return list
}

func dropGate(list []PendingGate, gateID string) []PendingGate {
	for i := range list {
		if list[i].GateID == gateID {
			return append(list[:i:i], list[i+1:]...)
		}
	}
	return list
}
