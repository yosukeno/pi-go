package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangy/pi-go/llm"
	"github.com/wangy/pi-go/tools"
)

// --- test doubles ---

// fakeTool is scripted: it records calls, optionally sleeps, and can fail.
type fakeTool struct {
	name string
	mode tools.ExecutionMode
	// run, when set, replaces the default behaviour.
	run func(ctx context.Context, args json.RawMessage) (tools.Result, error)

	mu      sync.Mutex
	calls   []string
	active  atomic.Int32
	maxSeen atomic.Int32
}

func (f *fakeTool) Name() string                       { return f.name }
func (f *fakeTool) Description() string                { return f.name }
func (f *fakeTool) InputSchema() map[string]any        { return map[string]any{"type": "object"} }
func (f *fakeTool) ExecutionMode() tools.ExecutionMode { return f.mode }

func (f *fakeTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	n := f.active.Add(1)
	for {
		seen := f.maxSeen.Load()
		if n <= seen || f.maxSeen.CompareAndSwap(seen, n) {
			break
		}
	}
	defer f.active.Add(-1)

	f.mu.Lock()
	f.calls = append(f.calls, string(args))
	f.mu.Unlock()

	if f.run != nil {
		return f.run(ctx, args)
	}
	return tools.Result{Text: "ok:" + f.name}, nil
}

func (f *fakeTool) callArgs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// fakeClient replays a scripted sequence of assistant responses.
type fakeClient struct {
	responses []llm.Response
	mu        sync.Mutex
	turn      int
	// calls counts every Stream invocation, including the ones served by the
	// default response once the script runs out.
	calls int
	// seen records the history handed to each call, so tests can assert on what
	// the model would actually receive next turn.
	seen [][]llm.Message
	// onStream runs at the start of each call, on the run goroutine. It exists for
	// the steering tests: it is the only hook that fires while the model is
	// "producing" a response and no tool is running.
	onStream func(n int)
	// deltas, when set, is handed the call's onDelta so a test can replay
	// incremental provider output — the argument fragments of a tool call, say —
	// the way a streaming client would produce it. Like onStream it gets the
	// 1-based call number, since every turn streams, not just the one under test.
	deltas func(n int, emit func(llm.Delta))
}

func (c *fakeClient) Model() string { return "fake" }

func (c *fakeClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// lastSeen is the history of the most recent request, which is what a test asserting
// on the prompt — as opposed to the transcript — has to look at.
func (c *fakeClient) lastSeen() []llm.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.seen) == 0 {
		return nil
	}
	return c.seen[len(c.seen)-1]
}

func (c *fakeClient) Stream(
	ctx context.Context, _ string, history []llm.Message, _ []llm.ToolSchema, onDelta func(llm.Delta),
) (llm.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.seen = append(c.seen, append([]llm.Message(nil), history...))
	if c.onStream != nil {
		c.onStream(c.calls)
	}
	if c.deltas != nil && onDelta != nil {
		c.deltas(c.calls, onDelta)
	}
	if c.turn >= len(c.responses) {
		return llm.Response{
			Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "done"}}},
			StopReason: llm.StopEndTurn,
		}, nil
	}
	r := c.responses[c.turn]
	c.turn++
	return r, nil
}

func toolCalls(names ...string) llm.Response {
	msg := llm.Message{Role: llm.RoleAssistant}
	for i, n := range names {
		msg.Content = append(msg.Content, llm.Block{
			Type:  llm.BlockToolUse,
			ID:    fmt.Sprintf("call%d", i),
			Name:  n,
			Input: json.RawMessage(fmt.Sprintf(`{"i":%d}`, i)),
		})
	}
	return llm.Response{Message: msg, StopReason: llm.StopToolUse}
}

// drain consumes every event and returns them in arrival order.
func drain(events <-chan Event) []Event {
	var out []Event
	for e := range events {
		out = append(out, e)
	}
	return out
}

// --- 1. result count and ordering ---

func TestBatchProducesOneOrderedResultPerCall(t *testing.T) {
	slow := &fakeTool{name: "slow", run: func(ctx context.Context, _ json.RawMessage) (tools.Result, error) {
		time.Sleep(30 * time.Millisecond)
		return tools.Result{Text: "slow"}, nil
	}}
	fast := &fakeTool{name: "fast"}
	c := &fakeClient{responses: []llm.Response{toolCalls("slow", "fast", "slow")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(slow, fast)})

	drain(a.Run(context.Background(), "go"))

	// The tool results ride in the user message that follows the assistant one.
	var results []llm.Block
	for _, m := range a.Messages() {
		if m.Role == llm.RoleUser && len(m.Content) > 0 && m.Content[0].Type == llm.BlockToolResult {
			results = m.Content
		}
	}
	if len(results) != 3 {
		t.Fatalf("got %d tool results, want 3", len(results))
	}
	// Ordering must match the tool_use blocks even though "fast" finished first.
	for i, want := range []string{"call0", "call1", "call2"} {
		if results[i].ToolUseID != want {
			t.Errorf("result[%d].ToolUseID = %q, want %q (order must follow tool_use)",
				i, results[i].ToolUseID, want)
		}
	}
}

