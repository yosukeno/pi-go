package agent

import (
	"encoding/json"
	"time"

	"github.com/yosukeno/pi-go/llm"
)

// EventKind enumerates what the loop reports. Every interface (the -p one-shot
// printer, a REPL, later an HTTP/SSE handler) is just a consumer of these, so
// adding a front end never touches the loop.
type EventKind string

const (
	EventAgentStart EventKind = "agent_start"
	EventTurnStart  EventKind = "turn_start"
	// EventTextDelta and EventThinkingDelta carry streaming fragments.
	EventTextDelta     EventKind = "text_delta"
	EventThinkingDelta EventKind = "thinking_delta"
	// EventMessage carries a settled assistant message.
	EventMessage EventKind = "message"
	// EventToolArgs carries model-side argument fragments while a tool call is
	// still being generated — before review, before tool_start. It is not tool
	// execution output; that is EventToolPartial. The first event for a call
	// carries ToolName, fragment events carry Text, and ToolCallID ties them all
	// to the call the settled arguments will arrive with.
	EventToolArgs  EventKind = "tool_args"
	EventToolStart EventKind = "tool_start"
	// EventToolPartial carries output from a tool that is still running, for the
	// tools that can produce it. Text is a delta. Consumers may ignore it: the
	// settled output always arrives with EventToolEnd, so nothing depends on
	// having seen the fragments.
	EventToolPartial EventKind = "tool_partial"
	EventToolEnd     EventKind = "tool_end"
	// EventSteer reports a message the caller queued mid-run, at the moment it
	// enters the conversation. The loop does not announce the run's opening prompt
	// — the caller already has that — but a steering message is delivered at a
	// time only the loop knows. The loop itself uses the same event for the soft
	// turn cap's checkpoint notice (softcap.go): it too is a user-role message
	// landing mid-run at a time only the loop knows.
	EventSteer EventKind = "steer"
	// EventToolResults carries the aggregated user message of one tool batch —
	// every tool_result of one assistant message, in call order — exactly as it
	// was appended to the history. It exists for incremental persistence (see
	// TurnPersister) and is deliberately absent from the wire contract: a
	// browser already renders the same results as tool_end cards, and a second
	// copy would be noise.
	EventToolResults EventKind = "tool_results"
	EventAgentEnd    EventKind = "agent_end"
)

type Event struct {
	Kind EventKind

	// Text is set for the delta events, and for EventToolArgs it is one argument
	// fragment.
	Text string

	// Message is set for EventMessage.
	Message llm.Message

	// Tool fields are set for EventToolArgs / EventToolStart / EventToolEnd.
	ToolCallID string
	ToolName   string
	ToolArgs   string
	ToolOutput string
	// ToolDetails is the tool's structured payload (tools.EditDetails and
	// friends). It is for interfaces only and never enters the conversation, so
	// consumers may ignore it entirely.
	ToolDetails any
	// ToolFrame is one structured progress event from a tool that has structure to
	// report, carried on EventToolPartial beside Text. Only the subagent tool sets
	// it; see tools.Partial.Frame.
	ToolFrame json.RawMessage
	IsError   bool

	// StopReason and Err are set for EventAgentEnd.
	StopReason llm.StopReason
	Err        error
	// EndReason is why the *run* ended, set on EventAgentEnd and nowhere else.
	//
	// Not a replacement for StopReason and not derivable from it: that field is the
	// provider's reason for ending one reply, and it has no vocabulary for a turn
	// cap, a budget or a stagnation verdict. Both travel. See endreason.go.
	EndReason EndReason
	// Undelivered is set on EventAgentEnd for steering messages that were accepted
	// but never reached the model, which a run can only avoid by never accepting
	// one late. Reporting them lets an interface offer the text back instead of
	// losing something the user typed.
	Undelivered []string

	// Usage means different things depending on the event, and the difference
	// matters: on EventAgentEnd it is the run's running total, while on
	// EventMessage it is that one turn's own report.
	//
	// Only the per-turn number says anything about context occupancy. The total
	// grows quadratically with turn count, because every turn resends the whole
	// conversation, so a meter built on it would read far past the window.
	Usage llm.Usage

	// CallTiming is that one call's latency, set on EventMessage.
	CallTiming llm.Timing
	// Timing is the run's latency summary, set on EventAgentEnd. Zero Calls means
	// no call ever produced content, so there is nothing to report.
	Timing Timing

	// Turn is the 1-based turn index for EventTurnStart.
	Turn int

	// ContextEdit is set on EventTurnStart when this turn's prompt had old tool
	// results cleared out of it, and nil otherwise — most turns clear nothing, and a
	// field that is always present says nothing by being present.
	//
	// A field on turn_start rather than a tenth event kind. The nine names are a
	// contract two consumers already speak (SSE and the JSONL of -mode json), and
	// clearing is not an episode of its own: it is a property of the turn whose
	// prompt it shrank, in the same way OverheadMetrics is a property of a message.
	ContextEdit *ContextEdit

	// OverheadMetrics exposes fixed overhead ratio information for diagnostic
	// purposes. This is only set on EventMessage and EventAgentEnd, and reflects
	// the per-turn fixed cost (system prompt + tool schemas) relative to total input.
	// See RFC B1: Fixed Overhead Calculation for details.
	OverheadMetrics *OverheadMetrics
}

