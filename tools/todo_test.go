package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// The rule the whole tool exists to enforce, and the one a JSON Schema cannot
// state. Asserted through ValidateArgs rather than Execute because the layer is
// the point: validation runs before the approval gate, so a list with two things
// in flight cannot be approved into existence.
func TestTodoRefusesTwoInProgress(t *testing.T) {
	err := (&Todo{}).ValidateArgs(json.RawMessage(`{"todos":[
		{"task":"read the file","status":"in_progress"},
		{"task":"run the tests","status":"pending"},
		{"task":"update the docs","status":"in_progress"}
	]}`))
	if err == nil {
		t.Fatal("two in_progress items were accepted")
	}
	// The indices are 1-based and must name the offending lines, because that is
	// the difference between a message the model can act on and one it has to
	// guess at. They match how RenderTodos numbers the list.
	for _, want := range []string{"1", "3", "in_progress"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "2") {
		t.Errorf("error blames item 2, which is pending: %v", err)
	}
}

func TestTodoAcceptsOneInProgress(t *testing.T) {
	err := (&Todo{}).ValidateArgs(json.RawMessage(`{"todos":[
		{"task":"read the file","status":"completed"},
		{"task":"run the tests","status":"in_progress"},
		{"task":"update the docs","status":"pending"}
	]}`))
	if err != nil {
		t.Fatalf("a well-formed list was rejected: %v", err)
	}
}

// Zero in_progress is legal: a list where everything is done, or one written
// before starting, is not an error. "Exactly one" is a rule for the model's own
// discipline, and enforcing it here would refuse the final update that marks the
// last item completed.
func TestTodoAcceptsNoneInProgress(t *testing.T) {
	for _, body := range []string{
		`{"todos":[{"task":"a","status":"completed"},{"task":"b","status":"completed"}]}`,
		`{"todos":[{"task":"a","status":"pending"},{"task":"b","status":"pending"}]}`,
	} {
		if err := (&Todo{}).ValidateArgs(json.RawMessage(body)); err != nil {
			t.Errorf("%s was rejected: %v", body, err)
		}
	}
}

// The generic checks must still apply. Delegating to validateAgainst rather than
// hand-writing them is what keeps a bad status reported in the same words every
// other tool's bad enum is.
func TestTodoStillGetsSchemaValidation(t *testing.T) {
	cases := map[string]string{
		"unknown status":     `{"todos":[{"task":"a","status":"doing"}]}`,
		"missing status":     `{"todos":[{"task":"a"}]}`,
		"missing task":       `{"todos":[{"status":"pending"}]}`,
		"missing todos":      `{}`,
		"todos not an array": `{"todos":"a, b"}`,
	}
	for name, body := range cases {
		if err := (&Todo{}).ValidateArgs(json.RawMessage(body)); err == nil {
			t.Errorf("%s: accepted %s", name, body)
		}
	}

	// The enum message should list the alternatives: a model that sent "doing" is
	// one word away from correct and should not need another turn to find out
	// which word.
	err := (&Todo{}).ValidateArgs(json.RawMessage(`{"todos":[{"task":"a","status":"doing"}]}`))
	if err == nil || !strings.Contains(err.Error(), TodoInProgress) {
		t.Errorf("enum error does not offer the valid values: %v", err)
	}
	// And it must point at the element, not just at "todos".
	if err != nil && !strings.Contains(err.Error(), "todos[0].status") {
		t.Errorf("enum error does not name the offending element: %v", err)
	}
}

func TestTodoRejectsEmptyTaskText(t *testing.T) {
	err := (&Todo{}).ValidateArgs(json.RawMessage(`{"todos":[{"task":"   ","status":"pending"}]}`))
	if err == nil {
		t.Fatal("a whitespace-only task was accepted")
	}
	if !strings.Contains(err.Error(), "todos[0].task") {
		t.Errorf("error does not name the offending element: %v", err)
	}
}

// Wholesale replacement is the contract, so the tool has to be a pure function of
// its arguments: the same call twice produces the same result, and a shorter list
// does not merge into a longer one it never saw.
func TestTodoReplacesRatherThanMerges(t *testing.T) {
	long := `{"todos":[{"task":"a","status":"completed"},{"task":"b","status":"completed"},{"task":"c","status":"pending"}]}`
	short := `{"todos":[{"task":"c","status":"in_progress"}]}`

	first := runTodo(t, long)
	if got := len(first.Todos); got != 3 {
		t.Fatalf("first call: %d items, want 3", got)
	}
	second := runTodo(t, short)
	if got := len(second.Todos); got != 1 {
		t.Fatalf("second call: %d items, want 1 — state leaked between calls", got)
	}
	if second.Todos[0].Task != "c" || second.Todos[0].Status != TodoInProgress {
		t.Errorf("second call: %+v", second.Todos[0])
	}
	// Idempotent: replaying the first call must not be affected by the second.
	if again := runTodo(t, long); len(again.Todos) != 3 {
		t.Errorf("replaying the first call gave %d items, want 3", len(again.Todos))
	}
}

