package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/wangy/pi-go/llm"
	"github.com/wangy/pi-go/tools"
)

// endOf returns the terminal event, failing if the run did not produce one.
//
// Every test below goes through this rather than indexing the slice, because the
// property it asserts on the way — the last event is agent_end — is the one that
// makes an EndReason reachable at all. A run that dies without emitting agent_end
// leaves a driver waiting, and no field on the event can fix that.
func endOf(t *testing.T, events []Event) Event {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("run produced no events")
	}
	end := events[len(events)-1]
	if end.Kind != EventAgentEnd {
		t.Fatalf("last event is %v, want %v", end.Kind, EventAgentEnd)
	}
	return end
}

// --- the table ---

func TestEveryEndReasonHasADisposition(t *testing.T) {
	known := map[Disposition]bool{
		DispositionDone: true, DispositionContinue: true,
		DispositionIntervene: true, DispositionHalt: true,
	}

	for _, r := range AllEndReasons {
		d, ok := dispositions[r]
		if !ok {
			t.Errorf("EndReason %q has no disposition; add it to the dispositions table "+
				"and decide what a driver should do about it", r)
			continue
		}
		if !known[d] {
			t.Errorf("EndReason %q maps to %q, which is not a Disposition", r, d)
		}
	}

	// The other direction. Without it a reason could be deleted from AllEndReasons
	// and keep a stale row in the table, and the loop above would still pass.
	if len(dispositions) != len(AllEndReasons) {
		t.Errorf("dispositions has %d rows for %d reasons; the two are out of step",
			len(dispositions), len(AllEndReasons))
	}
	for r := range dispositions {
		found := false
		for _, want := range AllEndReasons {
			if r == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("dispositions has a row for %q, which is not in AllEndReasons", r)
		}
	}
}

// The one classification the type exists for. Stagnation and overflow are the two
// reasons that leave work unfinished and yet must not be retried unchanged, and a
// driver that lumps every non-completed reason into "try again" spins on both: the
// stagnation check re-fires at the same turn on the same history, and clearing has
// already run and freed nothing.
func TestStagnationAndOverflowAreNotContinue(t *testing.T) {
	for _, r := range []EndReason{EndStagnation, EndContextOverflow} {
		if got := r.Disposition(); got != DispositionIntervene {
			t.Errorf("%q disposition = %q, want %q — retrying it unchanged reproduces it",
				r, got, DispositionIntervene)
		}
	}
}

// An unclassified reason halts. Halting an unattended driver costs one stalled job;
// continuing on something nobody classified costs an unbounded number of runs, so
// the default is the cheap mistake rather than the expensive one.
func TestUnregisteredEndReasonHalts(t *testing.T) {
	if got := EndReason("something_new").Disposition(); got != DispositionHalt {
		t.Errorf("unregistered disposition = %q, want %q", got, DispositionHalt)
	}
	if got := EndReason("").Disposition(); got != DispositionHalt {
		t.Errorf("zero disposition = %q, want %q", got, DispositionHalt)
	}
}

// --- the loop sets it on every exit ---

func TestRunEndsCompletedWhenTheModelStops(t *testing.T) {
	c := &fakeClient{}
	a := New(Config{Client: c, Registry: tools.NewRegistry()})

	end := endOf(t, drain(a.Run(context.Background(), "hello")))
	if end.EndReason != EndCompleted {
		t.Errorf("EndReason = %q, want %q", end.EndReason, EndCompleted)
	}
	if end.Err != nil {
		t.Errorf("a completed run reported an error: %v", end.Err)
	}
	if got := end.EndReason.Disposition(); got != DispositionDone {
		t.Errorf("disposition = %q, want %q", got, DispositionDone)
	}
}

