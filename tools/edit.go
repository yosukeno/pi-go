package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/wangy/pi-go/diff"
)

// Edit does exact string replacement. It is the tool that decides whether a
// coding agent is usable: without it the model rewrites whole files with write
// and burns output tokens, and every rewrite risks silent regressions.
type Edit struct {
	Cwd string
	// Roots are additional directories this tool may modify, for the agent's memory
	// notes. Empty for every other caller. See Options.WriteRoots.
	Roots []string
	// Journal, when non-nil, snapshots each file's pre-image on first change.
	Journal Journal
}

type editOp struct {
	OldText string `json:"oldText" required:"true" description:"Exact text to replace. Must be unique in the file."`
	NewText string `json:"newText" required:"true" description:"Replacement text."`
}

type editArgs struct {
	Path  string   `json:"path" required:"true" description:"Path to the file to edit (relative or absolute)"`
	Edits []editOp `json:"edits" required:"true" description:"One or more targeted replacements, each matched against the original file content."`
	// Models frequently emit the flat single-edit form regardless of the schema,
	// so accept it instead of wasting a turn on a validation error.
	OldText *string `json:"oldText,omitempty"`
	NewText *string `json:"newText,omitempty"`
}

func (*Edit) Name() string { return "edit" }

func (*Edit) Description() string {
	return "Edit a single file using exact text replacement. Every edits[].oldText must match exactly one " +
		"non-overlapping region of the file on disk. Include enough surrounding context to make each match unique. " +
		"If two changes touch the same or adjacent lines, merge them into one edit."
}

// ExecutionMode is Parallel; the per-path lock is what prevents two edits to one
// file from losing an update.
func (*Edit) ExecutionMode() ExecutionMode { return Parallel }

func (*Edit) InputSchema() map[string]any {
	// path first, same load-bearing order as write's schema (see there).
	return objectOrdered([]string{"path", "edits"}, orderedProps{
		{"path", prop("string", "Path to the file to edit (relative or absolute)")},
		{"edits", map[string]any{
			"type":        "array",
			"description": "One or more targeted replacements, each matched against the original file content.",
			"items": object([]string{"oldText", "newText"}, map[string]any{
				"oldText": prop("string", "Exact text to replace. Must be unique in the file."),
				"newText": prop("string", "Replacement text."),
			}),
		}},
	})
}

// Schema returns the JSON schema for edit tool using reflection.
func (*Edit) Schema() map[string]any {
	// For edit tool, we need to build the schema manually because of the nested
	// array structure. The reflection generator handles simple structs, but the
	// array of editOps needs manual construction.
	return object([]string{"path", "edits"}, map[string]any{
		"path": prop("string", "Path to the file to edit (relative or absolute)"),
		"edits": map[string]any{
			"type":        "array",
			"description": "One or more targeted replacements, each matched against the original file content.",
			"items":       GenerateSchema(reflect.TypeOf(editOp{})),
		},
	})
}

// ValidateArgs opts edit out of schema validation, because the schema does not
// describe every shape it accepts: the flat oldText/newText form above is
// missing the required "edits" field and would be rejected before it ever
// reached Execute.
//
// The check is therefore written in terms of what Execute actually needs — a
// path, and at least one replacement in either form.
func (*Edit) ValidateArgs(raw json.RawMessage) error {
	var a editArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("arguments must be a JSON object matching: path (string, required), "+
			"edits (array of {oldText, newText}, required): %w", err)
	}
	if a.Path == "" {
		return fmt.Errorf(`missing required field "path". Expected: edits (array, required), path (string, required)`)
	}
	if len(a.Edits) == 0 && (a.OldText == nil || a.NewText == nil) {
		return fmt.Errorf(`missing required field "edits": pass edits as [{"oldText": ..., "newText": ...}], ` +
			`or the single-edit shorthand oldText and newText at the top level`)
	}
	for i, e := range a.Edits {
		if e.OldText == "" {
			return fmt.Errorf("edits[%d].oldText must not be empty: it is the text to be replaced", i)
		}
	}
	return nil
}

