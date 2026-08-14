package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// The task states.
//
// Five rather than the three Claude Code uses, following what Gemini CLI settled
// on. The two extra ones are where the completion rule has to put things, and
// without them it has nowhere to put them: a task whose verification failed is
// neither done nor untouched, and a task the plan dropped is worth keeping as a
// record that it was considered rather than deleting outright.
const (
	TodoPending    = "pending"
	TodoInProgress = "in_progress"
	TodoCompleted  = "completed"
	TodoCancelled  = "cancelled"
	TodoBlocked    = "blocked"
)

// todoStatusList is the enum in schema order. Declared once so the schema, the
// validation message and the description cannot drift apart.
var todoStatusList = []string{TodoPending, TodoInProgress, TodoCompleted, TodoCancelled, TodoBlocked}

// MaxTodoItems caps one list. In house style with the other tools' caps, and for
// the same reason turned inside out: this text is resent on every subsequent
// turn, so an unbounded list is a permanent tax rather than one large result.
// A hundred items is not a plan anyway.
const MaxTodoItems = 100

// TodoItem is one entry. Task is imperative ("Run the tests"), not a status
// report, so the same string reads correctly whatever state it is in.
type TodoItem struct {
	Task   string `json:"task" required:"true" description:"What needs to be done, in the imperative: \"Run the tests\", not \"Running tests\""`
	Status string `json:"status" required:"true" enum:"pending,in_progress,completed,cancelled,blocked" description:"pending | in_progress | completed | cancelled | blocked"`
}

type todoArgs struct {
	Todos []TodoItem `json:"todos" required:"true" description:"The complete list. It replaces the previous one entirely, so include every item, not just the changed ones"`
}

// Todo records the agent's own task list for the session.
//
// It holds no state, and that is the design rather than an omission. The
// authoritative list is the newest todo tool_result in the conversation, so:
//
//   - There is nothing to lock. Tools run on batch goroutines, and Agent.mu
//     deliberately guards steering alone; a field here would be a new concurrency
//     surface for something the transcript already stores.
//   - There is nothing to persist. The list reaches the session file as an
//     ordinary message, so -resume restores it without a new record type.
//   - The prompt prefix stays stable. Each update appends rather than rewriting,
//     which is what keeps both providers' implicit prefix caches intact — neither
//     Kimi nor Zhipu exposes any way to edit a cached prefix, so a rewrite would
//     be paid for in full.
//
// The cost is that superseded lists pile up in the history. That is the eviction
// layer's job, not this tool's: only the newest one is live, and older ones are
// ordinary stale tool output.
//
// Withheld from subagent children. See tools.Options.Todo.
type Todo struct{}

func (*Todo) Name() string { return "todo" }

// Description is short on purpose.
//
// The reference implementations range from about 1400 words (Claude Code) down to
// two sentences (OpenCode's current one), and pi-go is at the far end of that
// scale by construction: the whole system prompt is around a thousand tokens, and
// tool schemas are the larger of the two fixed per-turn costs. So this says when
// to use it, when not to, and the one rule that is load-bearing — the completion
// rule — and leaves the rest to the state names.
func (*Todo) Description() string {
	return "Record and update your task list for this session, so you and the user can both " +
		"see what is done and what is left. Pass the whole list every time; it replaces the " +
		"previous one.\n\n" +
		"Use it when the work has three or more distinct steps, or the user asked for several " +
		"things. Skip it for one step, for anything trivial, and for questions.\n\n" +
		"Keep one task in_progress and update it as you go, not in a batch at the end: write the " +
		"list the moment a step finishes and the next one starts, so a reader always sees where " +
		"the work actually is. Mark a task completed only once it is really done, verification " +
		"included; if it failed or you are stuck, mark it blocked and add an item for the " +
		"blocker. A subagent reporting success is not completion on its own — an edit subagent " +
		"hands back a commit nobody has applied yet.\n\n" +
		"Settle the list before your final answer: nothing may be left in_progress once you are " +
		"finished, so the last step gets its own update rather than being left half-done."
}

// ExecutionMode is Parallel, and trivially so: the tool touches nothing. See the
// type comment for why there is no shared state to interfere with.
func (*Todo) ExecutionMode() ExecutionMode { return Parallel }