func TestRunEndsWithTurnLimit(t *testing.T) {
	// The model keeps asking for tools, so nothing but the cap can stop it.
	echo := &fakeTool{name: "echo"}
	c := &fakeClient{responses: []llm.Response{
		toolCalls("echo"), toolCalls("echo"), toolCalls("echo"), toolCalls("echo"),
	}}
	a := New(Config{
		Client: c, Registry: tools.NewRegistry(echo), MaxTurns: 2,
		// Off, or identical results would trip stagnation first and this test would
		// pass while asserting the wrong reason.
		StagnationThreshold: 0,
	})

	end := endOf(t, drain(a.Run(context.Background(), "go")))
	if end.EndReason != EndTurnLimit {
		t.Fatalf("EndReason = %q, want %q (err: %v)", end.EndReason, EndTurnLimit, end.Err)
	}
	// Err stays, and stays readable: it is what a person sees. The point of the
	// field is that a script no longer has to parse this sentence.
	if end.Err == nil {
		t.Error("turn limit reported no error; a person needs the sentence too")
	}
	if got := end.EndReason.Disposition(); got != DispositionContinue {
		t.Errorf("disposition = %q, want %q", got, DispositionContinue)
	}
}

func TestRunEndsWithTokenBudget(t *testing.T) {
	echo := &fakeTool{name: "echo"}
	c := &fakeClient{responses: []llm.Response{
		{
			Message:    toolCalls("echo").Message,
			StopReason: llm.StopToolUse,
			Usage:      llm.Usage{Input: 500, Output: 500},
		},
		toolCalls("echo"),
	}}
	a := New(Config{
		Client: c, Registry: tools.NewRegistry(echo),
		TokenBudget: 100, StagnationThreshold: 0,
	})

	end := endOf(t, drain(a.Run(context.Background(), "go")))
	if end.EndReason != EndTokenBudget {
		t.Fatalf("EndReason = %q, want %q (err: %v)", end.EndReason, EndTokenBudget, end.Err)
	}
}

func TestRunEndsWithCostBudget(t *testing.T) {
	echo := &fakeTool{name: "echo"}
	c := &fakeClient{responses: []llm.Response{
		{
			Message:    toolCalls("echo").Message,
			StopReason: llm.StopToolUse,
			Usage:      llm.Usage{Input: 2_000_000, Output: 100_000},
		},
		toolCalls("echo"),
	}}
	a := New(Config{
		Client: c, Registry: tools.NewRegistry(echo),
		CostBudget: 1.0, Price: &llm.Price{Input: 10, Output: 30},
		StagnationThreshold: 0,
	})

	end := endOf(t, drain(a.Run(context.Background(), "go")))
	if end.EndReason != EndCostBudget {
		t.Fatalf("EndReason = %q, want %q (err: %v)", end.EndReason, EndCostBudget, end.Err)
	}
	if got := end.EndReason.Disposition(); got != DispositionContinue {
		t.Errorf("disposition = %q, want %q", got, DispositionContinue)
	}
}

// A cost budget with no price must not crash the run, which is what dereferencing a
// nil *Price would do.
//
// That is all this pins, and the modest claim is the accurate one: substituting a
// zero price for nil would not stop the run either, because a zero rate never
// exceeds a positive budget. The guarantee that an unpriced run cannot proceed
// uncapped is checkCostBudget's, not this function's — a mutation that made this
// path nil-safe-but-wrong passed this test, which is how that got established.
func TestACostBudgetWithoutAPriceDoesNotStopTheRun(t *testing.T) {
	c := &fakeClient{}
	a := New(Config{
		Client: c, Registry: tools.NewRegistry(),
		CostBudget: 0.000001, // Price is nil.
	})

	end := endOf(t, drain(a.Run(context.Background(), "go")))
	if end.EndReason != EndCompleted {
		t.Errorf("EndReason = %q, want %q — an unpriced model has no cost to compare",
			end.EndReason, EndCompleted)
	}
}

