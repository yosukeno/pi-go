package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func toolNamed(t *testing.T, name string) Tool {
	t.Helper()
	tool, ok := Default(t.TempDir()).Get(name)
	if !ok {
		t.Fatalf("no tool named %q", name)
	}
	return tool
}

func validate(t *testing.T, name, args string) error {
	t.Helper()
	return ValidateArgs(toolNamed(t, name), json.RawMessage(args))
}

// The message has to name the field the tool actually wants, because that is the
// only thing the model can act on. Before this, an absent field produced no error
// at all: it arrived as a zero value and the tool failed later for a reason that
// looked unrelated.
func TestMissingRequiredFieldIsNamed(t *testing.T) {
	cases := []struct{ tool, args, want string }{
		{"read", `{}`, `missing required field "path"`},
		{"bash", `{}`, `missing required field "command"`},
		{"write", `{"path":"a.go"}`, `missing required field "content"`},
		{"grep", `{"path":"."}`, `missing required field "pattern"`},
		{"find", `{}`, `missing required field "pattern"`},
	}
	for _, c := range cases {
		err := validate(t, c.tool, c.args)
		if err == nil {
			t.Errorf("%s%s: expected an error", c.tool, c.args)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %q, want it to contain %q", c.tool, err, c.want)
		}
		// The field list lets the model correct itself from the error alone rather
		// than guessing or asking to see the schema again.
		if !strings.Contains(err.Error(), "Expected:") {
			t.Errorf("%s: message does not list the accepted fields: %q", c.tool, err)
		}
	}
}

// A model that sends file_path needs to be told which name this tool uses. Saying
// only "path is missing" leaves it to guess that its own key was the problem.
func TestAMisspelledFieldIsPointedOut(t *testing.T) {
	for _, sent := range []string{"file_path", "filePath", "Path", "filepath"} {
		err := validate(t, "read", `{"`+sent+`":"a.go"}`)
		if err == nil {
			t.Fatalf("%s: expected an error", sent)
		}
		if !strings.Contains(err.Error(), sent) {
			t.Errorf("%s: message does not mention what was received: %q", sent, err)
		}
	}

	// A near-match is called out as the likely culprit.
	if err := validate(t, "read", `{"file_path":"a.go"}`); err == nil ||
		!strings.Contains(err.Error(), `received "file_path"`) {
		t.Errorf("a near-match should be named as the culprit: %v", err)
	}

	// An unrelated key is reported neutrally rather than blamed. It is still worth
	// naming — "cmd" for "command" is a real mistake no cheap similarity test
	// catches — but the message must not claim it was meant to be the missing one.
	err := validate(t, "read", `{"reason":"because"}`)
	if err == nil {
		t.Fatal("expected an error about the missing path")
	}
	if !strings.Contains(err.Error(), "unrecognised field(s) received: reason") {
		t.Errorf("unrecognised fields should be listed: %q", err)
	}
	if strings.Contains(err.Error(), `received "reason"`) {
		t.Errorf("an unrelated field was blamed as the intended one: %q", err)
	}

	// The abbreviation case, which is why the neutral branch exists.
	if err := validate(t, "bash", `{"cmd":"ls"}`); err == nil ||
		!strings.Contains(err.Error(), "cmd") {
		t.Errorf("bash with cmd should mention what arrived: %v", err)
	}
}

func TestWrongTypeSaysWhatWasWantedAndWhatArrived(t *testing.T) {
	err := validate(t, "read", `{"path":123}`)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{`"path"`, "must be a string", "got number"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message missing %q: %q", want, err)
		}
	}
	// No Go internals. This is the exact failure mode being replaced.
	for _, leak := range []string{"json:", "Go struct", "readArgs"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("message leaks implementation detail %q: %q", leak, err)
		}
	}

	if err := validate(t, "read", `{"path":"a.go","limit":"20"}`); err == nil {
		t.Error("a numeric string for a number field should be reported")
	} else if !strings.Contains(err.Error(), "must be a number, got string") {
		t.Errorf("got %q", err)
	}
}

// Extra keys are how models routinely behave and are harmless. Rejecting them
// would spend a turn to gain nothing.
func TestUnknownFieldsAreTolerated(t *testing.T) {
	if err := validate(t, "read", `{"path":"a.go","thoughts":"looks fine","depth":3}`); err != nil {
		t.Errorf("unknown fields should be tolerated: %v", err)
	}
}

// A tool with no required fields is legitimately called with nothing at all.
func TestNoArgumentsIsFineWhenNothingIsRequired(t *testing.T) {
	for _, args := range []string{"", "{}", "   "} {
		if err := validate(t, "ls", args); err != nil {
			t.Errorf("ls with %q: %v", args, err)
		}
	}
	if err := validate(t, "read", ""); err == nil {
		t.Error("read still needs a path when called with nothing")
	}
}

func TestNonObjectArgumentsAreRejectedPlainly(t *testing.T) {
	err := validate(t, "read", `["a.go"]`)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "must be a JSON object") || !strings.Contains(err.Error(), "got array") {
		t.Errorf("got %q", err)
	}
}

