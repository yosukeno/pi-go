package agent

import (
	"context"
	"testing"

	"github.com/wangy/pi-go/llm"
	"github.com/wangy/pi-go/tools"
)

// LastInput and Usage().Input answer different questions and the difference is the
// reason both exist: one is how full the context window is, the other is what the
// session has cost. The total grows with every turn because each turn resends the
// whole conversation, so using it as a gauge reads far past the window — the
// mistake this accessor exists to make unnecessary.
func TestLastInputIsTheLatestTurnNotTheTotal(t *testing.T) {
	tool := &fakeTool{name: "probe"}
	c := &fakeClient{responses: []llm.Response{
		withUsage(toolCalls("probe"), llm.Usage{Input: 1000, Output: 10}),
		withUsage(toolCalls("probe"), llm.Usage{Input: 2500, Output: 20}),
		withUsage(final("done"), llm.Usage{Input: 4000, Output: 30}),
	}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool)})
	drain(a.Run(context.Background(), "go"))

	if got := a.LastInput(); got != 4000 {
		t.Errorf("LastInput = %d, want the final turn's 4000", got)
	}
	if got := a.Usage().Input; got != 7500 {
		t.Errorf("Usage().Input = %d, want the cumulative 7500", got)
	}
}

// Zero before anything has been measured, rather than a guess. A composition
// recorded for a run whose first call failed has no provider count to calibrate
// against, and reporting one would be worse than reporting none.
func TestLastInputIsZeroBeforeAnyCall(t *testing.T) {
	a := New(Config{Client: &fakeClient{}, Registry: tools.NewRegistry()})
	if got := a.LastInput(); got != 0 {
		t.Errorf("LastInput = %d before any run, want 0", got)
	}
}

// It carries across runs within one session, because the window does not empty
// between them: the second run's prompt starts where the first one ended.
func TestLastInputPersistsAcrossRuns(t *testing.T) {
	c := &fakeClient{responses: []llm.Response{
		withUsage(final("one"), llm.Usage{Input: 900, Output: 5}),
		withUsage(final("two"), llm.Usage{Input: 1800, Output: 5}),
	}}
	a := New(Config{Client: c, Registry: tools.NewRegistry()})
	drain(a.Run(context.Background(), "first"))
	if got := a.LastInput(); got != 900 {
		t.Fatalf("after run 1: LastInput = %d, want 900", got)
	}
	drain(a.Run(context.Background(), "second"))
	if got := a.LastInput(); got != 1800 {
		t.Errorf("after run 2: LastInput = %d, want 1800", got)
	}
}

func withUsage(r llm.Response, u llm.Usage) llm.Response {
	r.Usage = u
	return r
}

func final(text string) llm.Response {
	return llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: text}}},
		StopReason: llm.StopEndTurn,
	}
}