// --- 2. one failure must not cancel its siblings ---

func TestFailingCallDoesNotCancelSiblings(t *testing.T) {
	boom := &fakeTool{name: "boom", run: func(context.Context, json.RawMessage) (tools.Result, error) {
		return tools.Result{}, fmt.Errorf("exploded")
	}}
	slow := &fakeTool{name: "slow", run: func(ctx context.Context, _ json.RawMessage) (tools.Result, error) {
		select {
		case <-time.After(50 * time.Millisecond):
			return tools.Result{Text: "finished"}, nil
		case <-ctx.Done():
			return tools.Result{}, ctx.Err()
		}
	}}
	c := &fakeClient{responses: []llm.Response{toolCalls("boom", "slow", "slow")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(boom, slow)})

	drain(a.Run(context.Background(), "go"))

	var results []llm.Block
	for _, m := range a.Messages() {
		if m.Role == llm.RoleUser && len(m.Content) > 0 && m.Content[0].Type == llm.BlockToolResult {
			results = m.Content
		}
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3: every tool_use needs a pair", len(results))
	}
	if !results[0].IsError || !strings.Contains(results[0].Text, "exploded") {
		t.Errorf("result[0] should carry the failure, got %+v", results[0])
	}
	for _, i := range []int{1, 2} {
		if results[i].IsError {
			t.Errorf("result[%d] was cancelled by its sibling's failure: %q", i, results[i].Text)
		}
		if results[i].Text != "finished" {
			t.Errorf("result[%d].Text = %q, want %q", i, results[i].Text, "finished")
		}
	}
}

// --- 3. sequential tools serialize the whole batch ---

func TestSequentialToolSerializesBatch(t *testing.T) {
	var mu sync.Mutex
	var overlaps int
	inFlight := 0
	track := func(ctx context.Context, _ json.RawMessage) (tools.Result, error) {
		mu.Lock()
		inFlight++
		if inFlight > 1 {
			overlaps++
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		return tools.Result{Text: "ok"}, nil
	}

	par := &fakeTool{name: "par", mode: tools.Parallel, run: track}
	seq := &fakeTool{name: "seq", mode: tools.Sequential, run: track}
	c := &fakeClient{responses: []llm.Response{toolCalls("par", "par", "seq")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(par, seq)})

	drain(a.Run(context.Background(), "go"))

	if overlaps != 0 {
		t.Errorf("observed %d overlapping executions; one sequential tool must serialize the batch", overlaps)
	}
}

func TestParallelBatchActuallyOverlaps(t *testing.T) {
	par := &fakeTool{name: "par", mode: tools.Parallel,
		run: func(context.Context, json.RawMessage) (tools.Result, error) {
			time.Sleep(20 * time.Millisecond)
			return tools.Result{Text: "ok"}, nil
		}}
	c := &fakeClient{responses: []llm.Response{toolCalls("par", "par", "par")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(par)})

	drain(a.Run(context.Background(), "go"))

	if got := par.maxSeen.Load(); got < 2 {
		t.Errorf("max concurrent executions = %d, want at least 2", got)
	}
}

// --- 4. concurrency limit ---

func TestToolConcurrencyIsBounded(t *testing.T) {
	const limit = 3
	par := &fakeTool{name: "par", run: func(context.Context, json.RawMessage) (tools.Result, error) {
		time.Sleep(15 * time.Millisecond)
		return tools.Result{Text: "ok"}, nil
	}}
	names := make([]string, 20)
	for i := range names {
		names[i] = "par"
	}
	c := &fakeClient{responses: []llm.Response{toolCalls(names...)}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(par), ToolConcurrency: limit})

	drain(a.Run(context.Background(), "go"))

	if got := par.maxSeen.Load(); got > limit {
		t.Errorf("max concurrent executions = %d, want at most %d", got, limit)
	}
	if got := len(par.callArgs()); got != 20 {
		t.Errorf("ran %d calls, want 20", got)
	}
}

// --- 5. session-wide sequential override ---

func TestSessionWideSequentialOverride(t *testing.T) {
	par := &fakeTool{name: "par", run: func(context.Context, json.RawMessage) (tools.Result, error) {
		time.Sleep(15 * time.Millisecond)
		return tools.Result{Text: "ok"}, nil
	}}
	c := &fakeClient{responses: []llm.Response{toolCalls("par", "par", "par")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(par), ToolExecution: tools.Sequential})

	drain(a.Run(context.Background(), "go"))

	if got := par.maxSeen.Load(); got != 1 {
		t.Errorf("max concurrent = %d, want 1 under a session-wide sequential override", got)
	}
}

// --- 6. cancellation still pairs every tool_use ---

