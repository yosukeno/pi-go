package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ArgsValidator lets a tool take over validation because it accepts argument
// shapes its own schema does not describe.
//
// This is an opt-out on purpose. Leniency is sometimes worth having — see Edit,
// which accepts a flat single-edit form models keep sending — but it should be a
// visible decision in the tool that wants it, not a hole in the check everyone
// else relies on.
type ArgsValidator interface {
	ValidateArgs(args json.RawMessage) error
}

// ValidateArgs checks a call's arguments against the tool's schema before
// anything runs.
//
// It exists because the alternative is what the model used to receive: Go's own
// unmarshal error, e.g. "json: cannot unmarshal number into Go struct field
// readArgs.path of type string". That names Go internals the model cannot act on,
// and says nothing at all when a required field is simply absent — the argument
// just arrives as a zero value and the tool fails later for a reason that looks
// unrelated.
//
// Unknown fields are deliberately tolerated: models add harmless extras, and
// refusing those would cost a turn for nothing. They are only mentioned when one
// of them looks like a misspelling of a field that is missing.
func ValidateArgs(t Tool, args json.RawMessage) error {
	if v, ok := t.(ArgsValidator); ok {
		return v.ValidateArgs(args)
	}

	// Use reflection-based schema if available, otherwise fall back to hand-written
	schema := t.InputSchema()
	if sp, ok := t.(SchemaProvider); ok {
		schema = sp.Schema()
	}
	return validateAgainst(schema, args, "")
}

func validateAgainst(schema map[string]any, raw json.RawMessage, prefix string) error {
	fields, err := objectFields(raw)
	if err != nil {
		return err
	}

	props, _ := schema["properties"].(map[string]any)
	var problems []string

	for _, name := range requiredNames(schema) {
		v, ok := fields[name]
		if ok && jsonKind(v) != "null" {
			continue
		}
		problems = append(problems, missingFieldProblem(prefix+name, name, fields, props))
	}

	// Sorted so a call with two bad fields reports them the same way every time.
	// A message that reorders itself between identical calls reads like two
	// different problems.
	for _, name := range sortedKeys(fields) {
		spec, ok := props[name].(map[string]any)
		if !ok {
			continue // unknown field: tolerated, see the doc comment
		}
		want, _ := spec["type"].(string)
		got := jsonKind(fields[name])
		if want == "" || got == "null" || got == want {
			continue
		}
		problems = append(problems, fmt.Sprintf("field %q must be %s, got %s", prefix+name, article(want), got))
	}

	// Enum values, checked here rather than left to each tool. A field with a closed
	// set of values is the case where naming the alternatives costs nothing and
	// guessing costs a turn, and the model has usually sent something close —
	// "readonly" for "explore" — which a bare "invalid value" would not help with.
	for _, name := range sortedKeys(fields) {
		spec, ok := props[name].(map[string]any)
		if !ok {
			continue
		}
		allowed := enumValues(spec)
		if len(allowed) == 0 || jsonKind(fields[name]) != "string" {
			continue
		}
		var got string
		if json.Unmarshal(fields[name], &got) != nil {
			continue
		}
		if !contains(allowed, got) {
			problems = append(problems, fmt.Sprintf("field %q must be one of %s, got %q",
				prefix+name, strings.Join(quoteAll(allowed), ", "), got))
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%s. Expected: %s", strings.Join(problems, "; "), describeFields(schema))
	}

	// Only recurse once the shallow shape is sound, so the first message the model
	// reads is about the outer object rather than about elements of an array that
	// should not have been there.
	for _, name := range sortedKeys(fields) {
		spec, ok := props[name].(map[string]any)
		if !ok {
			continue
		}
		items, ok := spec["items"].(map[string]any)
		if !ok || jsonKind(fields[name]) != "array" {
			continue
		}
		var elems []json.RawMessage
		if json.Unmarshal(fields[name], &elems) != nil {
			continue
		}
		for i, el := range elems {
			if err := validateAgainst(items, el, fmt.Sprintf("%s%s[%d].", prefix, name, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// objectFields decodes the argument object. Absent or empty arguments are an
// empty object rather than an error: a tool with no required fields is legitimately
// called with nothing, and the required check below reports the rest.
func objectFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("arguments must be a JSON object, got %s", jsonKind(raw))
	}
	return fields, nil
}

// missingFieldProblem names the absent field and, when one of the supplied fields
// looks like a misspelling of it, says so.
//
// That hint is the difference between one wasted turn and none: a model that sent
// file_path needs to be told which name this tool uses, not merely that something
// is missing.
func missingFieldProblem(display, name string, fields map[string]json.RawMessage, props map[string]any) string {
	var unknown []string
	for _, got := range sortedKeys(fields) {
		if _, known := props[got]; known {
			continue
		}
		if looksLike(got, name) {
			return fmt.Sprintf("missing required field %q (received %q, which is not a field of this tool)", display, got)
		}
		unknown = append(unknown, got)
	}
	if len(unknown) > 0 {
		// No near-match, but something unrecognised did arrive — "cmd" for
		// "command" is a real case that no cheap similarity test catches. Naming
		// what was received makes the mistake visible instead of leaving the model
		// to wonder whether its key was ignored or misread.
		return fmt.Sprintf("missing required field %q (unrecognised field(s) received: %s)",
			display, strings.Join(unknown, ", "))
	}
	return fmt.Sprintf("missing required field %q", display)
}

// looksLike reports whether a supplied name is plausibly a variant of a wanted
// one: case, separators, and common prefixes like file_path for path.
func looksLike(got, want string) bool {
	g, w := fold(got), fold(want)
	if g == w {
		return true
	}
	// Guard on length so that a one-letter field does not match everything.
	return len(w) >= 3 && (strings.Contains(g, w) || strings.Contains(w, g))
}

func fold(s string) string {
	return strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(s))
}

// describeFields renders the tool's fields so the model can correct itself from
// the error alone, without another round trip to re-read the schema.
func describeFields(schema map[string]any) string {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return "no fields"
	}
	required := map[string]bool{}
	for _, n := range requiredNames(schema) {
		required[n] = true
	}
	out := make([]string, 0, len(props))
	for _, name := range sortedKeys(props) {
		spec, _ := props[name].(map[string]any)
		typ, _ := spec["type"].(string)
		if typ == "" {
			typ = "any"
		}
		if required[name] {
			typ += ", required"
		}
		out = append(out, fmt.Sprintf("%s (%s)", name, typ))
	}
	return strings.Join(out, ", ")
}

// requiredNames reads the required list, tolerating both the []string the
// hand-written schemas use and the []any a schema that made a JSON round trip
// would produce.
func requiredNames(schema map[string]any) []string {
	switch v := schema["required"].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// enumValues reads an enum list, tolerating both the []string a hand-written
// schema uses and the []any one that made a JSON round trip produces — the same
// two shapes requiredNames has to handle, for the same reason.
func enumValues(spec map[string]any) []string {
	switch v := spec["enum"].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

func quoteAll(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}

// jsonKind names the JSON type of a raw value using JSON Schema's vocabulary, so
// the message the model reads uses the same words as the schema it was given.
func jsonKind(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return "null"
	}
	switch s[0] {
	case '"':
		return "string"
	case '{':
		return "object"
	case '[':
		return "array"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	default:
		return "number"
	}
}

func article(typ string) string {
	if strings.HasPrefix(typ, "a") || strings.HasPrefix(typ, "o") {
		return "an " + typ
	}
	return "a " + typ
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
