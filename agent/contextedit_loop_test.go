package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/yosukeno/pi-go/llm"
	"github.com/yosukeno/pi-go/tools"
)

// The whole point, stated as the failure it removes.
//
// Before this, a session that kept reading large files died: the prompt crossed the
// window, the provider answered 400, and 400 is not in llm/retry.go's retryable set,
// so the loop ended the run. Worse, the history only ever grows, so the next prompt
// in the same session was larger still — the session was permanently unusable and
// the only way out was to start a new one.
//
// The fake provider here reproduces that cliff exactly: over the window it returns
// an error instead of a response. With clearing on, the run has to finish.
func TestContextEditKeepsAnOverflowingRunAlive(t *testing.T) {
	const window = 60_000
	// Each read returns a maximal tool output, which is what makes this the ordinary
	// shape of the failure rather than a contrived one.
	huge := strings.Repeat("source line\n", 4200) // ~50KB, tools.MaxBytes
	fat := &fakeTool{name: "read", run: func(context.Context, json.RawMessage) (tools.Result, error) {
		return tools.Result{Text: huge}, nil
	}}

	var overflows int
	c := &fakeClient{}
	c.onStream = func(int) {}
	client := &windowedClient{fake: c, window: window, onOverflow: func() { overflows++ }}

	a := New(Config{
		Client:   client,
		Registry: tools.NewRegistry(fat),
		MaxTurns: 30,
		ContextEdit: ContextEditConfig{
			Trigger: 30_000, Keep: 2, ClearAtLeast: 1_000,
		},
	})

	events := drain(a.Run(context.Background(), "read the same file over and over"))
	end := events[len(events)-1]
	if end.Kind != EventAgentEnd {
		t.Fatalf("last event is %v, want agent_end", end.Kind)
	}
	if end.Err != nil {
		t.Fatalf("run failed: %v", end.Err)
	}
	if overflows != 0 {
		t.Errorf("the provider rejected %d prompt(s) as too long; clearing should have "+
			"kept every one under the window", overflows)
	}
	// And it has to have actually done something, or the assertion above passes for
	// the wrong reason.
	var cleared int
	for _, e := range events {
		if e.Kind == EventTurnStart && e.ContextEdit != nil {
			cleared += e.ContextEdit.ClearedResults
		}
	}
	if cleared == 0 {
		t.Error("nothing was ever cleared: the test did not exercise the mechanism")
	}
}

// Without clearing the same session must still die, or the test above proves
// nothing about the mechanism — only that the fake provider is generous.
func TestWithoutContextEditTheSameRunStillDies(t *testing.T) {
	const window = 60_000
	huge := strings.Repeat("source line\n", 4200)
	fat := &fakeTool{name: "read", run: func(context.Context, json.RawMessage) (tools.Result, error) {
		return tools.Result{Text: huge}, nil
	}}
	client := &windowedClient{fake: &fakeClient{}, window: window}

	a := New(Config{Client: client, Registry: tools.NewRegistry(fat), MaxTurns: 30})
	events := drain(a.Run(context.Background(), "read the same file over and over"))
	end := events[len(events)-1]
	if end.Err == nil {
		t.Fatal("the run survived without clearing; the window is not being enforced")
	}
	if !strings.Contains(end.Err.Error(), "context length") {
		t.Errorf("died of something else: %v", end.Err)
	}
}