func TestCancellationStillPairsEveryToolUse(t *testing.T) {
	block := &fakeTool{name: "block", run: func(ctx context.Context, _ json.RawMessage) (tools.Result, error) {
		<-ctx.Done()
		return tools.Result{}, ctx.Err()
	}}
	c := &fakeClient{responses: []llm.Response{toolCalls("block", "block", "block")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(block)})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	drain(a.Run(ctx, "go"))

	var assistantCalls, toolResults int
	for _, m := range a.Messages() {
		for _, b := range m.Content {
			switch b.Type {
			case llm.BlockToolUse:
				assistantCalls++
			case llm.BlockToolResult:
				toolResults++
			}
		}
	}
	// An unpaired tool_use makes the session unresumable: the next request is
	// rejected outright.
	if assistantCalls != toolResults {
		t.Errorf("%d tool_use blocks but %d tool_result blocks: the session is unresumable",
			assistantCalls, toolResults)
	}
}

// --- gate ---

type fakeGate struct {
	mu       sync.Mutex
	seen     []GateRequest
	decide   func(GateRequest) GateDecision
	inFlight int
	overlaps int
}

func (g *fakeGate) Review(ctx context.Context, req GateRequest) GateDecision {
	g.mu.Lock()
	g.seen = append(g.seen, req)
	g.inFlight++
	if g.inFlight > 1 {
		g.overlaps++
	}
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.inFlight--
		g.mu.Unlock()
	}()
	if g.decide != nil {
		return g.decide(req)
	}
	return Allow
}

// A refusal must reach the model as a tool result and the loop must keep going.
// Ending the run instead would make one click cost the whole turn.
func TestGateDenialBecomesToolResultAndLoopContinues(t *testing.T) {
	tool := &fakeTool{name: "danger"}
	gate := &fakeGate{decide: func(GateRequest) GateDecision {
		return Deny("user said no")
	}}
	c := &fakeClient{responses: []llm.Response{toolCalls("danger")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), Gate: gate})

	events := drain(a.Run(context.Background(), "go"))

	if got := len(tool.callArgs()); got != 0 {
		t.Errorf("blocked tool ran anyway (%d times)", got)
	}
	var end *Event
	for i := range events {
		if events[i].Kind == EventAgentEnd {
			end = &events[i]
		}
	}
	if end == nil || end.Err != nil {
		t.Fatalf("a refusal must not end the run with an error, got %+v", end)
	}
	// The loop asked the model again after the refusal, which is the whole point.
	if got := c.callCount(); got < 2 {
		t.Errorf("model was consulted %d time(s); the loop should continue after a refusal", got)
	}

	var found bool
	for _, m := range a.Messages() {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult && b.IsError && strings.Contains(b.Text, "user said no") {
				found = true
			}
		}
	}
	if !found {
		t.Error("the refusal reason never reached the model")
	}
}

// Approving with edited arguments must run the edited ones, and the tool_start
// event must report what actually ran rather than what was asked for.
func TestGateCanRewriteArguments(t *testing.T) {
	tool := &fakeTool{name: "t"}
	gate := &fakeGate{decide: func(GateRequest) GateDecision {
		return GateDecision{Allow: true, Args: json.RawMessage(`{"i":"rewritten"}`)}
	}}
	c := &fakeClient{responses: []llm.Response{toolCalls("t")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), Gate: gate})

	events := drain(a.Run(context.Background(), "go"))

	got := tool.callArgs()
	if len(got) != 1 || !strings.Contains(got[0], "rewritten") {
		t.Fatalf("tool received %v, want the rewritten arguments", got)
	}
	for _, e := range events {
		if e.Kind == EventToolStart && !strings.Contains(e.ToolArgs, "rewritten") {
			t.Errorf("tool_start reported %q; it should show the arguments that ran", e.ToolArgs)
		}
	}
}

func TestGateSeesEveryCallWithTurnAndCallID(t *testing.T) {
	tool := &fakeTool{name: "t"}
	gate := &fakeGate{}
	c := &fakeClient{responses: []llm.Response{toolCalls("t", "t"), toolCalls("t")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), Gate: gate})

	drain(a.Run(context.Background(), "go"))

	if len(gate.seen) != 3 {
		t.Fatalf("gate reviewed %d calls, want 3", len(gate.seen))
	}
	if gate.seen[0].Turn != 1 || gate.seen[2].Turn != 2 {
		t.Errorf("turn numbers wrong: %d then %d", gate.seen[0].Turn, gate.seen[2].Turn)
	}
	for i, r := range gate.seen {
		if r.CallID == "" || r.ToolName != "t" {
			t.Errorf("request[%d] = %+v, missing identity", i, r)
		}
	}
}

// A nil gate must leave behaviour exactly as it was: this is what keeps -p
// scriptable and usable as a subagent.
func TestNilGateRunsEverything(t *testing.T) {
	tool := &fakeTool{name: "t"}
	c := &fakeClient{responses: []llm.Response{toolCalls("t", "t")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool)})

	drain(a.Run(context.Background(), "go"))

	if got := len(tool.callArgs()); got != 2 {
		t.Errorf("ran %d calls, want 2 with no gate configured", got)
	}
}

