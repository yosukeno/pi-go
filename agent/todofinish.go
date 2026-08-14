package agent

import (
	"encoding/json"
	"fmt"

	"github.com/yosukeno/pi-go/llm"
	"github.com/yosukeno/pi-go/tools"
)

// Closing out the task list before the run ends.
//
// The todo tool holds no state: the newest todo tool_use in the transcript *is*
// the plan, for the model, for the terminal and for the web bar. That works while
// the run is going and fails at exactly one point — the end. Models update the
// list when they start a step and then, having finished the last one, answer
// instead of writing the list a final time. The plan is then permanently one
// short: an interface reads "3/4" with the last item still in_progress on a run
// that finished cleanly, and the honest reading of that ("it stopped halfway") is
// wrong.
//
// The list cannot be repaired downstream. A UI that ticked the last item when the
// run ended would be inventing state — the item might genuinely have been
// abandoned, and cancelled and blocked are real outcomes the model is the only one
// who can tell apart. So the fix is to ask, once, at the one moment the answer is
// known: the model has just decided it is finished, and its own list disagrees.
//
// Shaped like softcap.go's checkpoint and for the same reasons: one user-role
// message at a legal boundary, carried on the steering path so both front ends
// already know how to show it, and marked [pi-go] so it is never mistaken for
// something the user typed. There is no parser on the reply — a todo call
// followed by an answer is the intended path, and a model that answers again
// without calling it ends the run exactly as it would have.
//
// Once per run, tracked by Agent.todoReminded, which is what stops it becoming a
// loop with a model that will not settle its list.

// The tool's name is todoToolName, declared in contextedit.go — that file needs
// it for the same underlying reason this one does: the task list is state, not an
// event, so both have to recognise it by name.
//
// It is checked against the schemas rather than the registry, because the schemas
// are the set the model was actually told about: a subagent child has the tool
// withheld, and reminding it to write a list it cannot write would be a wasted
// turn.

// todoFinishNotice returns the reminder to inject when the model is ending a run
// with its own task list unfinished, and false when there is nothing to say —
// no list, a settled one, the tool not registered, or the reminder already spent.
func (a *Agent) todoFinishNotice() (string, bool) {
	if a.todoReminded || !a.hasSchema(todoToolName) {
		return "", false
	}
	todos, ok := newestTodoList(a.messages)
	if !ok || len(todos) == 0 {
		return "", false
	}
	var running, pending int
	for _, it := range todos {
		switch it.Status {
		case tools.TodoInProgress:
			running++
		case tools.TodoPending:
			pending++
		}
	}
	if running == 0 && pending == 0 {
		return "", false
	}
	return fmt.Sprintf("[pi-go] Your task list still has %d item(s) in progress and %d pending, "+
		"but you are about to finish. Call todo once with the whole list and settle every item: "+
		"completed for the work that is really done, cancelled for what the plan dropped, blocked "+
		"for anything that failed or is stuck. Nothing may be left in_progress. Then give your "+
		"final answer — do not redo work that is already done.\n\nThe list as it stands:\n%s",
		running, pending, tools.RenderTodos(todos)), true
}

// hasSchema reports whether a tool of this name was declared to the model.
func (a *Agent) hasSchema(name string) bool {
	for _, s := range a.schemas {
		if s.Name == name {
			return true
		}
	}
	return false
}

// newestTodoList is the plan as it stands: the arguments of the last successful
// todo call in the history.
//
// It reads the call rather than its result text because the arguments are already
// structured — the result is RenderTodos' numbered lines, and parsing those back
// would be a second, looser copy of the same format. Failed calls are skipped:
// a rejected list (two items in_progress, say) never became the state, so the
// good list before it is still the plan. This is the same rule the web timeline
// applies in markSupersededTodos, deliberately.
func newestTodoList(msgs []llm.Message) ([]tools.TodoItem, bool) {
	failed := make(map[string]bool)
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult && b.IsError {
				failed[b.ToolUseID] = true
			}
		}
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != llm.RoleAssistant {
			continue
		}
		content := msgs[i].Content
		for j := len(content) - 1; j >= 0; j-- {
			b := content[j]
			if b.Type != llm.BlockToolUse || b.Name != todoToolName || failed[b.ID] {
				continue
			}
			var args struct {
				Todos []tools.TodoItem `json:"todos"`
			}
			if err := json.Unmarshal(b.Input, &args); err != nil {
				continue
			}
			return args.Todos, true
		}
	}
	return nil, false
}
