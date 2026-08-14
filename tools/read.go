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
	Path  string   `json:"path,omitempty" description:"Path to the file to read (relative or absolute)"`
	Paths []string `json:"paths,omitempty" description:"Several files to read in one call. Use instead of path, not alongside it"`
	// Offset and Limit describe a position in one file, so they pair with path
	// only. See readArgs.targets for why using them with paths is refused rather
	// than applied to each.
	Offset int `json:"offset,omitempty" description:"Line number to start reading from (1-indexed). With path only"`
	Limit  int `json:"limit,omitempty" description:"Maximum number of lines to read. With path only"`
}

func (*Read) Name() string { return "read" }

func (*Read) Description() string {
	return fmt.Sprintf(
		"Read the contents of a text file. Output is truncated to %d lines or %dKB, whichever is hit first. "+
			"Use offset/limit for large files; when you need the whole file, continue with offset until complete. "+
			"For several files pass them as paths in one call, not one call each; that budget is then split "+
			"among them, so read a large file alone with path.",
		MaxLines, MaxBytes/1024)
}

// ExecutionMode is Parallel, and read is the tool that makes parallelism worth
// having: a model opening five files to understand a change is the common case,
// and it is pure I/O.
//
// Parallel with a caveat that took a while to notice: read-only is not the same as
// safe beside a sibling. A concurrent write or edit to the same file can be
// observed mid-truncation, so Execute takes the per-path lock. See the note there.
func (*Read) ExecutionMode() ExecutionMode { return Parallel }

// InputSchema declares no required property, which is a deliberate loss.
//
// path was required, and could not stay so once paths exists: a schema demanding
// path makes the multi-file form unusable, and a schema demanding either would need
// oneOf, which this project has already been bitten by on one provider's validator
// (see object). So "exactly one of path and paths" is enforced in targets instead,
// with an error the model can act on — which is the same trade the guard makes
// everywhere else here: refuse clearly rather than describe cleverly.
func (*Read) InputSchema() map[string]any {
	return object(nil, map[string]any{
		"path": prop("string", "Path to the file to read (relative or absolute)"),
		"paths": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": "Several files to read in one call. Use instead of path, not alongside it",
		},
		"offset": prop("number", "Line number to start reading from (1-indexed). With path only"),
		"limit":  prop("number", "Maximum number of lines to read. With path only"),
	})
}

// Schema returns the JSON schema for read tool using reflection.
func (*Read) Schema() map[string]any {
	return GenerateSchema(reflect.TypeOf(readArgs{}))
}

// ValidateArgs takes over because "exactly one of path and paths" is not something
// the schema can state portably; see InputSchema for why oneOf was not used.
//
// The ordinary schema check runs first, so every message the model already got for a
// wrong type, a bad enum or an unrecognised field is unchanged and only the one-of
// rule is added on top. missingFieldProblem is reused rather than reworded, because
// the real cost of dropping path from the required list was losing its hint: a call
// that sends file_path has to be told which name this tool uses, or it guesses.
//
// Presence is read off the raw JSON keys rather than off an unmarshalled readArgs,
// and that is not a stylistic choice: encoding/json matches field names
// case-insensitively, so `{"Path":"a.go"}` populates readArgs.Path and a struct-based
// check would call the call well-formed. The whole point of this stage is to tell the
// model that this tool spells it "path", so the check has to be as case-sensitive as
// the schema it is enforcing. Verified by the Path case in
// TestAMisspelledFieldIsPointedOut, which this got wrong first time round.
func (t *Read) ValidateArgs(raw json.RawMessage) error {
	schema := t.Schema()
	if err := validateAgainst(schema, raw, ""); err != nil {
		return err
	}
	fields, err := objectFields(raw)
	if err != nil {
		return err
	}
	// Null counts as absent, matching how the generic required check reads it: a
	// field explicitly set to null was not supplied.
	present := func(name string) bool {
		v, ok := fields[name]
		return ok && jsonKind(v) != "null"
	}
	switch hasPath, hasPaths := present("path"), present("paths"); {
	case hasPath && hasPaths:
		return fmt.Errorf(`give either "path" or "paths", not both. Expected: %s`, describeFields(schema))
	case !hasPath && !hasPaths:
		props, _ := schema["properties"].(map[string]any)
		return fmt.Errorf("%s. Expected: %s",
			missingFieldProblem("path", "path", fields, props), describeFields(schema))
	}
	return nil
}