// A gate that blocks is allowed to; it must not stall the run past cancellation.
func TestGateRespectsCancellation(t *testing.T) {
	tool := &fakeTool{name: "t"}
	gate := &fakeGate{decide: func(GateRequest) GateDecision {
		// Simulates waiting for a human who never answers.
		time.Sleep(20 * time.Millisecond)
		return Deny("timed out")
	}}
	c := &fakeClient{responses: []llm.Response{toolCalls("t", "t", "t")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), Gate: gate})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() { drain(a.Run(ctx, "go")); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("run did not unwind after cancellation")
	}
	if got := len(tool.callArgs()); got != 0 {
		t.Errorf("denied tool ran %d times", got)
	}
}

// --- unknown tool ---

func TestUnknownToolIsReportedNotFatal(t *testing.T) {
	c := &fakeClient{responses: []llm.Response{toolCalls("ghost")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry()})

	events := drain(a.Run(context.Background(), "go"))

	for _, e := range events {
		if e.Kind == EventAgentEnd && e.Err != nil {
			t.Fatalf("an unknown tool should not end the run: %v", e.Err)
		}
	}
	var found bool
	for _, m := range a.Messages() {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult && b.IsError && strings.Contains(b.Text, "not found") {
				found = true
			}
		}
	}
	if !found {
		t.Error("the model was never told the tool does not exist")
	}
}

func TestMaxTurnsStopsTheLoop(t *testing.T) {
	tool := &fakeTool{name: "t"}
	// Always asks for another tool call, so only maxTurns can stop it.
	responses := make([]llm.Response, 10)
	for i := range responses {
		responses[i] = toolCalls("t")
	}
	c := &fakeClient{responses: responses}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), MaxTurns: 3})

	events := drain(a.Run(context.Background(), "go"))

	var end *Event
	for i := range events {
		if events[i].Kind == EventAgentEnd {
			end = &events[i]
		}
	}
	if end == nil || end.Err == nil || !strings.Contains(end.Err.Error(), "3 turns") {
		t.Fatalf("expected a max-turns error, got %+v", end)
	}
}

// The gate is a person, so a parallel batch reviews its calls one at a time even
// though it runs them together. Reviewing concurrently means three approval cards
// appearing at once, in an order nobody chose, each with its own countdown —
// reachable today with `strict` mode and one turn that edits three files.
//
// The three assertions are one unit and none of them can be dropped:
//   - without the overlap check the bug is not caught at all
//   - without the order check, a mutex around the gate would pass while still
//     asking the questions in whatever order the goroutines won
//   - without the concurrency check the whole thing could be "fixed" by
//     serializing the batch, which throws away the feature to fix its seam
func TestParallelBatchReviewsSeriallyAndStillRunsConcurrently(t *testing.T) {
	tool := &fakeTool{name: "t", run: func(ctx context.Context, _ json.RawMessage) (tools.Result, error) {
		// Long enough that three overlapping executions are observable.
		time.Sleep(40 * time.Millisecond)
		return tools.Result{Text: "ok"}, nil
	}}
	gate := &fakeGate{decide: func(GateRequest) GateDecision {
		// A human takes time to answer. Without this the concurrent reviews of the
		// old code could each finish before the next began, and the bug would hide.
		time.Sleep(15 * time.Millisecond)
		return Allow
	}}
	c := &fakeClient{responses: []llm.Response{toolCalls("t", "t", "t")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), Gate: gate})

	drain(a.Run(context.Background(), "go"))

	if gate.overlaps != 0 {
		t.Errorf("gate saw %d concurrent reviews; approvals must be asked one at a time", gate.overlaps)
	}

	var order []string
	for _, r := range gate.seen {
		order = append(order, r.CallID)
	}
	want := []string{"call0", "call1", "call2"}
	if len(order) != len(want) {
		t.Fatalf("gate reviewed %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("gate reviewed in order %v, want the order the model asked for %v", order, want)
		}
	}

	if got := tool.maxSeen.Load(); got < 2 {
		t.Errorf("tools ran %d at a time; serializing the batch is not the fix", got)
	}
}

// Cancelling while an earlier call in the batch is still under review must not
// spend effort on the calls already approved: nobody is waiting for the result.
func TestCancelDuringBatchReviewSkipsExecution(t *testing.T) {
	tool := &fakeTool{name: "t"}
	gate := &fakeGate{decide: func(GateRequest) GateDecision {
		time.Sleep(20 * time.Millisecond)
		return Allow
	}}
	c := &fakeClient{responses: []llm.Response{toolCalls("t", "t", "t")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), Gate: gate})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	drain(a.Run(ctx, "go"))

	// The first call may have been approved and run before the deadline; the point
	// is that the batch does not run all three regardless.
	if got := len(tool.callArgs()); got > 1 {
		t.Errorf("ran %d calls after cancellation, want at most 1", got)
	}

	// Every tool_use still needs a result, cancelled or not: an assistant message
	// with an unpaired tool_use is rejected on the next request, which would make
	// the session unresumable.
	msgs := a.Messages()
	last := msgs[len(msgs)-1]
	var results int
	for _, b := range last.Content {
		if b.Type == llm.BlockToolResult {
			results++
		}
	}
	if results != 3 {
		t.Errorf("got %d tool results for 3 calls; every tool_use must be answered", results)
	}
}