// edit's nested items schema must be checked too, and the failing element
// identified by index — "one of the edits is wrong" is not actionable.
func TestNestedArrayElementsAreValidatedByIndex(t *testing.T) {
	// A plain object schema with an items array, standing in for any future tool
	// that nests without edit's opt-out.
	schema := object([]string{"path", "edits"}, map[string]any{
		"path": prop("string", "path"),
		"edits": map[string]any{
			"type": "array",
			"items": object([]string{"oldText", "newText"}, map[string]any{
				"oldText": prop("string", "old"),
				"newText": prop("string", "new"),
			}),
		},
	})
	args := json.RawMessage(`{"path":"a.go","edits":[{"oldText":"a","newText":"b"},{"oldText":"c"}]}`)
	err := validateAgainst(schema, args, "")
	if err == nil {
		t.Fatal("expected an error about the second edit")
	}
	if !strings.Contains(err.Error(), "edits[1].newText") {
		t.Errorf("the failing element is not identified: %q", err)
	}
}

// edit opts out because its schema does not describe the flat form. The opt-out
// must keep that leniency working while still catching real mistakes.
func TestEditKeepsItsFlatFormButStillValidates(t *testing.T) {
	edit := toolNamed(t, "edit")

	flat := json.RawMessage(`{"path":"a.go","oldText":"x","newText":"y"}`)
	if err := ValidateArgs(edit, flat); err != nil {
		t.Errorf("the flat single-edit form must stay accepted: %v", err)
	}
	nested := json.RawMessage(`{"path":"a.go","edits":[{"oldText":"x","newText":"y"}]}`)
	if err := ValidateArgs(edit, nested); err != nil {
		t.Errorf("the documented form must be accepted: %v", err)
	}

	for _, c := range []struct{ args, want string }{
		{`{"edits":[{"oldText":"x","newText":"y"}]}`, `missing required field "path"`},
		{`{"path":"a.go"}`, `missing required field "edits"`},
		{`{"path":"a.go","oldText":"x"}`, `missing required field "edits"`},
		{`{"path":"a.go","edits":[{"oldText":"","newText":"y"}]}`, `edits[0].oldText must not be empty`},
	} {
		err := ValidateArgs(edit, json.RawMessage(c.args))
		if err == nil {
			t.Errorf("%s: expected an error", c.args)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %q, want %q", c.args, err, c.want)
		}
	}
}

// Every built-in must either validate against its own schema or say why not. A
// tool whose valid arguments its own schema rejects is a trap for the model.
func TestEveryToolAcceptsArgumentsMatchingItsSchema(t *testing.T) {
	valid := map[string]string{
		"read":  `{"path":"a.go"}`,
		"ls":    `{"path":"."}`,
		"find":  `{"pattern":"*.go"}`,
		"grep":  `{"pattern":"x","include":"*.go"}`,
		"write": `{"path":"a.go","content":"x"}`,
		"edit":  `{"path":"a.go","edits":[{"oldText":"a","newText":"b"}]}`,
		"bash":  `{"command":"ls","timeout":10}`,
	}
	for _, tool := range Default(t.TempDir()).All() {
		args, ok := valid[tool.Name()]
		if !ok {
			t.Errorf("%s has no sample arguments here; add one when adding a tool", tool.Name())
			continue
		}
		if err := ValidateArgs(tool, json.RawMessage(args)); err != nil {
			t.Errorf("%s rejects arguments that match its schema: %v", tool.Name(), err)
		}
	}
}

func TestJSONKindNamesTypesTheSchemaWay(t *testing.T) {
	cases := map[string]string{
		`"x"`: "string", `12`: "number", `-1.5`: "number", `true`: "boolean",
		`false`: "boolean", `null`: "null", `{}`: "object", `[]`: "array", ``: "null",
	}
	for raw, want := range cases {
		if got := jsonKind(json.RawMessage(raw)); got != want {
			t.Errorf("jsonKind(%q) = %q, want %q", raw, got, want)
		}
	}
}

// A closed set of values is the case where the error can just say what the
// alternatives are. The subagent tool is the first user: a model that sends
// "readonly" instead of "explore" should learn both names from one message.
func TestValidateArgsChecksEnums(t *testing.T) {
	sub := &Subagent{}

	err := ValidateArgs(sub, json.RawMessage(`{"task":"x","mode":"readonly"}`))
	if err == nil {
		t.Fatal("ValidateArgs accepted a mode outside the enum")
	}
	for _, want := range []string{`"explore"`, `"edit"`, `"readonly"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %s", err, want)
		}
	}

	for _, mode := range []string{ModeExplore, ModeEdit} {
		args := json.RawMessage(`{"task":"x","mode":"` + mode + `"}`)
		if err := ValidateArgs(sub, args); err != nil {
			t.Errorf("ValidateArgs rejected the valid mode %q: %v", mode, err)
		}
	}

	// An absent enum field is the required check's business, not this one's, and the
	// message should be about what is missing rather than about a value it never got.
	err = ValidateArgs(sub, json.RawMessage(`{"task":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "missing required field") {
		t.Errorf("error for an absent mode = %v, want a missing-field message", err)
	}

	// A field with no enum in its schema is unaffected: this must not become a
	// check that every string has to opt out of.
	if err := ValidateArgs(&Read{Cwd: "."}, json.RawMessage(`{"path":"anything.txt"}`)); err != nil {
		t.Errorf("ValidateArgs rejected a plain string field: %v", err)
	}
}
