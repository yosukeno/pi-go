package agent

import (
	"context"
	"testing"
	"time"

	"github.com/wangy/pi-go/llm"
	"github.com/wangy/pi-go/tools"
)

// textTurnWithTiming is a final answer that reports how long it took to start.
func textTurnWithTiming(ttfb, ttft time.Duration) llm.Response {
	return llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockText, Text: "done"},
		}},
		StopReason: llm.StopEndTurn,
		Timing:     llm.Timing{TTFB: ttfb, TTFT: ttft, Total: ttft * 2},
	}
}

func finalEvent(t *testing.T, events []Event) Event {
	t.Helper()
	last := events[len(events)-1]
	if last.Kind != EventAgentEnd {
		t.Fatalf("last event = %s, want agent_end", last.Kind)
	}
	return last
}

func TestAgentEndCarriesTheAverageFirstTokenLatency(t *testing.T) {
	// Two calls: a tool-calling turn and the answer. The average has to be over
	// model calls, which is not the same as the number of turns once a run ends
	// on a budget or a turn limit.
	toolTurn := toolCalls("noop")
	toolTurn.Timing = llm.Timing{TTFB: 100 * time.Millisecond, TTFT: 1 * time.Second}
	c := &fakeClient{responses: []llm.Response{
		toolTurn,
		textTurnWithTiming(200*time.Millisecond, 3*time.Second),
	}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(&fakeTool{name: "noop"})})

	end := finalEvent(t, drain(a.Run(context.Background(), "go")))

	if end.Timing.Calls != 2 {
		t.Fatalf("Calls = %d, want 2", end.Timing.Calls)
	}
	if want := 2 * time.Second; end.Timing.AvgTTFT != want {
		t.Errorf("AvgTTFT = %v, want %v", end.Timing.AvgTTFT, want)
	}
	if want := 150 * time.Millisecond; end.Timing.AvgTTFB != want {
		t.Errorf("AvgTTFB = %v, want %v", end.Timing.AvgTTFB, want)
	}
	// The worst wait is the one the user remembers, so it must survive the
	// averaging.
	if want := 3 * time.Second; end.Timing.MaxTTFT != want {
		t.Errorf("MaxTTFT = %v, want %v", end.Timing.MaxTTFT, want)
	}
	if want := 4 * time.Second; end.Timing.TotalWait != want {
		t.Errorf("TotalWait = %v, want %v", end.Timing.TotalWait, want)
	}
}

// TestCallsWithNoMeasurementAreNotAveragedInAsZero is why timingAccum counts
// calls itself. A provider that sends no content, or an aborted turn, would
// otherwise halve the reported latency.
func TestCallsWithNoMeasurementAreNotAveragedInAsZero(t *testing.T) {
	unmeasured := toolCalls("noop") // Timing left zero
	c := &fakeClient{responses: []llm.Response{
		unmeasured,
		textTurnWithTiming(50*time.Millisecond, 2*time.Second),
	}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(&fakeTool{name: "noop"})})

	end := finalEvent(t, drain(a.Run(context.Background(), "go")))

	if end.Timing.Calls != 1 {
		t.Fatalf("Calls = %d, want 1: the unmeasured call must be skipped", end.Timing.Calls)
	}
	if want := 2 * time.Second; end.Timing.AvgTTFT != want {
		t.Errorf("AvgTTFT = %v, want %v", end.Timing.AvgTTFT, want)
	}
}

func TestPerCallTimingRidesOnTheMessageEvent(t *testing.T) {
	c := &fakeClient{responses: []llm.Response{
		textTurnWithTiming(80*time.Millisecond, 900*time.Millisecond),
	}}
	a := New(Config{Client: c, Registry: tools.NewRegistry()})

	var seen []llm.Timing
	for _, e := range drain(a.Run(context.Background(), "go")) {
		if e.Kind == EventMessage {
			seen = append(seen, e.CallTiming)
		}
	}
	if len(seen) != 1 {
		t.Fatalf("message events = %d, want 1", len(seen))
	}
	if want := 900 * time.Millisecond; seen[0].TTFT != want {
		t.Errorf("TTFT = %v, want %v (the call's own number, not an average)", seen[0].TTFT, want)
	}
}

// TestTimingSummaryIsEmptyBeforeAnyCall keeps the renderer from printing
// "ttft n/a" on a run that failed before it reached the model.
func TestTimingSummaryIsEmptyBeforeAnyCall(t *testing.T) {
	a := New(Config{Client: &fakeClient{}, Registry: tools.NewRegistry()})
	if got := a.Timing(); got.Calls != 0 || got.AvgTTFT != 0 {
		t.Errorf("Timing() = %+v, want the zero value", got)
	}
}

// TestTimingAccumulatesAcrossRuns matches Usage: in a REPL the average should
// cover the session, because "is this model slow today" is a session question.
func TestTimingAccumulatesAcrossRuns(t *testing.T) {
	c := &fakeClient{responses: []llm.Response{
		textTurnWithTiming(10*time.Millisecond, 1*time.Second),
		textTurnWithTiming(10*time.Millisecond, 3*time.Second),
	}}
	a := New(Config{Client: c, Registry: tools.NewRegistry()})

	drain(a.Run(context.Background(), "first"))
	drain(a.Run(context.Background(), "second"))

	got := a.Timing()
	if got.Calls != 2 {
		t.Fatalf("Calls = %d, want 2 across both runs", got.Calls)
	}
	if want := 2 * time.Second; got.AvgTTFT != want {
		t.Errorf("AvgTTFT = %v, want %v", got.AvgTTFT, want)
	}
}