// A tool's structured payload has to ride in the transcript, not only in the live
// event stream, or it is gone the moment the session is reloaded — which is when
// a diff is most worth looking at.
func TestToolResultBlockCarriesDetailsForThePersistedTranscript(t *testing.T) {
	tool := &fakeTool{name: "t", run: func(context.Context, json.RawMessage) (tools.Result, error) {
		return tools.Result{
			Text:    "Successfully replaced 1 block(s)",
			Details: map[string]any{"diff": "@@ -1 +1 @@", "added": 1},
		}, nil
	}}
	c := &fakeClient{responses: []llm.Response{toolCalls("t")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool)})

	events := drain(a.Run(context.Background(), "go"))

	var block *llm.Block
	for _, m := range a.Messages() {
		for i := range m.Content {
			if m.Content[i].Type == llm.BlockToolResult {
				block = &m.Content[i]
			}
		}
	}
	if block == nil {
		t.Fatal("no tool result in the transcript")
	}
	var got struct {
		Diff  string `json:"diff"`
		Added int    `json:"added"`
	}
	if err := json.Unmarshal(block.Details, &got); err != nil {
		t.Fatalf("details missing from the tool result block: %v", err)
	}
	if got.Diff != "@@ -1 +1 @@" || got.Added != 1 {
		t.Errorf("details = %+v", got)
	}

	// The live event must still carry the payload in its original form. Interfaces
	// that are already connected type-switch on it (tools.EditDetails and friends)
	// and must not be pushed onto the marshalled path.
	var end *Event
	for i := range events {
		if events[i].Kind == EventToolEnd {
			end = &events[i]
		}
	}
	if end == nil || end.ToolDetails == nil {
		t.Fatal("tool_end lost its details")
	}
	if _, ok := end.ToolDetails.(map[string]any); !ok {
		t.Errorf("live details were converted to %T; they must stay as the tool returned them", end.ToolDetails)
	}
}

// A tool that returns no payload must not put an empty details field in the
// transcript: session files are read by people.
func TestToolResultWithoutDetailsStaysClean(t *testing.T) {
	tool := &fakeTool{name: "t"} // default run returns no Details
	c := &fakeClient{responses: []llm.Response{toolCalls("t")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool)})

	drain(a.Run(context.Background(), "go"))

	for _, m := range a.Messages() {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult && b.Details != nil {
				t.Errorf("details = %s, want nothing", b.Details)
			}
		}
	}
}

// --- steering ---

// A message queued mid-run must land after the current turn's tool results and
// before the next model call. That position is not a preference: a tool_use may
// only be answered by its tool_result, so anywhere earlier produces a request the
// API rejects.
//
// Steering is queued from a second goroutine, the way a request handler does it,
// so -race covers the path that actually exists. The handshake keeps it
// deterministic: the tool holds the turn open until the message is in.
func TestSteeringLandsBetweenToolResultsAndTheNextCall(t *testing.T) {
	running, queued := make(chan struct{}), make(chan struct{})
	tool := &fakeTool{name: "t", run: func(context.Context, json.RawMessage) (tools.Result, error) {
		close(running)
		<-queued
		return tools.Result{Text: "ok"}, nil
	}}
	c := &fakeClient{responses: []llm.Response{toolCalls("t")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool)})

	go func() {
		defer close(queued)
		<-running
		if !a.Steer("actually, do it the other way") {
			t.Error("steering was refused while a tool was running")
		}
	}()

	events := drain(a.Run(context.Background(), "go"))

	// Shape check on the transcript: prompt, assistant tool_use, tool results, the
	// steering message, then the answer.
	var shape []string
	for _, m := range a.Messages() {
		for _, b := range m.Content {
			shape = append(shape, string(m.Role)+":"+string(b.Type))
		}
	}
	want := []string{
		"user:text",
		"assistant:tool_use",
		"user:tool_result",
		"user:text", // the steering message
		"assistant:text",
	}
	if len(shape) != len(want) {
		t.Fatalf("transcript shape %v, want %v", shape, want)
	}
	for i := range want {
		if shape[i] != want[i] {
			t.Fatalf("transcript shape %v, want %v", shape, want)
		}
	}

	// And it must be announced, or the timeline would show an answer to a question
	// nobody can see.
	var steerText string
	for _, e := range events {
		if e.Kind == EventSteer {
			steerText = e.Text
		}
	}
	if steerText != "actually, do it the other way" {
		t.Errorf("steer event text = %q", steerText)
	}
}