// The rate belongs to the model, so a switch has to move it. Left alone, the new
// model's tokens would be charged at the old model's price.
func TestSetPriceMovesTheRateWithTheModel(t *testing.T) {
	a := New(Config{
		Client: &fakeClient{}, Registry: tools.NewRegistry(),
		CostBudget: 1.0, Price: &llm.Price{Input: 10},
	})
	if a.Price() == nil || a.Price().Input != 10 {
		t.Fatalf("Price = %+v, want the configured rate", a.Price())
	}
	if a.CostBudget() != 1.0 {
		t.Errorf("CostBudget = %g, want 1.0", a.CostBudget())
	}

	a.SetPrice(&llm.Price{Input: 99})
	if a.Price().Input != 99 {
		t.Errorf("Price.Input = %g after SetPrice, want 99", a.Price().Input)
	}

	// nil is legitimate and disables the comparison rather than meaning free.
	a.SetPrice(nil)
	if a.Price() != nil {
		t.Error("SetPrice(nil) did not clear the rate")
	}
}

func TestRunEndsWithTimeBudget(t *testing.T) {
	// A budget already spent by the time the second turn's check runs.
	echo := &fakeTool{name: "echo", run: func(context.Context, json.RawMessage) (tools.Result, error) {
		time.Sleep(20 * time.Millisecond)
		return tools.Result{Text: "ok"}, nil
	}}
	c := &fakeClient{responses: []llm.Response{toolCalls("echo"), toolCalls("echo")}}
	a := New(Config{
		Client: c, Registry: tools.NewRegistry(echo),
		TimeBudget: time.Millisecond, StagnationThreshold: 0,
	})

	end := endOf(t, drain(a.Run(context.Background(), "go")))
	if end.EndReason != EndTimeBudget {
		t.Fatalf("EndReason = %q, want %q (err: %v)", end.EndReason, EndTimeBudget, end.Err)
	}
}

func TestRunEndsWithStagnation(t *testing.T) {
	// fakeTool's default output is fixed and toolCalls reuses the same call id, so
	// each batch hashes identically — which is exactly the shape the check looks for.
	echo := &fakeTool{name: "echo"}
	c := &fakeClient{responses: []llm.Response{
		toolCalls("echo"), toolCalls("echo"), toolCalls("echo"), toolCalls("echo"),
	}}
	a := New(Config{
		Client: c, Registry: tools.NewRegistry(echo),
		StagnationThreshold: 3, MaxTurns: 20,
	})

	end := endOf(t, drain(a.Run(context.Background(), "go")))
	if end.EndReason != EndStagnation {
		t.Fatalf("EndReason = %q, want %q (err: %v)", end.EndReason, EndStagnation, end.Err)
	}
	if got := end.EndReason.Disposition(); got != DispositionIntervene {
		t.Errorf("disposition = %q, want %q", got, DispositionIntervene)
	}
}

// failingClient fails every call with a fixed error.
type failingClient struct{ err error }

func (c *failingClient) Model() string { return "failing" }

func (c *failingClient) Stream(
	context.Context, string, []llm.Message, []llm.ToolSchema, func(llm.Delta),
) (llm.Response, error) {
	return llm.Response{}, c.err
}

func TestRunEndsWithTransportError(t *testing.T) {
	c := &failingClient{err: errors.New("dial tcp: connection refused")}
	a := New(Config{Client: c, Registry: tools.NewRegistry()})

	end := endOf(t, drain(a.Run(context.Background(), "go")))
	if end.EndReason != EndTransportError {
		t.Fatalf("EndReason = %q, want %q", end.EndReason, EndTransportError)
	}
	if got := end.EndReason.Disposition(); got != DispositionHalt {
		t.Errorf("disposition = %q, want %q — a dead provider is not a reason to start another run",
			got, DispositionHalt)
	}
	// StopReason keeps its old value. Both fields travel, and a consumer that was
	// reading stop_reason before this change must see no difference.
	if end.StopReason != llm.StopError {
		t.Errorf("StopReason = %q, want %q", end.StopReason, llm.StopError)
	}
}

