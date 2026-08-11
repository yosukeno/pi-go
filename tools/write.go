package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/wangy/pi-go/diff"
)

type Write struct {
	Cwd string
	// Roots are additional directories this tool may write to, for the agent's memory
	// notes. Empty for every other caller, which is what keeps skill bundles and the
	// rest of the filesystem out of reach. See Options.WriteRoots.
	Roots []string
	// Journal, when non-nil, snapshots each file's pre-image on first change.
	Journal Journal
}

type writeArgs struct {
	Path    string `json:"path" required:"true" description:"Path to the file to write (relative or absolute)"`
	Content string `json:"content" required:"true" description:"Content to write to the file"`
}

func (*Write) Name() string { return "write" }

func (*Write) Description() string {
	return "Write content to a file. Creates the file if it doesn't exist, overwrites it if it does. " +
		"Parent directories are created automatically."
}

// ExecutionMode is Parallel; concurrent writes to the same file are made safe by
// the per-path lock rather than by serializing the whole batch.
func (*Write) ExecutionMode() ExecutionMode { return Parallel }

func (*Write) InputSchema() map[string]any {
	// path before content, and the order is load-bearing: models tend to emit
	// arguments in the schema's property order, and the streaming preview names
	// the file from the first fragments only when the path arrives ahead of
	// the content.
	return objectOrdered([]string{"path", "content"}, orderedProps{
		{"path", prop("string", "Path to the file to write (relative or absolute)")},
		{"content", prop("string", "Content to write to the file")},
	})
}

// Schema returns the JSON schema for write tool using reflection.
func (*Write) Schema() map[string]any {
	return GenerateSchema(reflect.TypeOf(writeArgs{}))
}

func (t *Write) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var a writeArgs
	if err := unmarshal(raw, &a); err != nil {
		return Result{}, err
	}
	path, err := resolve(t.Cwd, a.Path, t.Roots...)
	if err != nil {
		return Result{}, err
	}

	// The read-then-write below is only atomic with respect to other tool calls
	// while this lock is held.
	return withFileLock(path, func() (Result, error) {
		// Read the previous content before overwriting so the UI can show what
		// changed. A failure here must not block the write: an unreadable file is
		// still a writable one, we just lose the diff.
		previous, readErr := os.ReadFile(path)
		created := readErr != nil

		// Snapshot the pre-image before MkdirAll can change anything. When the
		// read failed for a reason other than absence (permissions), the journal
		// records an empty base — wrong but harmlessly so, and rare.
		if t.Journal != nil {
			t.Journal.BeforeChange(path, previous, !created)
		}

		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return Result{}, err
		}
		if err := os.WriteFile(path, []byte(a.Content), 0o644); err != nil {
			return Result{}, err
		}

		details := WriteDetails{Path: a.Path, Bytes: len(a.Content), Created: created}
		if created {
			// Nothing to diff against, but the stats still describe the
			// change: every line of a created file is an addition.
			details.Added, details.Removed = diff.Stat("", a.Content)
		} else {
			old := string(previous)
			body, _ := diff.Display(old, a.Content, diff.DefaultContext)
			patch := diff.Unified(a.Path, old, a.Content, diff.DefaultContext)
			details.Diff, details.Patch, details.TooBig = capDetailsDiff(body, patch)
			details.Added, details.Removed = diff.Stat(old, a.Content)
		}
		return Result{
			Text:    fmt.Sprintf("Successfully wrote %d bytes to %s", len(a.Content), a.Path),
			Details: details,
		}, nil
	})
}