// The prompt sent must never be the stored history once clearing is on, and the
// stored history must stay whole. This is the property that keeps `-resume`, the
// session file and the web diff view showing the real output.
func TestContextEditLeavesTheTranscriptWhole(t *testing.T) {
	huge := strings.Repeat("y", 40_000)
	fat := &fakeTool{name: "read", run: func(context.Context, json.RawMessage) (tools.Result, error) {
		return tools.Result{Text: huge}, nil
	}}
	c := &fakeClient{responses: []llm.Response{
		toolCalls("read"), toolCalls("read"), toolCalls("read"), toolCalls("read"),
	}}
	a := New(Config{
		Client: c, Registry: tools.NewRegistry(fat), MaxTurns: 10,
		ContextEdit: ContextEditConfig{Trigger: 1, Keep: 1, ClearAtLeast: 1},
	})
	drain(a.Run(context.Background(), "go"))

	// Every stored result is still the original.
	stored := 0
	for _, m := range a.Messages() {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult {
				stored++
				if b.Text != huge {
					t.Fatalf("the transcript holds a placeholder: %.60q", b.Text)
				}
			}
		}
	}
	if stored < 3 {
		t.Fatalf("only %d results stored; the run was too short to prove anything", stored)
	}
	// And what was sent was smaller than what is stored.
	last := c.lastSeen()
	if llm.EstimateTokens(last) >= llm.EstimateTokens(a.Messages()) {
		t.Error("the prompt was not smaller than the transcript")
	}
}

// promptTokens is measured-baseline-plus-estimated-increment, and the increment is
// the half that matters: a batch of tool results can add more between two
// measurements than the headroom below the trigger, so a trigger reading only the
// last measured count would sail past the window on the turn that filled it.
func TestPromptTokensCountsWhatArrivedSinceTheLastMeasurement(t *testing.T) {
	a := New(Config{
		Client: &fakeClient{}, Registry: tools.NewRegistry(),
		ContextEdit: ContextEditConfig{Trigger: 1},
	})
	a.messages = []llm.Message{llm.UserText("hi")}
	a.lastInput, a.measuredThrough = 5_000, 1

	if got := a.promptTokens(); got != 5_000 {
		t.Errorf("with nothing new: %d, want the measured 5000", got)
	}
	a.messages = append(a.messages, llm.Message{Role: llm.RoleUser, Content: []llm.Block{
		{Type: llm.BlockToolResult, ToolUseID: "x", Text: strings.Repeat("z", 8_000)},
	}})
	if got := a.promptTokens(); got != 7_000 {
		t.Errorf("with 2000 tokens appended: %d, want 7000", got)
	}
}

// A resumed session has a baseline that describes a different conversation, so it
// must fall back to estimating the whole history rather than reporting whatever the
// previous run last measured.
func TestPromptTokensResetsOnSetMessages(t *testing.T) {
	a := New(Config{
		Client: &fakeClient{}, Registry: tools.NewRegistry(),
		ContextEdit: ContextEditConfig{Trigger: 1},
	})
	a.lastInput, a.measuredThrough = 100_000, 5
	a.SetMessages([]llm.Message{llm.UserText(strings.Repeat("q", 4_000))})

	got := a.promptTokens()
	if got >= 100_000 {
		t.Errorf("promptTokens = %d: the stale baseline survived SetMessages", got)
	}
	if got < 1_000 {
		t.Errorf("promptTokens = %d, want at least the 1000 tokens of history", got)
	}
}

// Nothing walks the history when nobody asked for clearing.
func TestPromptTokensIsFreeWhenDisabled(t *testing.T) {
	a := New(Config{Client: &fakeClient{}, Registry: tools.NewRegistry()})
	a.messages = []llm.Message{llm.UserText(strings.Repeat("q", 100_000))}
	if got := a.promptTokens(); got != 0 {
		t.Errorf("promptTokens = %d with clearing disabled, want 0", got)
	}
}

// windowedClient rejects any prompt over its window, the way a provider does, and
// otherwise defers to the scripted fake.
type windowedClient struct {
	fake       *fakeClient
	window     int64
	onOverflow func()
}

func (w *windowedClient) Model() string { return "fake" }