// The distinction this pair of reasons exists for: two 400s from the same provider,
// fixed by opposite actions. Told apart here without matching any prose.
func TestRunEndsWithContextOverflowWhenClearingCannotHelp(t *testing.T) {
	c := &failingClient{err: &llm.APIError{
		Status: 400, StatusText: "400 Bad Request", Type: "invalid_request_error",
		Message: "Invalid request: Your request exceeded model token limit: 262144",
	}}
	// No tool results in the history, so forceClear has nothing to free and returns
	// false — which is the state that makes an overflow terminal.
	a := New(Config{Client: c, Registry: tools.NewRegistry()})

	end := endOf(t, drain(a.Run(context.Background(), "go")))
	if end.EndReason != EndContextOverflow {
		t.Fatalf("EndReason = %q, want %q (err: %v)", end.EndReason, EndContextOverflow, end.Err)
	}
	if got := end.EndReason.Disposition(); got != DispositionIntervene {
		t.Errorf("disposition = %q, want %q", got, DispositionIntervene)
	}
}

// Cancellation reaches the loop by two different routes, and both have to land on
// the same reason. Getting this wrong is visible: a Ctrl-C reported as
// transport_error tells the operator the provider broke.
//
// Route one is the client's. A cancelled context is not an error on this interface —
// llm/openai.go returns Response{StopReason: StopAborted} with a nil error at every
// point it can be interrupted — so the loop learns about it from the stop reason.
func TestRunEndsWithAbortedWhenTheClientReportsIt(t *testing.T) {
	c := &fakeClient{responses: []llm.Response{{
		Message:    llm.Message{Role: llm.RoleAssistant},
		StopReason: llm.StopAborted,
	}}}
	a := New(Config{Client: c, Registry: tools.NewRegistry()})

	end := endOf(t, drain(a.Run(context.Background(), "go")))
	if end.EndReason != EndAborted {
		t.Fatalf("EndReason = %q, want %q (err: %v)", end.EndReason, EndAborted, end.Err)
	}
	if got := end.EndReason.Disposition(); got != DispositionHalt {
		t.Errorf("disposition = %q, want %q — someone stopped this on purpose",
			got, DispositionHalt)
	}
}

// Route two is the loop's own check after a tool batch, for a cancellation that
// arrives while tools are running rather than while the model is.
func TestRunEndsWithAbortedWhenCancelledDuringAToolBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	echo := &fakeTool{name: "echo", run: func(context.Context, json.RawMessage) (tools.Result, error) {
		cancel()
		return tools.Result{Text: "ok"}, nil
	}}
	c := &fakeClient{responses: []llm.Response{toolCalls("echo"), toolCalls("echo")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(echo)})

	end := endOf(t, drain(a.Run(ctx, "go")))
	if end.EndReason != EndAborted {
		t.Fatalf("EndReason = %q, want %q (err: %v)", end.EndReason, EndAborted, end.Err)
	}
}

// max_tokens leaves the loop by the same path as a completed run, which is why it
// needs its own reason: the reply is cut off mid-sentence and calling that
// "completed" would tell a driver the work is done.
func TestRunEndsWithMaxTokens(t *testing.T) {
	c := &fakeClient{responses: []llm.Response{{
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockText, Text: "I was saying that the answer is"},
		}},
		StopReason: llm.StopMaxTokens,
	}}}
	a := New(Config{Client: c, Registry: tools.NewRegistry()})

	end := endOf(t, drain(a.Run(context.Background(), "go")))
	if end.EndReason != EndMaxTokens {
		t.Fatalf("EndReason = %q, want %q", end.EndReason, EndMaxTokens)
	}
	if end.EndReason == EndCompleted {
		t.Error("a truncated reply reported itself as completed")
	}
}

