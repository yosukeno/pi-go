package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yosukeno/pi-go/llm"
	"github.com/yosukeno/pi-go/tools"
)

// todoTurn is an assistant response that writes a task list.
func todoTurn(items string) llm.Response {
	return llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{
			Type:  llm.BlockToolUse,
			ID:    "todo1",
			Name:  "todo",
			Input: json.RawMessage(`{"todos":` + items + `}`),
		}}},
		StopReason: llm.StopToolUse,
	}
}

func answerTurn(text string) llm.Response {
	return llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: text}}},
		StopReason: llm.StopEndTurn,
	}
}

func steerTexts(events []Event) []string {
	var out []string
	for _, e := range events {
		if e.Kind == EventSteer {
			out = append(out, e.Text)
		}
	}
	return out
}

// The case this exists for: the model works through its list, finishes the last
// step, and answers without writing the list again. The run would otherwise end
// with an item still in_progress, which every interface reads — correctly, given
// what it is told — as a run that stopped halfway.
func TestUnfinishedTodoListGetsOneReminderBeforeTheRunEnds(t *testing.T) {
	c := &fakeClient{responses: []llm.Response{
		todoTurn(`[{"task":"a","status":"completed"},{"task":"b","status":"in_progress"}]`),
		answerTurn("all done"),
	}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(&tools.Todo{})})

	events := drain(a.Run(context.Background(), "go"))

	steers := steerTexts(events)
	if len(steers) != 1 {
		t.Fatalf("got %d steering messages, want exactly 1: %q", len(steers), steers)
	}
	// The count is what makes the message actionable rather than a nag, and the
	// prefix is what marks it as the harness talking rather than the user.
	for _, want := range []string{"[pi-go]", "1 item(s) in progress", "in_progress"} {
		if !strings.Contains(steers[0], want) {
			t.Errorf("reminder does not mention %q: %s", want, steers[0])
		}
	}
	// Three calls: the list, the premature answer, and the turn the reminder
	// bought. Any more would mean the reminder fired twice.
	if got := c.callCount(); got != 3 {
		t.Errorf("model calls = %d, want 3", got)
	}
}

// A settled list is the common case and must cost nothing: no extra turn, no
// message in the transcript.
func TestSettledTodoListEndsTheRunImmediately(t *testing.T) {
	for _, tc := range []struct {
		name  string
		items string
	}{
		{"all completed", `[{"task":"a","status":"completed"},{"task":"b","status":"completed"}]`},
		// Cancelled and blocked are outcomes, not unfinished work: the plan dropped
		// one item and one failed, and asking the model to settle either again would
		// be asking it to change an answer it already gave.
		{"cancelled counts as settled", `[{"task":"a","status":"completed"},{"task":"b","status":"cancelled"}]`},
		{"blocked counts as settled", `[{"task":"a","status":"blocked"}]`},
		{"cleared list", `[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeClient{responses: []llm.Response{todoTurn(tc.items), answerTurn("done")}}
			a := New(Config{Client: c, Registry: tools.NewRegistry(&tools.Todo{})})

			events := drain(a.Run(context.Background(), "go"))

			if steers := steerTexts(events); len(steers) != 0 {
				t.Errorf("got a reminder for a settled list: %q", steers)
			}
			if got := c.callCount(); got != 2 {
				t.Errorf("model calls = %d, want 2", got)
			}
		})
	}
}

// A run that never wrote a list has nothing to settle, and a subagent child has
// the tool withheld — reminding either would spend a turn asking for something
// that cannot happen.
func TestNoTodoListMeansNoReminder(t *testing.T) {
	c := &fakeClient{responses: []llm.Response{answerTurn("done")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(&tools.Todo{})})

	if steers := steerTexts(drain(a.Run(context.Background(), "go"))); len(steers) != 0 {
		t.Errorf("got a reminder with no list in the transcript: %q", steers)
	}
}

func TestReminderIsSkippedWhenTheToolIsNotRegistered(t *testing.T) {
	a := New(Config{Client: &fakeClient{}, Registry: tools.NewRegistry(&fakeTool{name: "noop"})})
	a.messages = []llm.Message{todoTurn(`[{"task":"a","status":"in_progress"}]`).Message}

	if notice, ok := a.todoFinishNotice(); ok {
		t.Errorf("notice for an agent without the todo tool: %q", notice)
	}
}

// A rejected write never became the state — the list it carried was refused
// before it ran — so the good list before it is still the plan. Getting this
// wrong would mean reminding the model about items it had already settled.
func TestNewestTodoListIgnoresFailedWrites(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.Block{{
			Type: llm.BlockToolUse, ID: "good", Name: "todo",
			Input: json.RawMessage(`{"todos":[{"task":"kept","status":"completed"}]}`),
		}}},
		{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "good", Text: "ok"}}},
		{Role: llm.RoleAssistant, Content: []llm.Block{{
			Type: llm.BlockToolUse, ID: "bad", Name: "todo",
			Input: json.RawMessage(`{"todos":[{"task":"rejected","status":"in_progress"}]}`),
		}}},
		{Role: llm.RoleUser, Content: []llm.Block{{
			Type: llm.BlockToolResult, ToolUseID: "bad", IsError: true, Text: "at most one task may be in_progress",
		}}},
	}

	todos, ok := newestTodoList(msgs)
	if !ok || len(todos) != 1 || todos[0].Task != "kept" {
		t.Fatalf("newestTodoList = %+v (ok=%v), want the list before the rejected write", todos, ok)
	}
}