// targets resolves the one-or-many argument into the list of files to read, or says
// why it cannot.
//
// Duplicates are dropped, keeping first position. A repeated path would otherwise be
// read twice and charged twice against a budget the other files are sharing, and the
// second copy tells the model nothing the first did not.
//
// offset and limit with paths is refused rather than applied to every file. Applying
// them would mean "line 40 onward in each of these five files", which is a question
// nobody asks; and silently ignoring them would answer a different question than the
// one that was asked, which is worse than a refusal that names the fix.
func (a readArgs) targets() ([]string, error) {
	switch {
	case a.Path != "" && len(a.Paths) > 0:
		return nil, fmt.Errorf("give either path or paths, not both: path=%q and %d paths were set. "+
			"Use paths alone to read several files in one call", a.Path, len(a.Paths))
	case a.Path == "" && len(a.Paths) == 0:
		return nil, fmt.Errorf("path is required — or paths, to read several files in one call")
	case a.Path != "":
		return []string{a.Path}, nil
	case a.Offset > 0 || a.Limit > 0:
		return nil, fmt.Errorf("offset and limit describe a position in one file and cannot be used with paths. "+
			"Read that file on its own with path, or drop offset/limit to read all %d whole", len(a.Paths))
	}
	seen := make(map[string]bool, len(a.Paths))
	out := make([]string, 0, len(a.Paths))
	for _, p := range a.Paths {
		if p == "" {
			return nil, fmt.Errorf("paths contains an empty string")
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out, nil
}

func (t *Read) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var a readArgs
	if err := unmarshal(raw, &a); err != nil {
		return Result{}, err
	}
	targets, err := a.targets()
	if err != nil {
		return Result{}, err
	}
	// One file keeps the original path exactly, Details included, because that is
	// what the web UI's ReadResult renders and what the overwhelming majority of
	// calls are.
	if len(targets) > 1 {
		return t.readMany(targets)
	}
	a.Path = targets[0]
	path, err := resolve(t.Cwd, a.Path, t.Roots...)
	if err != nil {
		return Result{}, err
	}
	// Under the same lock write and edit take, and for a reason that is not
	// symmetry. Both of them rewrite with os.WriteFile, which truncates and then
	// writes, so a sibling read of that file in the same parallel batch can land in
	// the gap and come back with a prefix — or with nothing — and no error. A short
	// file is not distinguishable from a file that was short.
	//
	// The lock was on the mutating half only, which covered two writers racing each
	// other and left a reader racing a writer wide open. bash's Sequential comment
	// claims a batch-wide serialization "also stops a read from racing a command
	// that rewrites the file being read", and that is true of bash and was never
	// true of write and edit, which are Parallel.
	//
	// Not a theoretical window. With this lock removed, the test drives it on the
	// twelfth read: 0 bytes back from a 32,000-byte file, no error. What was never
	// observed is a *session* hitting it, because a model usually reads a file in an
	// earlier turn than the one that edits it — which is why this went unnoticed, not
	// why it was safe.
	//
	// What this still does not cover: bash. Its writes are arbitrary shell commands
	// that never pass through here, so read-vs-bash rests entirely on bash being
	// Sequential. That is the one protection a relaxed batch rule would spend, and
	// this lock does not buy it back.
	data, err := withFileLock(path, func() ([]byte, error) { return os.ReadFile(path) })
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

// readMany reads several files in one call, which is one round trip instead of one
// per file.
//
// The point is not disk time — reads were already parallel — it is the turn. Every
// extra tool call is another model round trip, and a round trip costs a full
// inference plus the wait for its first token, whether or not the prompt was cached.
// Five separate reads also depend on the model choosing to emit five tool_use blocks
// in one message, which it frequently does not; five paths in one call depend only on
// it filling an array.
//
// Three things are deliberately different from the single-file path.
//
// The budget is divided rather than repeated. Five files at the full ceiling each
// would be five times the limit this package exists to enforce, and that limit is
// what stops one tool result from swallowing the context window. So each file gets
// its share and says so when it is cut, which is also the honest signal to the model
// that asking for fewer files gets more of each.
//
// A file that cannot be read does not fail the call. Its section carries the error
// and the others still return, because the alternative — one missing file wasting the
// whole round trip the call existed to save — is the exact cost this is trying to
// avoid. All of them failing is a different matter and is returned as an error.
//
// Details is deliberately not shaped like ReadDetails. The web UI's ReadResult
// renders one file: a path, a line count, a continue-from-here button. Rather than
// grow that component now, the multi-file result carries its own summary, which the
// UI does not recognise and falls back to rendering as plain text (ToolCall.vue's
// final v-else). The transcript still has the structure for the analyzer and for
// whenever the component is written.
func (t *Read) readMany(paths []string) (Result, error) {
	// Headers and blank lines are not counted against the shares. They are a
	// handful of bytes per file against a budget in tens of kilobytes, and
	// deducting them would make each file's share depend on the length of its own
	// path.
	perLines := max(1, MaxLines/len(paths))
	perBytes := max(1, MaxBytes/len(paths))

	var b strings.Builder
	details := ReadManyDetails{Files: make([]ReadFileDetails, 0, len(paths))}
	failures := 0
	for i, p := range paths {
		if i > 0 {
			b.WriteString("\n\n")
		}
		// The same convention head and tail use for several files, so the shape is
		// already familiar rather than invented here.
		fmt.Fprintf(&b, "==> %s <==\n", p)

		fd := ReadFileDetails{Path: p}
		content, err := t.readOne(p)
		if err != nil {
			failures++
			fd.Error = err.Error()
			fmt.Fprintf(&b, "[error: %s]", err)
			details.Files = append(details.Files, fd)
			continue
		}

		tr := TruncateHeadLimit(content, perLines, perBytes)
		fd.TotalLines, fd.ShownLines, fd.Truncated, fd.TruncatedBy = tr.TotalLines, tr.OutputLines, tr.Truncated, tr.By
		// Recorded around the content alone: the header identifies the section and the
		// note below is addressed to the model, so neither is part of the file. See
		// ReadFileDetails.BodyOffset for why this is measured here rather than found
		// again by whoever renders it.
		fd.BodyOffset = b.Len()
		b.WriteString(tr.Content)
		fd.BodyLength = b.Len() - fd.BodyOffset
		if tr.Truncated {
			// Naming path rather than paths on purpose: offset is refused with
			// paths, so the continuation has to be a single-file call.
			fmt.Fprintf(&b, "\n\n[Showing lines 1-%d of %d — this call's budget split %d ways. "+
				"Read it alone with path, or continue with path=%s offset=%d.]",
				tr.OutputLines, tr.TotalLines, len(paths), p, tr.OutputLines+1)
		}
		details.Files = append(details.Files, fd)
	}

	// Every path failing is not a partial success worth reporting as one: the call
	// produced no file contents at all, and an is_error result is what makes the
	// model fix the paths instead of reasoning about empty sections.
	if failures == len(paths) {
		return Result{Details: details}, fmt.Errorf("none of the %d paths could be read:\n%s", len(paths), b.String())
	}
	return Result{Text: b.String(), Details: details}, nil
}

// readOne resolves a path and returns its bytes under the per-path lock. Shared with
// the single-file path so that both get the same guard and the same lock; see the
// note in Execute for why a read takes it at all.
func (t *Read) readOne(p string) (string, error) {
	path, err := resolve(t.Cwd, p, t.Roots...)
	if err != nil {
		return "", err
	}
	data, err := withFileLock(path, func() ([]byte, error) { return os.ReadFile(path) })
	if err != nil {
		return "", err
	}
	return string(data), nil
}
