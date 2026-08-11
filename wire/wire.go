// Package wire is the event contract shared by every programmatic consumer of
// the loop: the `-mode json` emitter in main, and the HTTP/SSE server in web.
//
// It exists because those two consumers were about to grow separate translations
// of the same events. The loop already emits structured events (agent.Event), but
// that type is internal: it has no JSON tags, carries an `error` (which marshals
// to `{}`) and an `any` payload. Something has to name the events and decide
// which fields travel. Doing that once, here, is what keeps the CLI's JSON output
// and the browser's SSE stream from drifting into two dialects.
//
// The one rule for this package: it must not import web. The names below are the
// loop's vocabulary, not the browser's, and web is free to add its own events on
// top (snapshots, approval gates, policy changes) without those leaking into a
// contract that scripts and subagents consume.
package wire

import (
	"encoding/json"
	"time"

	"github.com/wangy/pi-go/agent"
	"github.com/wangy/pi-go/llm"
)

// Type is the event name as it appears on the wire.
//
// Names are chosen for the consumer rather than the loop's internal vocabulary:
// `token` instead of text_delta, `run_end` instead of agent_end. They are already
// being consumed by the browser front end, so they are a compatibility surface:
// add, do not rename.
type Type string

const (
	// Session is the JSON mode header, written once before any event. It is not
	// loop-derived — web publishes its own richer run_start instead — but it
	// belongs to the same contract, because a consumer reading a JSONL stream
	// needs to know what session it is reading.
	Session Type = "session"

	TurnStart Type = "turn_start"
	Thinking  Type = "thinking"
	Token     Type = "token"
	Message   Type = "message"
	ToolStart Type = "tool_start"
	// ToolArgs is one fragment of a call's arguments while the model is still
	// generating them — before review, before ToolStart. Name is set on the
	// first, name-bearing event only; Text on fragment events only.
	ToolArgs Type = "tool_args"
	// ToolPartial is output from a call that is still running. Consumers may
	// ignore it: the settled output always arrives with ToolEnd.
	ToolPartial Type = "tool_partial"
	ToolEnd     Type = "tool_end"
	// UserMessage carries a steering message at the moment it enters the
	// conversation. The name is the consumer's word, not the loop's
	// (agent.EventSteer): to a client it is simply another user turn, drawn the
	// way every other one is.
	UserMessage Type = "user_message"
	RunEnd      Type = "run_end"
)

// Header is the first line of a JSON mode stream.
type Header struct {
	Type Type  `json:"type"`
	TS   int64 `json:"ts"`
	// Session is the path to the transcript this run appends to. It is the handle
	// a caller needs to resume, analyse, or (for a subagent) hand back to a
	// parent, which is why it travels rather than just the session id.
	Session string   `json:"session"`
	Cwd     string   `json:"cwd,omitempty"`
	Model   string   `json:"model,omitempty"`
	Skills  []string `json:"skills,omitempty"`
}