func (*Todo) InputSchema() map[string]any {
	return object([]string{"todos"}, map[string]any{
		"todos": map[string]any{
			"type": "array",
			// Terse on purpose: the description above already says the list is
			// replaced wholesale, and this text is resent on every turn for the rest
			// of the session. The only fact that is not up there is what an empty
			// array does.
			"description": "The whole list, in the order you mean to work through it. Empty clears it.",
			// object, not objectOrdered, and the difference is not cosmetic:
			// validateAgainst reads an item schema's properties as a map[string]any,
			// which orderedProps is not, so an ordered item schema would silently
			// skip every per-item check — the required fields and the status enum,
			// which is most of what there is to check here. Pinned by
			// TestTodoStillGetsSchemaValidation.
			//
			// Nothing is lost. Property order is load-bearing for write and edit
			// because path has to stream ahead of a large content blob, so a live
			// preview has something to name itself with. Both fields here are short
			// strings arriving in the same fragment; there is no blob to get ahead of.
			"items": object([]string{"task", "status"}, map[string]any{
				"task":   prop("string", "What to do, imperative: \"Run the tests\"."),
				"status": todoStatusProp(),
			}),
		},
	})
}

// todoStatusProp describes the state field. The enum carries the names, so the
// text only has to cover the two that are not self-evident from their name — and
// the one constraint, which the description states as well because being told it
// costs a few tokens while discovering it costs a turn.
func todoStatusProp() map[string]any {
	p := prop("string", "At most one in_progress. blocked = cannot proceed; cancelled = no longer needed.")
	p["enum"] = todoStatusList
	return p
}

// Schema is deliberately absent: GenerateSchema renders a slice as a bare
// "array" with no items, which would drop the per-item required fields and the
// status enum — and ValidateArgs prefers Schema() over InputSchema() when both
// exist, so implementing it here would quietly disable exactly the checks this
// tool needs most. The hand-written schema above is the single source of truth.

// ValidateArgs adds the one rule a JSON Schema cannot state: at most one task may
// be in_progress.
//
// It runs here rather than in Execute because validation happens before the
// approval gate and its failures come back as text the model corrects itself
// from. That makes "exactly one thing at a time" an enforced property instead of
// a request in the description, which is how the other implementations leave it —
// Gemini CLI is the exception, and this follows it.
//
// The generic schema check runs first and unchanged, so required fields, types
// and the status enum are still reported the way every other tool reports them.
func (t *Todo) ValidateArgs(raw json.RawMessage) error {
	if err := validateAgainst(t.InputSchema(), raw, ""); err != nil {
		return err
	}
	var a todoArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return err
	}
	if len(a.Todos) > MaxTodoItems {
		return fmt.Errorf("todos has %d items, which is more than the %d this tool accepts: "+
			"a list that long is a dump rather than a plan. Group the steps, or keep only "+
			"the ones that remain", len(a.Todos), MaxTodoItems)
	}

	var running []string
	for i, it := range a.Todos {
		if strings.TrimSpace(it.Task) == "" {
			return fmt.Errorf("todos[%d].task must not be empty: it is what the item says to do", i)
		}
		if it.Status == TodoInProgress {
			// 1-based, matching how the list is rendered back, so the model is
			// pointed at the line it can see rather than at an index it cannot.
			running = append(running, fmt.Sprintf("%d", i+1))
		}
	}
	if len(running) > 1 {
		return fmt.Errorf("at most one task may be in_progress, but items %s are all marked "+
			"in_progress. Mark the one you are actually working on now and leave the rest pending",
			strings.Join(running, ", "))
	}
	return nil
}

func (*Todo) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var a todoArgs
	if err := unmarshal(raw, &a); err != nil {
		return Result{}, err
	}
	if len(a.Todos) == 0 {
		return Result{Text: "Todo list cleared.", Details: TodoDetails{}}, nil
	}
	return Result{
		Text:    "Todo list updated:\n" + RenderTodos(a.Todos),
		Details: TodoDetails{Todos: a.Todos},
	}, nil
}

// RenderTodos formats a list the way the model reads it back.
//
// The whole list is echoed rather than an acknowledgement, because this text is
// the state: the newest of these results is what a later turn — and, after
// compaction, a later context window — reads to find out where the work stands.
// Numbered and status-prefixed so a single line identifies an item unambiguously,
// which is what the validation messages above refer to.
//
// Exported because the terminal and the compaction boundary both need to render a
// list they did not produce, and a second copy of this format is a second place
// for it to drift.
func RenderTodos(todos []TodoItem) string {
	var b strings.Builder
	for i, it := range todos {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d. [%s] %s", i+1, it.Status, oneLineTask(it.Task))
	}
	return b.String()
}

// oneLineTask keeps an item to a single line. A task containing newlines would
// otherwise break the numbering the validation messages point at.
func oneLineTask(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// CurrentTodo returns the task that is in_progress, or "" when none is. It is what
// a status line shows: of the whole list, the one line that answers "what is it
// doing".
func CurrentTodo(todos []TodoItem) string {
	for _, it := range todos {
		if it.Status == TodoInProgress {
			return oneLineTask(it.Task)
		}
	}
	return ""
}