func (w *windowedClient) Stream(ctx context.Context, system string, history []llm.Message,
	schemas []llm.ToolSchema, onDelta func(llm.Delta)) (llm.Response, error) {

	size := llm.EstimateTokens(history)
	if size > w.window {
		if w.onOverflow != nil {
			w.onOverflow()
		}
		return llm.Response{}, fmt.Errorf(
			"400 Bad Request: context length exceeded: %d > %d", size, w.window)
	}
	resp, err := w.fake.Stream(ctx, system, history, schemas, onDelta)
	if err != nil {
		return resp, err
	}
	// The provider reports what it actually received, which is what makes the
	// measured half of promptTokens real in this test.
	resp.Usage.Input = size
	// Keep asking for another read until the caller stops it, so the history grows
	// the way a real session's does.
	if len(resp.Message.ToolCalls()) == 0 && resp.StopReason == llm.StopEndTurn {
		if w.fake.callCount() < 20 {
			return toolCallsWithUsage("read", size), nil
		}
	}
	return resp, nil
}

func toolCallsWithUsage(name string, input int64) llm.Response {
	r := toolCalls(name)
	r.Usage.Input = input
	return r
}

// A composition snapshot compares the provider's count for the last prompt against
// an estimate of the whole history. Those differ by exactly what clearing removed,
// so the agent has to report that amount or the comparison is between two different
// bodies of text.
func TestAgentReportsWhatClearingTookOutOfTheLastPrompt(t *testing.T) {
	// A history big enough to trigger, with results large enough to clear.
	msgs := make([]llm.Message, 0, 41)
	msgs = append(msgs, llm.UserText("go"))
	for i := range 20 {
		id := fmt.Sprintf("c%d", i)
		msgs = append(msgs,
			llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
				Type: llm.BlockToolUse, ID: id, Name: "read",
				Input: json.RawMessage(`{"path":"a.go"}`),
			}}},
			llm.Message{Role: llm.RoleUser, Content: []llm.Block{{
				Type: llm.BlockToolResult, ToolUseID: id,
				Text: strings.Repeat("x", 4*5_000),
			}}},
		)
	}

	client := &fakeClient{responses: []llm.Response{{
		Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "done"}}},
		StopReason: llm.StopEndTurn,
		Usage:      llm.Usage{Input: 12_345},
	}}}
	a := New(Config{
		Client:      client,
		Registry:    tools.Default(t.TempDir()),
		ContextEdit: ContextEditConfig{Trigger: 10_000, Keep: 2, ClearAtLeast: 1},
	})
	a.SetMessages(msgs)

	if got := a.ClearedFromPrompt(); got != 0 {
		t.Errorf("before any turn ClearedFromPrompt = %d, want 0", got)
	}
	drain(a.Run(context.Background(), "carry on"))

	cleared := a.ClearedFromPrompt()
	if cleared <= 0 {
		t.Fatalf("ClearedFromPrompt = %d, but this history is well over the trigger", cleared)
	}
	// It has to describe the prompt LastInput reports on, so the two must come from
	// the same turn: the estimate of what was sent is history minus this.
	if a.LastInput() != 12_345 {
		t.Fatalf("LastInput = %d, want the scripted 12345", a.LastInput())
	}

	full := llm.EstimateTokens(a.Messages()) + a.OverheadTokens()
	if cleared >= full {
		t.Errorf("cleared %d of an estimated %d: nothing would be left to calibrate against",
			cleared, full)
	}
}

// And it describes the *last* prompt, not every prompt. Accumulating would make a
// long session report more cleared than the history contains, and Calibration would
// then divide by a denominator that had been subtracted several times over.
func TestClearedFromPromptDescribesOnlyTheLastPrompt(t *testing.T) {
	huge := strings.Repeat("source line\n", 4200)
	fat := &fakeTool{name: "read", run: func(context.Context, json.RawMessage) (tools.Result, error) {
		return tools.Result{Text: huge}, nil
	}}

	// Three tool turns then a final answer, so clearing runs on several prompts.
	client := &fakeClient{responses: []llm.Response{
		toolCalls("read"), toolCalls("read"), toolCalls("read"),
	}}
	a := New(Config{
		Client: client, Registry: tools.NewRegistry(fat), MaxTurns: 10,
		ContextEdit: ContextEditConfig{Trigger: 10_000, Keep: 1, ClearAtLeast: 1},
	})

	events := drain(a.Run(context.Background(), "read it repeatedly"))

	// What the last turn actually reported is the only right answer.
	var passes int
	var lastPass, sumOfPasses int64
	for _, e := range events {
		if e.Kind == EventTurnStart && e.ContextEdit != nil {
			passes++
			lastPass = e.ContextEdit.ClearedTokens + e.ContextEdit.ClearedArgTokens
			sumOfPasses += lastPass
		}
	}
	if passes < 2 {
		t.Fatalf("clearing ran on %d prompt(s); this test needs at least two", passes)
	}
	if sumOfPasses == lastPass {
		t.Fatal("every pass cleared the same total, so accumulation would be invisible here")
	}
	if got := a.ClearedFromPrompt(); got != lastPass {
		t.Errorf("ClearedFromPrompt = %d, want the last pass's %d (sum of all passes is %d)",
			got, lastPass, sumOfPasses)
	}
}

