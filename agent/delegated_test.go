package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yosukeno/pi-go/llm"
	"github.com/yosukeno/pi-go/tools"
)

// delegatingTool reports tokens spent in another process, the way the subagent tool
// does.
type delegatingTool struct {
	name  string
	usage tools.SubagentDetails
}

func (d *delegatingTool) Name() string                       { return d.name }
func (d *delegatingTool) Description() string                { return "spends tokens elsewhere" }
func (d *delegatingTool) InputSchema() map[string]any        { return map[string]any{"type": "object"} }
func (d *delegatingTool) ExecutionMode() tools.ExecutionMode { return tools.Parallel }
func (d *delegatingTool) Execute(context.Context, json.RawMessage) (tools.Result, error) {
	return tools.Result{Text: "delegated", Details: d.usage}, nil
}

// A subagent's tokens are the parent's tokens: they were spent on the parent's
// behalf, and a budget that ignored them would bound only the work the agent chose
// not to delegate.
//
// Run under -race in CI: the accounting deliberately happens after the batch, on the
// run goroutine, because the usage counters have a single writer. Adding to them
// from a tool goroutine is the race this arrangement avoids.
func TestDelegatedUsageIsAddedToTheRunTotal(t *testing.T) {
	tool := &delegatingTool{name: "sub", usage: tools.SubagentDetails{InputTok: 1000, OutTok: 50}}
	c := &fakeClient{responses: []llm.Response{
		// Two calls in one batch, so the parallel path is the one measured.
		toolCalls("sub", "sub"),
	}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool)})

	drain(a.Run(context.Background(), "delegate twice"))

	u := a.Usage()
	if u.Input != 2000 || u.Output != 100 {
		t.Errorf("Usage() = in %d out %d, want the two children's 2000/100 folded in",
			u.Input, u.Output)
	}
}

// A tool with no delegated usage must not disturb the totals, which is what keeps
// this mechanism invisible to the other seven tools.
func TestOrdinaryToolsAddNothing(t *testing.T) {
	tool := &fakeTool{name: "t"}
	c := &fakeClient{responses: []llm.Response{toolCalls("t")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool)})

	drain(a.Run(context.Background(), "go"))

	if u := a.Usage(); u.Input != 0 || u.Output != 0 {
		t.Errorf("Usage() = %+v, want nothing added by a plain tool", u)
	}
}

// Tokens spent before a failure still count. A subagent that crashed after four
// turns spent those turns, and ignoring them would be wrong in the direction that
// costs money.
func TestDelegatedUsageCountsEvenWhenTheToolFails(t *testing.T) {
	tool := &failingDelegate{usage: tools.SubagentDetails{InputTok: 700, OutTok: 20}}
	c := &fakeClient{responses: []llm.Response{toolCalls("sub")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool)})

	drain(a.Run(context.Background(), "delegate and fail"))

	if u := a.Usage(); u.Input != 700 || u.Output != 20 {
		t.Errorf("Usage() = in %d out %d, want the failed child's 700/20", u.Input, u.Output)
	}
}

type failingDelegate struct {
	usage tools.SubagentDetails
}

func (f *failingDelegate) Name() string                { return "sub" }
func (f *failingDelegate) Description() string         { return "fails after spending" }
func (f *failingDelegate) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (f *failingDelegate) ExecutionMode() tools.ExecutionMode {
	return tools.Parallel
}
func (f *failingDelegate) Execute(context.Context, json.RawMessage) (tools.Result, error) {
	return tools.Result{Details: f.usage}, errTooLate
}

var errTooLate = &delegateError{}

type delegateError struct{}

func (*delegateError) Error() string { return "the subagent exited with code 3" }
