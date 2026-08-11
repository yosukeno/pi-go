// Package web serves the browser interface. It is a second consumer of
// agent.Event, alongside the terminal renderer in main.go.
//
// The one structural decision everything else follows from: a run's lifetime
// belongs to the session, not to an HTTP connection. Starting a turn and
// subscribing to its events are two different requests, so closing the browser
// cannot cancel the work. See hub.go for the replay that makes reconnecting
// cheap, and manager.go for the timeouts that replace "the client hung up" as
// the way runs eventually end.
package web

import (
	"encoding/json"
	"time"

	"github.com/wangy/pi-go/agent"
	"github.com/wangy/pi-go/llm"
	"github.com/wangy/pi-go/wire"
)

// EventType is the SSE `event:` name.
//
// An alias for wire.Type, not a separate type: the loop-derived names below are
// the shared contract, and `pi-go -mode json` emits the same ones. Aliasing keeps
// the two from drifting while leaving every existing use site in this package
// unchanged.
type EventType = wire.Type

const (
	// The loop-derived names live in package wire, which is the single definition
	// shared with the JSON mode emitter. Referencing them here rather than
	// redeclaring the strings is what makes "one definition, two consumers" true
	// instead of aspirational.
	EvUserMessage = wire.UserMessage
	EvTurnStart   = wire.TurnStart
	EvThinking    = wire.Thinking
	EvToken       = wire.Token
	EvMessage     = wire.Message
	EvToolArgs    = wire.ToolArgs
	EvToolStart   = wire.ToolStart
	EvToolPartial = wire.ToolPartial
	EvToolEnd     = wire.ToolEnd
	EvRunEnd      = wire.RunEnd
)

// The rest are web's own: they describe the browser session rather than the loop,
// so they deliberately do not travel in the JSON mode contract.
const (
	// EvSnapshot is always the first frame of a subscription that did not ask to
	// resume from a sequence number.
	EvSnapshot EventType = "snapshot"

	EvRunStart EventType = "run_start"
	EvRetry    EventType = "retry"
	EvError    EventType = "error"

	// The gate publishes these itself rather than through the loop's event
	// channel, because the loop only consumes a verdict and knows nothing about
	// approval as a concept.
	//
	// One consequence clients must handle: a gate_request can arrive *before* the
	// assistant `message` that requested the call. The loop emits that message and
	// then enters the gate from its own goroutine, so the two publishes race over
	// the Hub lock. A snapshot is always self-consistent; a live stream is not
	// ordered across those two paths.
	EvGateRequest  EventType = "gate_request"
	EvGateDeadline EventType = "gate_deadline"
	EvGateResolved EventType = "gate_resolved"
	EvGateAuto     EventType = "gate_auto"

	EvPolicyChanged  EventType = "policy_changed"
	EvPolicyReverted EventType = "policy_reverted"
	EvModelChanged   EventType = "model_changed"

	// EvRewound tells subscribers the session was rewound to an earlier
	// message: everything they hold past that point describes a branch that
	// no longer exists, and only a fresh snapshot rebuilds the truth.
	EvRewound EventType = "rewound"

	// EvCompacted tells subscribers the conversation was replaced by a summary of
	// itself. Structurally the same news as EvRewound — everything held is stale and
	// only a fresh snapshot rebuilds it — and deliberately not the same name: one is
	// a branch the user abandoned and the other is the whole history condensed, and a
	// client that wanted to say so could not if they shared a type.
	EvCompacted EventType = "compacted"
)

// logged reports whether an event goes into the replay log.
//
// Only semantic events do. The incremental kinds are fanned out live so the
// typing effect is not lost, but they are folded into the live state instead of
// being stored: a reconnecting browser wants the current text, not a replay of
// the typing. That is what keeps memory proportional to the transcript rather
// than to the token count — and, for tool_args and tool_partial, to the
// transcript rather than to how much a model streamed or a command printed.
func logged(t EventType) bool {
	switch t {
	case EvToken, EvThinking, EvToolArgs, EvToolPartial, EvSnapshot, EvRewound, EvCompacted:
		// EvRewound joins the unlogged kinds: it is a control signal, not
		// history, and a reconnecting client already gets the post-rewind
		// truth from the snapshot the cleared log forces it to take.
		return false
	}
	return true
}