// The model finishing is not a reason to drop a message that arrived while it was
// finishing: that is precisely the message someone typed because they saw where
// the answer was going.
func TestSteeringAfterTheModelWouldStopContinuesTheRun(t *testing.T) {
	var a *Agent
	c := &fakeClient{onStream: func(n int) {
		// Queue while the first response — which has no tool calls, so it would
		// have ended the run — is being produced.
		if n == 1 && !a.Steer("one more thing") {
			t.Error("steering was refused mid-run")
		}
	}}
	a = New(Config{Client: c, Registry: tools.NewRegistry()})

	drain(a.Run(context.Background(), "go"))

	if got := c.callCount(); got != 2 {
		t.Errorf("the model was called %d time(s); steering should have bought one more turn", got)
	}
	var texts []string
	for _, m := range a.Messages() {
		if m.Role == llm.RoleUser {
			texts = append(texts, m.Text())
		}
	}
	if len(texts) != 2 || texts[1] != "one more thing" {
		t.Errorf("user messages = %v, want the prompt then the steering message", texts)
	}
}

// Steering is only for a run in flight. Rejecting it here is what lets the caller
// start a new run instead, and the answer has to come from the same lock that
// would accept it — checking "is it active" first would be a race.
func TestSteerIsRefusedWithNoRunInFlight(t *testing.T) {
	c := &fakeClient{}
	a := New(Config{Client: c, Registry: tools.NewRegistry()})

	if a.Steer("too early") {
		t.Error("steering was accepted before the run started")
	}
	drain(a.Run(context.Background(), "go"))
	if a.Steer("too late") {
		t.Error("steering was accepted after the run ended")
	}
	if a.Steer("") {
		t.Error("an empty message was accepted")
	}
	for _, m := range a.Messages() {
		if m.Role == llm.RoleUser && (m.Text() == "too early" || m.Text() == "too late") {
			t.Fatalf("a refused message reached the transcript: %q", m.Text())
		}
	}
}

// A message can always be accepted moments before the run hits its turn limit.
// Losing it silently would be the worst outcome, so the run reports it and the
// interface can hand the text back.
func TestUndeliveredSteeringIsReportedNotDropped(t *testing.T) {
	tool := &fakeTool{name: "t"}
	// Two tool-calling turns with a limit of two, so the run ends on the turn
	// boundary right after the message is queued.
	c := &fakeClient{responses: []llm.Response{toolCalls("t"), toolCalls("t")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), MaxTurns: 2})

	var runs atomic.Int32
	tool.run = func(context.Context, json.RawMessage) (tools.Result, error) {
		// Queue during the last permitted turn: there is no turn left to deliver it
		// in, which is the window this test is about.
		if runs.Add(1) == 2 {
			if !a.Steer("this one will not make it") {
				t.Error("steering was refused while the run was still going")
			}
		}
		return tools.Result{Text: "ok"}, nil
	}

	events := drain(a.Run(context.Background(), "go"))

	var end *Event
	for i := range events {
		if events[i].Kind == EventAgentEnd {
			end = &events[i]
		}
	}
	if end == nil {
		t.Fatal("no agent_end")
	}
	if len(end.Undelivered) != 1 || end.Undelivered[0] != "this one will not make it" {
		t.Errorf("undelivered = %v, want the queued message", end.Undelivered)
	}
	// Once reported it must be gone, or the next run would replay it out of
	// nowhere.
	if a.Steer("probe") {
		t.Error("the run is still accepting steering after it ended")
	}
}

// A batch that spends its approval budget must refuse the rest of its calls and
// let the run continue, rather than let the additive gate waits outlast the run's
// own deadline and lose the whole turn.
func TestReviewBudgetRefusesTheRestAndKeepsTheRunAlive(t *testing.T) {
	tool := &fakeTool{name: "t"}
	// Every review outlasts the budget, so only the first call gets asked.
	gate := &fakeGate{decide: func(GateRequest) GateDecision {
		time.Sleep(30 * time.Millisecond)
		return Allow
	}}
	c := &fakeClient{responses: []llm.Response{toolCalls("t", "t", "t", "t")}}
	a := New(Config{
		Client:       c,
		Registry:     tools.NewRegistry(tool),
		Gate:         gate,
		ReviewBudget: 10 * time.Millisecond,
	})

	events := drain(a.Run(context.Background(), "go"))

	// The first call is always asked: a budget that can refuse everything
	// unreviewed is just a broken gate.
	gate.mu.Lock()
	asked := len(gate.seen)
	gate.mu.Unlock()
	if asked != 1 {
		t.Errorf("gate was asked %d time(s), want 1: the budget should stop after the first", asked)
	}
	if got := len(tool.callArgs()); got != 1 {
		t.Errorf("tool ran %d time(s), want 1: unreviewed calls must not execute", got)
	}

	// Every tool_use still needs a result, or the next request is rejected and the
	// session becomes unresumable.
	var results, refusals int
	for _, m := range a.Messages() {
		for _, b := range m.Content {
			if b.Type != llm.BlockToolResult {
				continue
			}
			results++
			if b.IsError && strings.Contains(b.Text, "approval budget") {
				refusals++
			}
		}
	}
	if results < 4 {
		t.Errorf("got %d tool results for 4 calls; an unpaired tool_use breaks resume", results)
	}
	if refusals != 3 {
		t.Errorf("got %d budget refusals, want 3", refusals)
	}

	// The run survives, which is the difference from the deadline killing it.
	var end *Event
	for i := range events {
		if events[i].Kind == EventAgentEnd {
			end = &events[i]
		}
	}
	if end == nil || end.Err != nil {
		t.Fatalf("spending the budget must not fail the run, got %+v", end)
	}
	if got := c.callCount(); got < 2 {
		t.Errorf("model was consulted %d time(s); it should get the chance to react", got)
	}
}