// Nothing but agent_end carries the field. A reason on a turn_start or a tool_end
// would be a second place for it to be wrong.
func TestOnlyTheTerminalEventCarriesAnEndReason(t *testing.T) {
	echo := &fakeTool{name: "echo"}
	c := &fakeClient{responses: []llm.Response{toolCalls("echo")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(echo)})

	events := drain(a.Run(context.Background(), "go"))
	for _, e := range events {
		if e.Kind == EventAgentEnd {
			continue
		}
		if e.EndReason != "" {
			t.Errorf("%v carries EndReason %q; only agent_end should", e.Kind, e.EndReason)
		}
	}
	if got := endOf(t, events).EndReason; got == "" {
		t.Error("agent_end carries no EndReason")
	}
}

// Belt and braces on the whole point: no exit path may leave the field empty. A
// consumer branching on it would read "" as an unknown reason and halt, turning a
// forgotten assignment into a stalled driver rather than a test failure.
func TestNoExitPathLeavesTheEndReasonEmpty(t *testing.T) {
	echo := &fakeTool{name: "echo"}
	loops := []llm.Response{toolCalls("echo"), toolCalls("echo"), toolCalls("echo"), toolCalls("echo")}

	cases := []struct {
		name string
		cfg  Config
		ctx  func() (context.Context, context.CancelFunc)
	}{
		{name: "completed", cfg: Config{Client: &fakeClient{}}},
		{name: "turn limit", cfg: Config{
			Client: &fakeClient{responses: loops}, MaxTurns: 2, StagnationThreshold: 0,
		}},
		{name: "stagnation", cfg: Config{
			Client: &fakeClient{responses: loops}, StagnationThreshold: 3,
		}},
		{name: "token budget", cfg: Config{
			Client:      &fakeClient{responses: []llm.Response{{Message: toolCalls("echo").Message, StopReason: llm.StopToolUse, Usage: llm.Usage{Input: 999}}, toolCalls("echo")}},
			TokenBudget: 1, StagnationThreshold: 0,
		}},
		{name: "cost budget", cfg: Config{
			Client:     &fakeClient{responses: []llm.Response{{Message: toolCalls("echo").Message, StopReason: llm.StopToolUse, Usage: llm.Usage{Input: 1_000_000}}, toolCalls("echo")}},
			CostBudget: 0.001, Price: &llm.Price{Input: 10}, StagnationThreshold: 0,
		}},
		{name: "time budget", cfg: Config{
			Client: &fakeClient{responses: loops}, TimeBudget: time.Nanosecond, StagnationThreshold: 0,
		}},
		{name: "transport error", cfg: Config{
			Client: &failingClient{err: errors.New("boom")},
		}},
		{name: "context overflow", cfg: Config{
			Client: &failingClient{err: &llm.APIError{
				Status: 400, Message: "Your request exceeded model token limit: 100",
			}},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			cfg.Registry = tools.NewRegistry(echo)
			a := New(cfg)
			end := endOf(t, drain(a.Run(context.Background(), "go")))
			if end.EndReason == "" {
				t.Fatalf("run ended with an empty EndReason (err: %v)", end.Err)
			}
			if _, ok := dispositions[end.EndReason]; !ok {
				t.Errorf("EndReason %q is not in the dispositions table", end.EndReason)
			}
		})
	}

	// Cancellation gets its own case because it needs the context, not the config,
	// and it has to cancel from inside a tool: the bottom-of-loop check is only
	// reached on a turn that ran one.
	t.Run("aborted", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancelling := &fakeTool{name: "echo", run: func(context.Context, json.RawMessage) (tools.Result, error) {
			cancel()
			return tools.Result{Text: "ok"}, nil
		}}
		a := New(Config{
			Client:   &fakeClient{responses: loops},
			Registry: tools.NewRegistry(cancelling),
		})
		end := endOf(t, drain(a.Run(ctx, "go")))
		if end.EndReason == "" {
			t.Fatalf("cancelled run ended with an empty EndReason (err: %v)", end.Err)
		}
		if end.EndReason != EndAborted {
			t.Errorf("EndReason = %q, want %q", end.EndReason, EndAborted)
		}
	})
}