// Event is one SSE frame. It is a flat union rather than a nested `data` object
// so that each field has a name and a type in exactly one place; the cost is a
// wide struct, the benefit is that no consumer has to guess a payload shape.
type Event struct {
	Seq  int64     `json:"seq"`
	Type EventType `json:"type"`
	TS   int64     `json:"ts"`

	// Run lifecycle.
	RunID         string `json:"run_id,omitempty"`
	Model         string `json:"model,omitempty"`
	Provider      string `json:"provider,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
	Turn          int    `json:"turn,omitempty"`
	// ContextEdit rides on turn_start when this turn's prompt had old tool results
	// cleared from it. Present only on the turns that cleared something, so it is the
	// explanation for a context gauge that just dropped rather than a per-turn field.
	ContextEdit *agent.ContextEdit `json:"context_edit,omitempty"`

	// Messages and streaming. MessageID is filled in by the Hub, which owns the
	// numbering, so publishers never have to track it.
	MessageID string      `json:"message_id,omitempty"`
	Role      string      `json:"role,omitempty"`
	Text      string      `json:"text,omitempty"`
	Content   []llm.Block `json:"content,omitempty"`

	// Tool calls. CallID is the identity that ties tool_start, tool_end and any
	// gate traffic together; the browser files everything by it, which is why
	// out-of-order completion in a parallel batch needs no special handling.
	CallID  string          `json:"call_id,omitempty"`
	Name    string          `json:"name,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
	// Details is a tool's structured payload (tools.EditDetails and friends). It
	// never enters the conversation, which is why the diff can be full length
	// here while the model only ever sees the truncated text.
	Details any `json:"details,omitempty"`

	// Frame is one structured progress event from a tool that reports them, on
	// EvToolPartial. Today only a subagent, whose child emits this same contract one
	// level down, so the browser renders a delegated run with the code it already has.
	Frame json.RawMessage `json:"frame,omitempty"`

	// Approval gate. Deadline is absolute epoch milliseconds, never a duration:
	// a client that reloads has to be able to recompute the remaining time, and
	// a countdown that started on the server cannot survive that.
	GateID   string   `json:"gate_id,omitempty"`
	Deadline int64    `json:"deadline,omitempty"`
	Danger   []string `json:"danger,omitempty"`
	Allow    bool     `json:"allow,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	// By records who decided: user, timeout, cancel, or rule:<name>.
	By   string `json:"by,omitempty"`
	Rule string `json:"rule,omitempty"`

	// Policy.
	Policy *PolicyState `json:"policy,omitempty"`
	From   string       `json:"from,omitempty"`
	To     string       `json:"to,omitempty"`

	// Retry, from the llm.OnRetry hook.
	Attempt int   `json:"attempt,omitempty"`
	Max     int   `json:"max,omitempty"`
	DelayMS int64 `json:"delay_ms,omitempty"`

	// Run end.
	StopReason string `json:"stop_reason,omitempty"`
	// EndReason is why the run ended, in the harness's own vocabulary rather than
	// the provider's — the only field that can say "turn_limit" or "stagnation".
	// Defined in agent/endreason.go and carried here unchanged, so the browser and a
	// script reading the JSONL stream cannot disagree about what ended a run.
	EndReason string     `json:"end_reason,omitempty"`
	Usage     *llm.Usage `json:"usage,omitempty"`
	Error     string     `json:"error,omitempty"`
	// Undelivered lists steering messages the run accepted but never passed to the
	// model. A client should offer the text back rather than let it disappear.
	Undelivered []string `json:"undelivered,omitempty"`

	Snapshot *Snapshot `json:"snapshot,omitempty"`
}

// Message is a settled conversation entry as the browser sees it.
//
// tool_result blocks are deliberately absent: they are keyed out into
// Snapshot.Results so that live rendering and replayed history have the same
// shape. Without that split, a session loaded from JSONL would arrive with
// results inline while a live one gets them as events.
type Message struct {
	ID      string      `json:"id"`
	Role    string      `json:"role"`
	Content []llm.Block `json:"content"`
	// TS is when the message was recorded, epoch ms. Zero for a message from a
	// session file old enough to predate it being threaded through.
	TS int64 `json:"ts,omitempty"`
}

// ToolResult is one finished call, filed by call id.
type ToolResult struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name,omitempty"`
	Text    string `json:"text"`
	IsError bool   `json:"is_error,omitempty"`
	Details any    `json:"details,omitempty"`
}