// Without a budget the behaviour is unchanged: every call gets reviewed. This is
// the CLI's case, where there is no gate and nothing to bound.
func TestNoReviewBudgetReviewsEveryCall(t *testing.T) {
	tool := &fakeTool{name: "t"}
	gate := &fakeGate{decide: func(GateRequest) GateDecision {
		time.Sleep(5 * time.Millisecond)
		return Allow
	}}
	c := &fakeClient{responses: []llm.Response{toolCalls("t", "t", "t")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), Gate: gate})

	drain(a.Run(context.Background(), "go"))

	gate.mu.Lock()
	asked := len(gate.seen)
	gate.mu.Unlock()
	if asked != 3 {
		t.Errorf("gate was asked %d time(s), want 3", asked)
	}
	if got := len(tool.callArgs()); got != 3 {
		t.Errorf("tool ran %d time(s), want 3", got)
	}
}

// schemaTool declares one required field so the loop's validation has something
// to enforce. It records whether it was ever executed.
type schemaTool struct {
	fakeTool
}

func (s *schemaTool) InputSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"path": map[string]any{"type": "string"}},
		"required":   []string{"path"},
	}
}

// Invalid arguments must be caught before the tool runs and before the gate asks
// anyone: the call cannot succeed whatever a human says, and the model needs a
// message it can act on rather than a Go unmarshal error.
func TestInvalidArgumentsAreRefusedBeforeExecutionAndBeforeTheGate(t *testing.T) {
	tool := &schemaTool{fakeTool{name: "probe"}}
	gate := &fakeGate{}
	c := &fakeClient{responses: []llm.Response{{
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
			Type: llm.BlockToolUse, ID: "call0", Name: "probe",
			Input: json.RawMessage(`{"file_path":"a.go"}`),
		}}},
		StopReason: llm.StopToolUse,
	}}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), Gate: gate})

	events := drain(a.Run(context.Background(), "go"))

	if got := len(tool.callArgs()); got != 0 {
		t.Errorf("tool ran %d time(s) with invalid arguments", got)
	}
	gate.mu.Lock()
	asked := len(gate.seen)
	gate.mu.Unlock()
	if asked != 0 {
		t.Errorf("the gate was asked %d time(s) about a call that cannot run", asked)
	}

	// The refusal is a tool result, so the model can fix it and carry on. Ending
	// the run instead would make one malformed call cost the whole turn.
	var found string
	for _, m := range a.Messages() {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult && b.IsError {
				found = b.Text
			}
		}
	}
	if found == "" {
		t.Fatal("no error tool result reached the model")
	}
	for _, want := range []string{`missing required field "path"`, "file_path"} {
		if !strings.Contains(found, want) {
			t.Errorf("result %q does not contain %q", found, want)
		}
	}
	if strings.Contains(found, "Go struct") {
		t.Errorf("result leaks a Go unmarshal error: %q", found)
	}

	var end *Event
	for i := range events {
		if events[i].Kind == EventAgentEnd {
			end = &events[i]
		}
	}
	if end == nil || end.Err != nil {
		t.Fatalf("invalid arguments must not fail the run, got %+v", end)
	}
	if got := c.callCount(); got < 2 {
		t.Errorf("model was consulted %d time(s); it should get the chance to correct itself", got)
	}
}

// Valid arguments must pass through untouched, or validation would be a new way
// for correct calls to fail.
func TestValidArgumentsStillReachTheTool(t *testing.T) {
	tool := &schemaTool{fakeTool{name: "probe"}}
	c := &fakeClient{responses: []llm.Response{{
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
			Type: llm.BlockToolUse, ID: "call0", Name: "probe",
			Input: json.RawMessage(`{"path":"a.go","extra":1}`),
		}}},
		StopReason: llm.StopToolUse,
	}}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool)})

	drain(a.Run(context.Background(), "go"))

	args := tool.callArgs()
	if len(args) != 1 {
		t.Fatalf("tool ran %d time(s), want 1", len(args))
	}
	// Unknown fields are tolerated and passed through rather than stripped.
	if !strings.Contains(args[0], `"extra"`) {
		t.Errorf("arguments were rewritten: %q", args[0])
	}
}

// streamingTool reports output before it finishes, the way bash does.
type streamingTool struct {
	fakeTool
	chunks []string
}

func (s *streamingTool) ExecuteStreaming(
	ctx context.Context, args json.RawMessage, onPartial func(tools.Partial),
) (tools.Result, error) {
	for _, c := range s.chunks {
		if onPartial != nil {
			onPartial(tools.Partial{Text: c})
		}
	}
	return s.fakeTool.Execute(ctx, args)
}