// With clearing off the number must stay zero, so an ordinary session's calibration
// is the raw ratio and not silently corrected by a stale value.
func TestClearedFromPromptStaysZeroWithoutClearing(t *testing.T) {
	a := New(Config{Client: &fakeClient{}, Registry: tools.Default(t.TempDir())})
	drain(a.Run(context.Background(), "hello"))
	if got := a.ClearedFromPrompt(); got != 0 {
		t.Errorf("ClearedFromPrompt = %d with clearing disabled, want 0", got)
	}
}

// overflowingClient rejects any prompt over limit the way Kimi actually does,
// verbatim from a live probe, and counts how many times it did.
type overflowingClient struct {
	limitTokens int64
	rejections  int
	accepted    int64 // estimated size of the prompt that finally got through
}

func (c *overflowingClient) Model() string { return "overflowing" }

func (c *overflowingClient) Stream(
	ctx context.Context, _ string, history []llm.Message, _ []llm.ToolSchema, _ func(llm.Delta),
) (llm.Response, error) {
	size := llm.EstimateTokens(history)
	if size > c.limitTokens {
		c.rejections++
		return llm.Response{}, &llm.APIError{
			Status: 400, StatusText: "400 Bad Request", Type: "invalid_request_error",
			Message: fmt.Sprintf(
				"Invalid request: Your request exceeded model token limit: %d (requested: %d)",
				c.limitTokens, size),
		}
	}
	c.accepted = size
	return llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "ok"}}},
		StopReason: llm.StopEndTurn,
		Usage:      llm.Usage{Input: size},
	}, nil
}

// The failure this exists to remove, stated as the death it was.
//
// A prompt crosses the window, the provider answers 400, and 400 is not retryable —
// so the run ended. The history only grows, so the next message in that session
// produced the same 400 on its first call: one overflow made the session permanently
// unusable and the only way out was to start a new one. The trigger did not save it,
// because the trigger is compared against a figure that is part estimated.
func TestAnOverflowRejectionDoesNotKillTheRun(t *testing.T) {
	// A history well past the fake provider's limit, with clearing switched off, so
	// the only thing that can rescue this run is the reaction to the rejection.
	msgs := []llm.Message{llm.UserText("go")}
	for i := range 30 {
		id := fmt.Sprintf("c%d", i)
		msgs = append(msgs,
			llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
				Type: llm.BlockToolUse, ID: id, Name: "read",
				Input: json.RawMessage(`{"path":"a.go"}`),
			}}},
			llm.Message{Role: llm.RoleUser, Content: []llm.Block{{
				Type: llm.BlockToolResult, ToolUseID: id, Text: strings.Repeat("x", 4*4_000),
			}}},
		)
	}

	client := &overflowingClient{limitTokens: 30_000}
	a := New(Config{Client: client, Registry: tools.Default(t.TempDir())})
	a.SetMessages(msgs)

	events := drain(a.Run(context.Background(), "carry on"))
	end := events[len(events)-1]
	if end.Kind != EventAgentEnd {
		t.Fatalf("last event is %v, want agent_end", end.Kind)
	}
	if end.Err != nil {
		t.Fatalf("the run still died on an overflow: %v", end.Err)
	}
	if client.rejections == 0 {
		t.Fatal("the provider never rejected anything, so this proves nothing")
	}
	if client.accepted > client.limitTokens {
		t.Errorf("the accepted prompt was %d tokens against a limit of %d",
			client.accepted, client.limitTokens)
	}
	// And the transcript is untouched: clearing is a view, so nothing was lost.
	if got := len(a.Messages()); got < len(msgs) {
		t.Errorf("history shrank to %d messages from %d; clearing must not touch the transcript",
			got, len(msgs))
	}
}