// PendingTool is a call that has started and not yet finished.
type PendingTool struct {
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Args      json.RawMessage `json:"args,omitempty"`
	StartedAt int64           `json:"started_at"`
	// Output is what the call has printed so far, for the tools that report it.
	//
	// Accumulated here rather than left to the client because it is the only way a
	// browser that reconnects mid-command sees anything: the fragments are not
	// logged, so there is nothing to replay. Bounded by maxPendingOutput.
	Output string `json:"output,omitempty"`
	// Frames are the structured progress events of a call that reports them — today
	// a subagent, whose child produces this same contract one level down. Kept here
	// for the same reason Output is: the fragments are not logged, so a browser that
	// reconnects mid-delegation would otherwise see an empty card.
	//
	// Bounded by maxPendingFrames, and by count rather than by bytes because these
	// are whole events: dropping the oldest loses one line of history, while cutting
	// bytes would hand the client half a frame it cannot parse.
	Frames []json.RawMessage `json:"frames,omitempty"`
}

// IncomingCall is a tool call whose arguments the model is still streaming in —
// before review, before tool_start. It is what lets the browser show a
// progressive preview card while a large write is being generated.
type IncomingCall struct {
	CallID string `json:"call_id"`
	Name   string `json:"name"`
	// Head and Tail are raw — still JSON-escaped — argument text: the path field
	// lives in the head, a line preview in the tail. The client does the lenient
	// display-unescaping; the server stays dumb. Both are bounded because this
	// copy goes into every snapshot.
	Head string `json:"head,omitempty"`
	Tail string `json:"tail,omitempty"`
	// Bytes is the total fragment bytes seen, so the card can report progress
	// beyond what Head and Tail keep.
	Bytes int `json:"bytes"`
	// Lines counts content newlines seen so far (the two-character `\n` escape
	// in the raw arguments), so the UI can show a live +N next to the path.
	Lines int `json:"lines"`
	// escPending is the cross-fragment carry: a fragment may end with the lone
	// backslash of an escape whose second char arrives in the next one. It is
	// process-local state, never serialized.
	escPending bool
	TS         int64 `json:"ts,omitempty"`
}

// PendingGate is an approval still waiting on a human.
type PendingGate struct {
	GateID   string          `json:"gate_id"`
	CallID   string          `json:"call_id"`
	Tool     string          `json:"tool"`
	Args     json.RawMessage `json:"args,omitempty"`
	Deadline int64           `json:"deadline"`
	Danger   []string        `json:"danger,omitempty"`
}

// Live is everything in flight: partial text, unfinished calls, undecided
// gates. It is the part of a snapshot that cannot be read back from the session
// file.
type Live struct {
	RunID     string `json:"run_id,omitempty"`
	Active    bool   `json:"active"`
	Turn      int    `json:"turn,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Text      string `json:"text,omitempty"`
	// The slices are non-nil in JSON so clients can index them without a guard.
	PendingTools []PendingTool `json:"pending_tools"`
	PendingGates []PendingGate `json:"pending_gates"`
	// Incoming holds calls whose arguments are still streaming in, ordered by
	// arrival. Entries leave when their tool_start (or tool_end) arrives.
	Incoming []IncomingCall `json:"incoming"`
}

func (l Live) clone() Live {
	out := l
	out.PendingTools = append([]PendingTool(nil), l.PendingTools...)
	out.PendingGates = append([]PendingGate(nil), l.PendingGates...)
	out.Incoming = append([]IncomingCall(nil), l.Incoming...)
	if out.PendingTools == nil {
		out.PendingTools = []PendingTool{}
	}
	if out.PendingGates == nil {
		out.PendingGates = []PendingGate{}
	}
	if out.Incoming == nil {
		out.Incoming = []IncomingCall{}
	}
	return out
}

// RunInfo tells a fresh client whether it may send a prompt, and what it is
// talking to.
type RunInfo struct {
	Active   bool   `json:"active"`
	RunID    string `json:"run_id,omitempty"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	// ContextWindow comes from the model catalog. It travels with the session so
	// the client does not have to join a model id against /api/models, and so an
	// unknown model degrades to "no meter" rather than to a wrong one.
	ContextWindow int `json:"context_window,omitempty"`
}