// Event is one loop event, flattened.
//
// A flat union rather than a nested payload so that every field has a name and a
// type in exactly one place. The cost is a wide struct; the benefit is that no
// consumer has to guess a shape from the event name.
type Event struct {
	Type Type  `json:"type"`
	TS   int64 `json:"ts"`

	// Turn is the 1-based loop iteration, set on TurnStart.
	Turn int `json:"turn,omitempty"`
	// ContextEdit is set on TurnStart when this turn's prompt had old tool results
	// cleared from it, and absent otherwise. It answers the one question a shrinking
	// context gauge raises: why.
	ContextEdit *agent.ContextEdit `json:"context_edit,omitempty"`

	// Role and Content carry a settled assistant message. Text carries a
	// streaming fragment (Thinking, Token) or a steering message.
	Role    string      `json:"role,omitempty"`
	Text    string      `json:"text,omitempty"`
	Content []llm.Block `json:"content,omitempty"`

	// CallID ties ToolArgs, ToolStart, ToolPartial and ToolEnd together. It is what
	// makes out-of-order completion in a parallel batch need no special handling: a
	// consumer files everything by id rather than by arrival order.
	CallID  string          `json:"call_id,omitempty"`
	Name    string          `json:"name,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
	// Details is a tool's structured payload (tools.EditDetails and friends). It
	// never enters the conversation, which is why a diff can be full length here
	// while the model only ever saw the truncated text.
	Details any `json:"details,omitempty"`

	// Frame is one structured progress event from a tool that has progress with
	// structure — today only a subagent, whose child produces this same event
	// contract one level down. Nesting the stream inside itself is the point: a
	// consumer that can already render a run can render a delegated one with the
	// same code.
	Frame json.RawMessage `json:"frame,omitempty"`

	StopReason string `json:"stop_reason,omitempty"`
	// EndReason is why the run ended, set on RunEnd. Both it and StopReason travel
	// because they answer different questions: stop_reason is the provider's word for
	// why one reply ended, end_reason is the harness's word for why the run did, and
	// only the second one can say "turn_limit". A driver branches on this field;
	// before it existed the only machine-readable signal for a turn cap was the
	// wording of Error, which made every reword a breaking change. See
	// agent/endreason.go for the values and what each implies.
	EndReason string `json:"end_reason,omitempty"`
	// Usage means different things by event, and the difference matters: on
	// Message it is that turn's own report, on RunEnd it is the run total. Only
	// the per-turn number says anything about context occupancy.
	Usage *llm.Usage `json:"usage,omitempty"`
	Error string     `json:"error,omitempty"`
	// Undelivered lists steering messages the run accepted but never passed to
	// the model. A client should offer the text back rather than let it vanish.
	Undelivered []string `json:"undelivered,omitempty"`
}

// FromAgent translates a loop event. The second return is false for events a
// consumer is expected to render itself with more context than the loop has:
// agent_start becomes web's run_start (which also carries the run id and model)
// and JSON mode's Header.
//
// This is the single translation in the tree. web widens the result into its own
// frame rather than repeating the mapping, so a new event kind is wired up in one
// place instead of two.
func FromAgent(e agent.Event) (Event, bool) {
	out := Event{TS: NowMS()}
	switch e.Kind {
	case agent.EventTurnStart:
		out.Type, out.Turn, out.ContextEdit = TurnStart, e.Turn, e.ContextEdit

	case agent.EventThinkingDelta:
		out.Type, out.Text = Thinking, e.Text

	case agent.EventTextDelta:
		out.Type, out.Text = Token, e.Text

	case agent.EventMessage:
		usage := e.Usage
		out.Type, out.Role, out.Content, out.Usage =
			Message, string(e.Message.Role), e.Message.Content, &usage

	case agent.EventToolArgs:
		out.Type, out.CallID, out.Name, out.Text = ToolArgs, e.ToolCallID, e.ToolName, e.Text

	case agent.EventToolStart:
		out.Type, out.CallID, out.Name, out.Args =
			ToolStart, e.ToolCallID, e.ToolName, RawJSON(e.ToolArgs)

	case agent.EventToolPartial:
		out.Type, out.CallID, out.Name, out.Text =
			ToolPartial, e.ToolCallID, e.ToolName, e.Text
		// Already JSON by construction — the producer built it, not a model — so it
		// passes through rather than going via RawJSON, which exists to survive
		// malformed model output.
		out.Frame = e.ToolFrame

	case agent.EventToolEnd:
		out.Type, out.CallID, out.Name = ToolEnd, e.ToolCallID, e.ToolName
		out.Text, out.IsError, out.Details = e.ToolOutput, e.IsError, e.ToolDetails

	case agent.EventSteer:
		out.Type, out.Text = UserMessage, e.Text

	case agent.EventAgentEnd:
		usage := e.Usage
		out.Type, out.StopReason, out.Usage = RunEnd, string(e.StopReason), &usage
		out.EndReason = string(e.EndReason)
		out.Undelivered = e.Undelivered
		if e.Err != nil {
			// The error has to be flattened here: an `error` marshals to `{}`,
			// so a consumer of the raw struct would see a run fail with no reason.
			out.Error = e.Err.Error()
		}

	default:
		return Event{}, false
	}
	return out, true
}

// RawJSON passes a tool's arguments through as JSON when they are valid, and
// falls back to a JSON string otherwise.
//
// The loop hands arguments around as text because it never parses them, and a
// model can emit something malformed. Embedding that verbatim would produce a
// frame the consumer cannot parse — one bad tool call would break the whole
// stream rather than one event.
func RawJSON(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	quoted, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	return quoted
}

// NowMS is the timestamp every frame carries. Epoch milliseconds rather than a
// formatted string: a consumer that wants to compute elapsed time should not have
// to parse anything.
func NowMS() int64 { return time.Now().UnixMilli() }
