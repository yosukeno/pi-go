package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
)

type Read struct {
	Cwd string
	// Roots are extra directories this tool may read outside Cwd, already
	// canonical. See tools.CanonicalRoots.
	Roots []string
}

type readArgs struct {
	Path   string `json:"path" required:"true" description:"Path to the file to read (relative or absolute)"`
	Offset int    `json:"offset,omitempty" description:"Line number to start reading from (1-indexed)"`
	Limit  int    `json:"limit,omitempty" description:"Maximum number of lines to read"`
}

func (*Read) Name() string { return "read" }

func (*Read) Description() string {
	return fmt.Sprintf(
		"Read the contents of a text file. Output is truncated to %d lines or %dKB, whichever is hit first. "+
			"Use offset/limit for large files; when you need the whole file, continue with offset until complete.",
		MaxLines, MaxBytes/1024)
}

// ExecutionMode is Parallel, and read is the tool that makes parallelism worth
// having: a model opening five files to understand a change is the common case,
// and it is pure I/O.
func (*Read) ExecutionMode() ExecutionMode { return Parallel }

func (*Read) InputSchema() map[string]any {
	return object([]string{"path"}, map[string]any{
		"path":   prop("string", "Path to the file to read (relative or absolute)"),
		"offset": prop("number", "Line number to start reading from (1-indexed)"),
		"limit":  prop("number", "Maximum number of lines to read"),
	})
}

// Schema returns the JSON schema for read tool using reflection.
func (*Read) Schema() map[string]any {
	return GenerateSchema(reflect.TypeOf(readArgs{}))
}

func (t *Read) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var a readArgs
	if err := unmarshal(raw, &a); err != nil {
		return Result{}, err
	}
	path, err := resolve(t.Cwd, a.Path, t.Roots...)
	if err != nil {
		return Result{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}

	lines := strings.Split(string(data), "\n")
	total := len(lines)
	start := 0
	if a.Offset > 0 {
		start = a.Offset - 1
	}
	if start >= total {
		return Result{}, fmt.Errorf("offset %d is beyond end of file (%d lines total)", a.Offset, total)
	}

	end := total
	userLimited := false
	if a.Limit > 0 && start+a.Limit < total {
		end = start + a.Limit
		userLimited = true
	}

	tr := TruncateHead(strings.Join(lines[start:end], "\n"))
	out := tr.Content
	shown := tr.OutputLines
	switch {
	case tr.Truncated:
		last := start + tr.OutputLines
		note := fmt.Sprintf("[Showing lines %d-%d of %d.", start+1, last, total)
		if tr.By == "bytes" {
			note = fmt.Sprintf("[Showing lines %d-%d of %d (%s limit).", start+1, last, total, formatSize(MaxBytes))
		}
		out += fmt.Sprintf("\n\n%s Use offset=%d to continue.]", note, last+1)
	case userLimited:
		out += fmt.Sprintf("\n\n[%d more lines in file. Use offset=%d to continue.]", total-end, end+1)
	}

	return Result{Text: out, Details: ReadDetails{
		Path:        a.Path,
		Offset:      a.Offset,
		Limit:       a.Limit,
		TotalLines:  total,
		ShownLines:  shown,
		FirstLine:   start + 1,
		Truncated:   tr.Truncated,
		TruncatedBy: tr.By,
	}}, nil
}