// The reaction is bounded. A prompt that cannot be made to fit — because there is
// nothing left to clear — has to surface the provider's error rather than loop, and
// the message has to still be the provider's own.
func TestAnUnfixableOverflowStillFails(t *testing.T) {
	// One enormous user message: no tool results, so clearing has no candidates.
	huge := llm.UserText(strings.Repeat("y", 4*50_000))
	client := &overflowingClient{limitTokens: 1_000}
	a := New(Config{Client: client, Registry: tools.Default(t.TempDir())})
	a.SetMessages([]llm.Message{huge})

	events := drain(a.Run(context.Background(), "go"))
	end := events[len(events)-1]
	if end.Err == nil {
		t.Fatal("an unfixable overflow reported success")
	}
	if !llm.IsContextOverflow(end.Err) {
		t.Errorf("the provider's own error was replaced: %v", end.Err)
	}
	// Exactly one attempt beyond the first would mean it retried an identical
	// prompt; zero extra means forceClear correctly reported it could not help.
	if client.rejections != 1 {
		t.Errorf("provider was asked %d times; a prompt that cannot shrink must be sent once",
			client.rejections)
	}
}

// User text is never a clearing candidate, so a conversation-dominated history
// cannot be rescued this way. Recorded as a test because it is the boundary between
// what this mechanism covers and what would need summarisation — and because a
// future change that started clearing user messages to survive would break a
// property the whole design rests on.
func TestOverflowRecoveryNeverDiscardsUserText(t *testing.T) {
	msgs := []llm.Message{
		llm.UserText("the standing instruction: always use pnpm"),
		llm.UserText(strings.Repeat("z", 4*40_000)),
	}
	client := &overflowingClient{limitTokens: 1_000}
	a := New(Config{Client: client, Registry: tools.Default(t.TempDir())})
	a.SetMessages(msgs)

	drain(a.Run(context.Background(), "go"))

	for i, want := range []string{
		"the standing instruction: always use pnpm",
		strings.Repeat("z", 4*40_000),
	} {
		if got := a.Messages()[i].Text(); got != want {
			t.Errorf("user message %d was altered: %d chars, want %d", i, len(got), len(want))
		}
	}
	// And it was genuinely under pressure, or the assertion above is free.
	if client.rejections == 0 {
		t.Error("no overflow occurred, so nothing was tempted to clear user text")
	}
}

// A forced pass is aggressive but not blind: the most recent tool result survives.
// Clearing the output the model is about to reason about would make the retry answer
// a question whose evidence had just been taken away.
func TestAForcedPassKeepsTheNewestResult(t *testing.T) {
	newest := "the answer is 42"
	msgs := []llm.Message{llm.UserText("go")}
	for i := range 10 {
		id := fmt.Sprintf("c%d", i)
		text := strings.Repeat("x", 4*4_000)
		if i == 9 {
			text = newest
		}
		msgs = append(msgs,
			llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
				Type: llm.BlockToolUse, ID: id, Name: "read",
				Input: json.RawMessage(`{"path":"a.go"}`),
			}}},
			llm.Message{Role: llm.RoleUser, Content: []llm.Block{{
				Type: llm.BlockToolResult, ToolUseID: id, Text: text,
			}}},
		)
	}

	a := New(Config{Client: &fakeClient{}, Registry: tools.Default(t.TempDir())})
	a.SetMessages(msgs)

	prompt, ok := a.forceClear()
	if !ok {
		t.Fatal("a forced pass on a large history freed nothing")
	}
	// Last block of the last message is the newest result.
	last := prompt[len(prompt)-1].Content[0]
	if last.Text != newest {
		t.Errorf("the newest result was cleared: %.60q", last.Text)
	}
	// And it really was aggressive, or the assertion above is free.
	var placeholders int
	for _, m := range prompt {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult && strings.HasPrefix(b.Text, "[") {
				placeholders++
			}
		}
	}
	if placeholders < 8 {
		t.Errorf("only %d of 10 results were cleared; a forced pass should spare one", placeholders)
	}
}