func (s *streamingTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	return s.ExecuteStreaming(ctx, args, nil)
}

func TestStreamingToolOutputReachesTheEventStream(t *testing.T) {
	tool := &streamingTool{fakeTool{name: "runner"}, []string{"step 1\n", "step 2\n"}}
	c := &fakeClient{responses: []llm.Response{toolCalls("runner")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool)})

	events := drain(a.Run(context.Background(), "go"))

	var partials []string
	var endedAt, lastPartialAt int
	for i, e := range events {
		switch e.Kind {
		case EventToolPartial:
			partials = append(partials, e.Text)
			lastPartialAt = i
			if e.ToolCallID != "call0" || e.ToolName != "runner" {
				t.Errorf("fragment not attributed: %+v", e)
			}
		case EventToolEnd:
			endedAt = i
		}
	}
	if strings.Join(partials, "") != "step 1\nstep 2\n" {
		t.Errorf("fragments = %q", partials)
	}
	// Every fragment must precede the end of the call it belongs to, or a consumer
	// folding them into a running call would have nowhere to put them.
	if lastPartialAt > endedAt {
		t.Errorf("a fragment arrived after tool_end (%d > %d)", lastPartialAt, endedAt)
	}

	// The settled output still arrives, so a consumer that ignored every fragment
	// ends up correct anyway. That is what makes them safe to drop.
	var result string
	for _, m := range a.Messages() {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult {
				result = b.Text
			}
		}
	}
	if result != "ok:runner" {
		t.Errorf("tool result = %q", result)
	}
}

// Argument fragments stream while the model is still generating the call, so a
// UI can preview a long write instead of sitting silent. Every fragment must
// precede tool_start — which only fires after review — and none may follow it.
func TestToolArgFragmentsStreamAheadOfToolStart(t *testing.T) {
	tool := &fakeTool{name: "write"}
	c := &fakeClient{
		responses: []llm.Response{toolCalls("write")},
		deltas: func(n int, emit func(llm.Delta)) {
			if n != 1 {
				return // only the tool-call turn streams fragments
			}
			emit(llm.Delta{Kind: llm.DeltaToolCallStart, ToolID: "call0", ToolName: "write"})
			emit(llm.Delta{Kind: llm.DeltaToolCallArgs, ToolID: "call0", Text: `{"path":"a.go",`})
			emit(llm.Delta{Kind: llm.DeltaToolCallArgs, ToolID: "call0", Text: `"content":"hi"}`})
		},
	}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool)})

	events := drain(a.Run(context.Background(), "go"))

	var argsAt []int
	nameFirst := false
	startAt := -1
	for i, e := range events {
		switch e.Kind {
		case EventToolArgs:
			if e.ToolCallID != "call0" {
				t.Errorf("fragment not attributed: %+v", e)
			}
			if len(argsAt) == 0 {
				nameFirst = e.ToolName == "write" && e.Text == ""
			}
			argsAt = append(argsAt, i)
		case EventToolStart:
			startAt = i
		}
	}
	if len(argsAt) != 3 {
		t.Fatalf("tool_args events = %d, want 3 (name + 2 fragments)", len(argsAt))
	}
	if !nameFirst {
		t.Error("the first tool_args event did not carry the tool name")
	}
	if startAt < 0 {
		t.Fatal("the call never started")
	}
	if argsAt[len(argsAt)-1] > startAt {
		t.Errorf("a fragment arrived after tool_start (%d > %d)", argsAt[len(argsAt)-1], startAt)
	}
}

// A tool that does not implement StreamingTool must be called exactly as before.
// Six of the seven built-ins are in this case, so a regression here would be
// broad and quiet.
func TestNonStreamingToolProducesNoFragments(t *testing.T) {
	tool := &fakeTool{name: "plain"}
	c := &fakeClient{responses: []llm.Response{toolCalls("plain")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool)})

	for _, e := range drain(a.Run(context.Background(), "go")) {
		if e.Kind == EventToolPartial {
			t.Fatalf("a non-streaming tool produced a fragment: %+v", e)
		}
	}
	if got := len(tool.callArgs()); got != 1 {
		t.Errorf("tool ran %d time(s), want 1", got)
	}
}

// Fragments from a parallel batch must stay attributable to their own call, since
// they arrive interleaved from several goroutines.
func TestFragmentsFromAParallelBatchStayAttributed(t *testing.T) {
	tool := &streamingTool{fakeTool{name: "runner"}, []string{"a", "b"}}
	c := &fakeClient{responses: []llm.Response{toolCalls("runner", "runner", "runner")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool)})

	perCall := map[string]string{}
	for _, e := range drain(a.Run(context.Background(), "go")) {
		if e.Kind == EventToolPartial {
			perCall[e.ToolCallID] += e.Text
		}
	}
	if len(perCall) != 3 {
		t.Fatalf("got fragments for %d calls, want 3: %v", len(perCall), perCall)
	}
	for id, text := range perCall {
		if text != "ab" {
			t.Errorf("call %s accumulated %q, want \"ab\"", id, text)
		}
	}
}