func (t *Edit) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var a editArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return Result{}, err
	}
	if a.OldText != nil && a.NewText != nil {
		a.Edits = append(a.Edits, editOp{OldText: *a.OldText, NewText: *a.NewText})
	}
	if len(a.Edits) == 0 {
		return Result{}, fmt.Errorf("edits must contain at least one replacement")
	}
	path, err := resolve(t.Cwd, a.Path, t.Roots...)
	if err != nil {
		return Result{}, err
	}
	// Everything below is a read-modify-write cycle. Without this lock two edits
	// to the same file in one batch would both read the original and the second
	// write would silently discard the first.
	return withFileLock(path, func() (Result, error) { return applyEdits(path, a, t.Journal) })
}

func applyEdits(path string, a editArgs, journal Journal) (Result, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Result{}, err
	}
	if info.IsDir() {
		return Result{}, fmt.Errorf("could not edit %s: path is a directory", a.Path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	// Snapshot before any matching happens: the pre-image is the file as read,
	// whatever the edits later do to it. The journal itself dedups repeat calls.
	if journal != nil {
		journal.BeforeChange(path, data, true)
	}

	// Strip any BOM before matching and put it back on write. Otherwise oldText
	// aimed at the first line of a Windows-authored file never matches, because
	// the model has no way to know the invisible prefix is there.
	original := string(data)
	bom := ""
	if strings.HasPrefix(original, "\ufeff") {
		bom, original = "\ufeff", strings.TrimPrefix(original, "\ufeff")
	}

	// Normalise to LF for matching so the model never has to guess line endings,
	// then restore the file's original convention on write.
	crlf := strings.Contains(original, "\r\n")
	content := strings.ReplaceAll(original, "\r\n", "\n")

	type span struct {
		start, end  int
		replacement string
	}
	spans := make([]span, 0, len(a.Edits))
	for i, e := range a.Edits {
		if e.OldText == "" {
			return Result{}, fmt.Errorf("edits[%d].oldText must not be empty", i)
		}
		old := strings.ReplaceAll(e.OldText, "\r\n", "\n")
		n := strings.Count(content, old)
		if n == 0 {
			return Result{}, fmt.Errorf("edits[%d].oldText was not found in %s. "+
				"Read the file again and copy the exact text, including indentation", i, a.Path)
		}
		if n > 1 {
			return Result{}, fmt.Errorf("edits[%d].oldText matches %d places in %s. "+
				"Add surrounding context to make it unique", i, n, a.Path)
		}
		start := strings.Index(content, old)
		spans = append(spans, span{start, start + len(old), strings.ReplaceAll(e.NewText, "\r\n", "\n")})
	}

	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for i := 1; i < len(spans); i++ {
		if spans[i].start < spans[i-1].end {
			return Result{}, fmt.Errorf("edits overlap in %s. Merge the overlapping replacements into one edit", a.Path)
		}
	}

	// Apply back to front so earlier offsets stay valid.
	out := content
	for i := len(spans) - 1; i >= 0; i-- {
		out = out[:spans[i].start] + spans[i].replacement + out[spans[i].end:]
	}

	final := out
	if crlf {
		final = strings.ReplaceAll(out, "\n", "\r\n")
	}
	if err := os.WriteFile(path, []byte(bom+final), info.Mode().Perm()); err != nil {
		return Result{}, err
	}

	// Diff against the LF-normalised forms so line endings never show up as
	// changes.
	body, firstChanged := diff.Display(content, out, diff.DefaultContext)
	added, removed := diff.Stat(content, out)
	display, patch, tooBig := capDetailsDiff(body, diff.Unified(a.Path, content, out, diff.DefaultContext))
	return Result{
		Text: fmt.Sprintf("Successfully replaced %d block(s) in %s", len(spans), a.Path),
		Details: EditDetails{
			Path:             a.Path,
			Edits:            len(spans),
			Diff:             display,
			Patch:            patch,
			FirstChangedLine: firstChanged,
			Added:            added,
			Removed:          removed,
			TooBig:           tooBig,
		},
	}, nil
}