// PolicyState is the gate configuration. It has to travel in the snapshot:
// policy lives on the server, so a reload must not make the auto-mode banner
// disappear while the gate is in fact still open.
type PolicyState struct {
	Mode string `json:"mode"`
	// RemainingTurns is only set for a turn-limited auto mode; zero means "no
	// limit applies".
	RemainingTurns int `json:"remaining_turns,omitempty"`
}

// Snapshot is the whole session state as of Seq.
type Snapshot struct {
	Seq      int64                 `json:"seq"`
	Messages []Message             `json:"messages"`
	Results  map[string]ToolResult `json:"results"`
	Live     Live                  `json:"live"`
	Run      RunInfo               `json:"run"`
	Policy   PolicyState           `json:"policy"`
	// Usage is the session's running total, for cost.
	Usage llm.Usage `json:"usage"`
	// ContextTokens is the most recent turn's prompt size, for occupancy. It is
	// deliberately not derived from Usage: that one accumulates across turns and
	// would read many times the window on a long session.
	ContextTokens int64 `json:"context_tokens"`
	// OverheadTokens is the estimated fixed cost of every request (system prompt
	// plus tool schemas). It is an estimate for the context meter's breakdown,
	// never a measurement and never a decision input.
	OverheadTokens int64 `json:"overhead_tokens,omitempty"`
	// ClearTrigger is the prompt size at which context clearing begins, or absent
	// when it is off. The gauge needs it because clearing holds occupancy just below
	// it: bands drawn at fixed fractions of the window would sit permanently in the
	// warning colour and stop carrying information.
	ClearTrigger int64 `json:"clear_trigger,omitempty"`
}

func nowMS() int64 { return time.Now().UnixMilli() }

// fromAgentEvent translates a loop event into a wire event. The second return
// value is false for events the web layer publishes itself with more context
// than the loop has: agent_start becomes run_start, which also carries the run
// id and the model.
// The mapping itself lives in wire.FromAgent, shared with the JSON mode emitter.
// This function only widens the result into the browser's frame, so a new event
// kind is wired up in one place rather than two — and the browser and a script
// cannot disagree about what `tool_end` means.
//
// Steering arrives as an ordinary user message, because that is what it is: the
// client already knows how to draw one, and the timeline should show it at the
// point it entered the conversation rather than when it was typed. That decision
// is wire's now (wire.UserMessage), which is why it is not repeated here.
func fromAgentEvent(e agent.Event) (Event, bool) {
	w, ok := wire.FromAgent(e)
	if !ok {
		return Event{}, false
	}
	// TS and Seq stay the Hub's job: it owns the numbering, so a publisher never
	// has to track it. wire's timestamp is discarded for the same reason.
	return Event{
		Type:        w.Type,
		Turn:        w.Turn,
		ContextEdit: w.ContextEdit,
		Role:        w.Role,
		Text:        w.Text,
		Content:     w.Content,
		CallID:      w.CallID,
		Name:        w.Name,
		Args:        w.Args,
		IsError:     w.IsError,
		Details:     w.Details,
		Frame:       w.Frame,
		StopReason:  w.StopReason,
		EndReason:   w.EndReason,
		Usage:       w.Usage,
		Error:       w.Error,
		Undelivered: w.Undelivered,
	}, true
}