// Timing summarises how long the run's model calls took to start producing.
//
// It answers the question a spinner cannot: "was that wait normal?". A single
// slow turn is invisible in a total, and a total is what every other counter
// here reports, so this one is deliberately an average with the sample size
// attached.
type Timing struct {
	// Calls is how many model calls the averages are over. Zero means nothing has
	// been measured yet, and the durations should not be shown.
	Calls int `json:"calls"`
	// AvgTTFT is the mean time-to-first-token across those calls: the wait
	// between submitting a turn and the first thinking or text appearing.
	AvgTTFT time.Duration `json:"avg_ttft"`
	// AvgTTFB is the mean time to response headers, which is the network and
	// queueing share of AvgTTFT. AvgTTFT minus AvgTTFB is the model's own
	// startup: prefill plus, on a reasoning model, however long it takes to
	// begin thinking.
	AvgTTFB time.Duration `json:"avg_ttfb"`
	// MaxTTFT is the worst single wait, which is what a user remembers.
	MaxTTFT time.Duration `json:"max_ttft"`
	// TotalWait is every first-token wait added up. Against the run's wall clock
	// it says how much of the run was spent waiting to start rather than
	// streaming or running tools.
	TotalWait time.Duration `json:"total_wait"`
}

// timingAccum collects per-call measurements. Calls that produced no content at
// all are skipped rather than averaged in as zero, which is why the count lives
// here instead of being taken from the turn number.
type timingAccum struct {
	calls   int
	sumTTFT time.Duration
	sumTTFB time.Duration
	maxTTFT time.Duration
}

func (t *timingAccum) add(m llm.Timing) {
	if m.TTFT <= 0 {
		return
	}
	t.calls++
	t.sumTTFT += m.TTFT
	t.sumTTFB += m.TTFB
	if m.TTFT > t.maxTTFT {
		t.maxTTFT = m.TTFT
	}
}

func (t timingAccum) summary() Timing {
	if t.calls == 0 {
		return Timing{}
	}
	n := time.Duration(t.calls)
	return Timing{
		Calls:     t.calls,
		AvgTTFT:   t.sumTTFT / n,
		AvgTTFB:   t.sumTTFB / n,
		MaxTTFT:   t.maxTTFT,
		TotalWait: t.sumTTFT,
	}
}

// OverheadMetrics reports the fixed overhead ratio for a turn.
// This helps identify when optimization efforts should focus on schemas rather
// than prompts, as tool schemas are ~5.8x larger than system prompts and are
// resent on every turn.
type OverheadMetrics struct {
	// FixedCostTokens is the per-turn fixed cost (system prompt + tool schemas).
	FixedCostTokens int64 `json:"fixed_cost_tokens"`
	// TotalInputTokens is the total input tokens for this turn.
	TotalInputTokens int64 `json:"total_input_tokens"`
	// OverheadRatio is the ratio of fixed cost to total input (0.0-1.0).
	OverheadRatio float64 `json:"overhead_ratio"`
}