// An empty array clears the list rather than failing. It is the only way to say
// "there is no plan any more", and refusing it would leave a stale plan on record
// with no way to retract it.
func TestTodoEmptyListClears(t *testing.T) {
	res, err := (&Todo{}).Execute(t.Context(), json.RawMessage(`{"todos":[]}`))
	if err != nil {
		t.Fatalf("clearing was rejected: %v", err)
	}
	if !strings.Contains(strings.ToLower(res.Text), "clear") {
		t.Errorf("text does not say the list was cleared: %q", res.Text)
	}
	d, ok := res.Details.(TodoDetails)
	if !ok || len(d.Todos) != 0 {
		t.Errorf("details = %+v, want an empty list", res.Details)
	}
}

// The text is the state: the newest of these results is what a later turn reads
// to find out where the work stands, so it has to carry the whole list and not an
// acknowledgement. Numbering is what the validation messages point at.
func TestTodoTextCarriesTheWholeList(t *testing.T) {
	res, err := (&Todo{}).Execute(t.Context(), json.RawMessage(`{"todos":[
		{"task":"read config.go","status":"completed"},
		{"task":"change the timeout","status":"in_progress"},
		{"task":"run go test","status":"pending"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"1. [completed] read config.go",
		"2. [in_progress] change the timeout",
		"3. [pending] run go test",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("text is missing %q:\n%s", want, res.Text)
		}
	}
}

// A task with newlines in it would break the numbering the validation messages
// refer to, so it is folded rather than rejected: the content is fine, only its
// shape is wrong, and refusing it would cost a turn over whitespace.
func TestTodoFoldsMultilineTask(t *testing.T) {
	res, err := (&Todo{}).Execute(t.Context(),
		json.RawMessage(`{"todos":[{"task":"first line\nsecond line","status":"pending"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(res.Text, "\n") != 1 { // the one between the header and the item
		t.Errorf("a multiline task produced extra lines:\n%q", res.Text)
	}
	if !strings.Contains(res.Text, "first line second line") {
		t.Errorf("task text was not folded onto one line: %q", res.Text)
	}
}

func TestTodoRejectsAbsurdlyLongList(t *testing.T) {
	var items []string
	for range MaxTodoItems + 1 {
		items = append(items, `{"task":"x","status":"pending"}`)
	}
	body := `{"todos":[` + strings.Join(items, ",") + `]}`
	if err := (&Todo{}).ValidateArgs(json.RawMessage(body)); err == nil {
		t.Fatalf("a list of %d items was accepted", MaxTodoItems+1)
	}
}

// CurrentTodo is what a status line shows, so it has to pick the in-progress item
// and not merely the first one.
func TestCurrentTodo(t *testing.T) {
	todos := []TodoItem{
		{Task: "a", Status: TodoCompleted},
		{Task: "b", Status: TodoInProgress},
		{Task: "c", Status: TodoPending},
	}
	if got := CurrentTodo(todos); got != "b" {
		t.Errorf("CurrentTodo = %q, want \"b\"", got)
	}
	if got := CurrentTodo([]TodoItem{{Task: "a", Status: TodoPending}}); got != "" {
		t.Errorf("CurrentTodo with nothing running = %q, want \"\"", got)
	}
}

// The hand-written schema is the single source of truth, and it must describe the
// items — GenerateSchema renders a slice as a bare "array", which would silently
// drop the per-item required fields and the status enum. ValidateArgs prefers
// Schema() over InputSchema(), so implementing SchemaProvider here would disable
// exactly the checks above.
func TestTodoDoesNotImplementSchemaProvider(t *testing.T) {
	if _, ok := any(&Todo{}).(SchemaProvider); ok {
		t.Fatal("Todo implements SchemaProvider: the reflected schema has no items, " +
			"so per-item validation and the status enum would be lost")
	}
	items, ok := (&Todo{}).InputSchema()["properties"].(map[string]any)["todos"].(map[string]any)["items"].(map[string]any)
	if !ok {
		t.Fatal("todos has no items schema")
	}
	if got := requiredNames(items); len(got) != 2 {
		t.Errorf("item required = %v, want task and status", got)
	}
	// A plain map, not orderedProps: validateAgainst reads an item schema's
	// properties as map[string]any, so an ordered one would make every per-item
	// check above pass vacuously. This is the assertion that catches someone
	// "improving" the schema by ordering it.
	props, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("item properties are %T, which validateAgainst cannot read: "+
			"per-item validation would silently do nothing", items["properties"])
	}
	status, _ := props["status"].(map[string]any)
	if len(enumValues(status)) != len(todoStatusList) {
		t.Errorf("status enum = %v, want %v", status["enum"], todoStatusList)
	}
}

func runTodo(t *testing.T, body string) TodoDetails {
	t.Helper()
	res, err := (&Todo{}).Execute(t.Context(), json.RawMessage(body))
	if err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	d, ok := res.Details.(TodoDetails)
	if !ok {
		t.Fatalf("%s: details are %T, want TodoDetails", body, res.Details)
	}
	return d
}