// Trigger must stay positive: editContext treats a non-positive trigger as
// "clearing is off" and returns the history untouched, so a forced pass with a zero
// trigger would silently do nothing and the run would die as before.
func TestAForcedPassIgnoresWhetherTheTriggerWouldHaveFired(t *testing.T) {
	msgs := []llm.Message{llm.UserText("go")}
	for i := range 6 {
		id := fmt.Sprintf("c%d", i)
		msgs = append(msgs,
			llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
				Type: llm.BlockToolUse, ID: id, Name: "read",
				Input: json.RawMessage(`{"path":"a.go"}`),
			}}},
			llm.Message{Role: llm.RoleUser, Content: []llm.Block{{
				Type: llm.BlockToolResult, ToolUseID: id, Text: strings.Repeat("x", 4*2_000),
			}}},
		)
	}
	// Clearing is switched off entirely for this session, which is the case that
	// matters: the provider's rejection has to override the policy, not consult it.
	a := New(Config{Client: &fakeClient{}, Registry: tools.Default(t.TempDir())})
	a.SetMessages(msgs)
	if a.contextEdit.Trigger != 0 {
		t.Fatalf("this test needs clearing disabled, got trigger %d", a.contextEdit.Trigger)
	}
	if _, ok := a.forceClear(); !ok {
		t.Error("a forced pass did nothing on a session with clearing disabled")
	}
}

// ExcludeTools survives a forced pass. It is the caller saying "never clear this",
// and a mechanism that discards an explicit instruction the moment it becomes
// inconvenient is worse than one that reports it could not help — so if the
// exclusion is what makes the prompt too large, the provider's error is returned
// instead of the instruction being quietly broken.
func TestAForcedPassStillHonoursTheExclusionList(t *testing.T) {
	msgs := []llm.Message{llm.UserText("go")}
	for i := range 8 {
		id := fmt.Sprintf("c%d", i)
		msgs = append(msgs,
			llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
				Type: llm.BlockToolUse, ID: id, Name: "read",
				Input: json.RawMessage(`{"path":"a.go"}`),
			}}},
			llm.Message{Role: llm.RoleUser, Content: []llm.Block{{
				Type: llm.BlockToolResult, ToolUseID: id, Text: strings.Repeat("x", 4*3_000),
			}}},
		)
	}

	// Every result in this history comes from the excluded tool, so a forced pass
	// has nothing it is allowed to touch.
	a := New(Config{
		Client: &fakeClient{}, Registry: tools.Default(t.TempDir()),
		ContextEdit: ContextEditConfig{Trigger: 1_000, ExcludeTools: []string{"read"}},
	})
	a.SetMessages(msgs)

	if _, ok := a.forceClear(); ok {
		t.Error("a forced pass cleared results from an excluded tool")
	}

	// And the same history without the exclusion is clearable, so the assertion
	// above is about the exclusion and not about the history being too small.
	b := New(Config{
		Client: &fakeClient{}, Registry: tools.Default(t.TempDir()),
		ContextEdit: ContextEditConfig{Trigger: 1_000},
	})
	b.SetMessages(msgs)
	if _, ok := b.forceClear(); !ok {
		t.Fatal("the same history cleared nothing without the exclusion; the test proves nothing")
	}
}
