package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func args(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// --- path guard ---

func TestResolveRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"../out.txt", "/etc/hosts", "sub/../../out.txt", ""} {
		if _, err := resolve(dir, p); err == nil {
			t.Errorf("resolve(%q) should have been rejected", p)
		}
	}
	if _, err := resolve(dir, "sub/ok.txt"); err != nil {
		t.Errorf("a path inside the root was rejected: %v", err)
	}
}

// --- edit ---

func TestEditProducesDiffDetails(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "f.go", "package p\n\nconst A = 1\nconst B = 2\n")

	e := &Edit{Cwd: dir}
	res, err := e.Execute(context.Background(), args(t, map[string]any{
		"path":  "f.go",
		"edits": []map[string]string{{"oldText": "const A = 1", "newText": "const A = 99"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if read(t, path) != "package p\n\nconst A = 99\nconst B = 2\n" {
		t.Fatalf("file content wrong: %q", read(t, path))
	}

	d, ok := res.Details.(EditDetails)
	if !ok {
		t.Fatalf("details type = %T, want EditDetails", res.Details)
	}
	if d.Added != 1 || d.Removed != 1 {
		t.Errorf("added=%d removed=%d, want 1/1", d.Added, d.Removed)
	}
	if d.FirstChangedLine != 3 {
		t.Errorf("firstChangedLine = %d, want 3", d.FirstChangedLine)
	}
	if !strings.Contains(d.Diff, "+3 const A = 99") {
		t.Errorf("diff missing the new line:\n%s", d.Diff)
	}
	if !strings.Contains(d.Patch, "@@") {
		t.Errorf("patch is not unified format:\n%s", d.Patch)
	}
	// The details must not leak into what the model sees.
	if strings.Contains(res.Text, "const A = 99") {
		t.Errorf("diff leaked into the model-facing text: %q", res.Text)
	}
}

func TestEditRequiresUniqueMatch(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "f.txt", "a\nb\na\n")
	e := &Edit{Cwd: dir}

	_, err := e.Execute(context.Background(), args(t, map[string]any{
		"path":  "f.txt",
		"edits": []map[string]string{{"oldText": "a", "newText": "z"}},
	}))
	if err == nil || !strings.Contains(err.Error(), "matches 2 places") {
		t.Fatalf("want an ambiguity error, got %v", err)
	}

	res, err := e.Execute(context.Background(), args(t, map[string]any{
		"path":  "f.txt",
		"edits": []map[string]string{{"oldText": "b\na", "newText": "b\nz"}},
	}))
	if err != nil {
		t.Fatalf("unique edit failed: %v", err)
	}
	if !strings.Contains(res.Text, "1 block") {
		t.Errorf("unexpected text %q", res.Text)
	}
}

// Line endings and the BOM are invisible to the model, so they must survive an
// edit untouched and must not appear as diff noise.
func TestEditPreservesCRLFAndBOM(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "w.txt", "\ufeffone\r\ntwo\r\nthree\r\n")

	e := &Edit{Cwd: dir}
	res, err := e.Execute(context.Background(), args(t, map[string]any{
		"path":  "w.txt",
		"edits": []map[string]string{{"oldText": "two", "newText": "TWO"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if want := "\ufeffone\r\nTWO\r\nthree\r\n"; got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	d := res.Details.(EditDetails)
	if d.Added != 1 || d.Removed != 1 {
		t.Errorf("line endings showed up as changes: added=%d removed=%d", d.Added, d.Removed)
	}
}

func TestEditAcceptsFlatForm(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "f.txt", "hello\n")
	e := &Edit{Cwd: dir}
	// Models routinely ignore the array schema and send this shape.
	if _, err := e.Execute(context.Background(),
		json.RawMessage(`{"path":"f.txt","oldText":"hello","newText":"bye"}`)); err != nil {
		t.Fatalf("flat form rejected: %v", err)
	}
	if read(t, filepath.Join(dir, "f.txt")) != "bye\n" {
		t.Error("flat form did not apply")
	}
}

func TestEditCapsOversizedDiffDetails(t *testing.T) {
	defer func(orig int) { maxDetailsDiff = orig }(maxDetailsDiff)
	maxDetailsDiff = 64

	dir := t.TempDir()
	write(t, dir, "f.txt", "alpha\nbeta\ngamma\n")
	e := &Edit{Cwd: dir}
	res, err := e.Execute(context.Background(), args(t, map[string]any{
		"path":  "f.txt",
		"edits": []map[string]any{{"oldText": "beta", "newText": "one\ntwo\nthree\nfour\nfive"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Details.(EditDetails)
	if !d.TooBig {
		t.Error("TooBig should be set when the combined diff bodies exceed the cap")
	}
	if d.Diff != "" || d.Patch != "" {
		t.Error("diff bodies should be dropped over the cap")
	}
	if d.Added != 5 || d.Removed != 1 {
		t.Errorf("stats must survive the cap: added=%d removed=%d, want 5/1", d.Added, d.Removed)
	}
	if !strings.HasPrefix(res.Text, "Successfully replaced 1 block(s)") {
		t.Errorf("model-facing text changed: %q", res.Text)
	}
}

// --- write ---

func TestWriteDetails(t *testing.T) {
	dir := t.TempDir()
	w := &Write{Cwd: dir}

	res, err := w.Execute(context.Background(), args(t, map[string]any{
		"path": "new.txt", "content": "a\nb\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Details.(WriteDetails)
	if !d.Created {
		t.Error("Created should be true for a new file")
	}
	if d.Diff != "" {
		t.Error("a new file has nothing to diff against")
	}
	if d.TooBig {
		t.Error("a create has no diff bodies, so nothing to cap")
	}
	if d.Added != 2 || d.Removed != 0 {
		t.Errorf("created file stats: added=%d removed=%d, want 2/0", d.Added, d.Removed)
	}

	res, err = w.Execute(context.Background(), args(t, map[string]any{
		"path": "new.txt", "content": "a\nc\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	d = res.Details.(WriteDetails)
	if d.Created {
		t.Error("Created should be false when overwriting")
	}
	if d.Added != 1 || d.Removed != 1 {
		t.Errorf("added=%d removed=%d, want 1/1", d.Added, d.Removed)
	}
	if !strings.Contains(d.Diff, "+2 c") {
		t.Errorf("overwrite diff wrong:\n%s", d.Diff)
	}
}

func TestWriteSmallDiffSurvivesTheCap(t *testing.T) {
	defer func(orig int) { maxDetailsDiff = orig }(maxDetailsDiff)
	maxDetailsDiff = 4096

	dir := t.TempDir()
	write(t, dir, "small.txt", "a\nb\n")
	w := &Write{Cwd: dir}
	res, err := w.Execute(context.Background(), args(t, map[string]any{
		"path": "small.txt", "content": "a\nc\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Details.(WriteDetails)
	if d.TooBig {
		t.Error("a small diff must not trip the cap")
	}
	if !strings.Contains(d.Diff, "+2 c") || d.Patch == "" {
		t.Errorf("small overwrite should keep both diff bodies:\n%s", d.Diff)
	}
}

func TestWriteCapsOversizedDiffDetails(t *testing.T) {
	defer func(orig int) { maxDetailsDiff = orig }(maxDetailsDiff)
	maxDetailsDiff = 64

	dir := t.TempDir()
	write(t, dir, "big.txt", "old one\nold two\n")
	w := &Write{Cwd: dir}
	content := "new one\nnew two\nnew three\n"
	res, err := w.Execute(context.Background(), args(t, map[string]any{
		"path": "big.txt", "content": content,
	}))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Details.(WriteDetails)
	if !d.TooBig {
		t.Error("TooBig should be set when the combined diff bodies exceed the cap")
	}
	if d.Diff != "" || d.Patch != "" {
		t.Error("diff bodies should be dropped over the cap")
	}
	if d.Added != 3 || d.Removed != 2 {
		t.Errorf("stats must survive the cap: added=%d removed=%d, want 3/2", d.Added, d.Removed)
	}
	if want := fmt.Sprintf("Successfully wrote %d bytes to big.txt", len(content)); res.Text != want {
		t.Errorf("model-facing text changed: %q, want %q", res.Text, want)
	}

	// On the wire the bodies are omitted entirely and too_big says why.
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	if _, ok := wire["diff"]; ok {
		t.Error("diff key should be omitted over the cap")
	}
	if _, ok := wire["patch"]; ok {
		t.Error("patch key should be omitted over the cap")
	}
	if wire["too_big"] != true {
		t.Errorf("too_big = %v, want true", wire["too_big"])
	}
}

// --- read ---

func TestReadDetailsReportTruncation(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := range MaxLines + 500 {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	write(t, dir, "big.txt", b.String())

	r := &Read{Cwd: dir}
	res, err := r.Execute(context.Background(), args(t, map[string]any{"path": "big.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Details.(ReadDetails)
	if !d.Truncated {
		t.Fatal("expected truncation")
	}
	if d.ShownLines > MaxLines {
		t.Errorf("shown=%d exceeds the %d line cap", d.ShownLines, MaxLines)
	}
	if d.TotalLines < MaxLines+500 {
		t.Errorf("totalLines=%d, want at least %d", d.TotalLines, MaxLines+500)
	}
	if !strings.Contains(res.Text, "Use offset=") {
		t.Error("the model needs a hint on how to continue reading")
	}
}

func TestReadOffsetBeyondEOF(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "s.txt", "a\nb\n")
	r := &Read{Cwd: dir}
	_, err := r.Execute(context.Background(), args(t, map[string]any{"path": "s.txt", "offset": 99}))
	if err == nil || !strings.Contains(err.Error(), "beyond end of file") {
		t.Errorf("want a beyond-EOF error, got %v", err)
	}
}

// --- bash ---

// A failed command still has an exit code and output worth showing, so details
// must accompany the error.
func TestBashDetailsOnFailure(t *testing.T) {
	dir := t.TempDir()
	b := &Bash{Cwd: dir}
	res, err := b.Execute(context.Background(), args(t, map[string]any{
		"command": "echo out; echo err >&2; exit 3",
	}))
	if err == nil {
		t.Fatal("want an error for a non-zero exit")
	}
	if !strings.Contains(err.Error(), "exited with code 3") {
		t.Errorf("error should name the exit code: %v", err)
	}
	if !strings.Contains(err.Error(), "out") || !strings.Contains(err.Error(), "err") {
		t.Errorf("both streams should reach the model: %v", err)
	}
	d, ok := res.Details.(BashDetails)
	if !ok {
		t.Fatalf("details type = %T, want BashDetails even on failure", res.Details)
	}
	if d.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", d.ExitCode)
	}
	if d.Command == "" {
		t.Error("Command should be recorded")
	}
}

func TestBashSuccessDetails(t *testing.T) {
	dir := t.TempDir()
	b := &Bash{Cwd: dir}
	res, err := b.Execute(context.Background(), args(t, map[string]any{"command": "pwd"}))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Details.(BashDetails)
	if d.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", d.ExitCode)
	}
	// Commands run in the tool's working directory, not the test process's.
	if !strings.Contains(res.Text, filepath.Base(dir)) {
		t.Errorf("command did not run in Cwd: %q", res.Text)
	}
}

func TestBashRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := &Bash{Cwd: t.TempDir()}
	if _, err := b.Execute(ctx, args(t, map[string]any{"command": "echo hi"})); err == nil {
		t.Error("a cancelled context should abort the command")
	}
}

// Cancelling has to take the command's descendants with it. The default
// exec.CommandContext behaviour signals only the direct child, which leaves a
// subshell running and reparented to init — so a cancelled `go test` would keep
// burning the machine while the agent reports it stopped.
func TestCancelKillsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "survivor")

	// The touch runs in a subshell, i.e. a grandchild of the process being
	// cancelled. Killing bash alone would let it finish and create the file.
	cmd := fmt.Sprintf("(sleep 1; touch %s) & wait", marker)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := &Bash{Cwd: dir}
		if _, err := b.Execute(ctx, args(t, map[string]any{"command": cmd})); err == nil {
			t.Error("a cancelled command should report failure")
		}
	}()

	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	// Well past the subshell's sleep: if it were still alive, the file would be
	// here by now.
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("a grandchild process outlived cancellation")
	}
}

// --- per-path lock ---

// Two concurrent edits to different parts of one file must both survive. Without
// the per-path lock each would read the original and the later write would
// silently discard the other's change.
func TestConcurrentEditsToSameFileDoNotLoseUpdates(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "f.txt", "alpha\nbeta\ngamma\ndelta\n")
	e := &Edit{Cwd: dir}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	edits := []struct{ old, new string }{
		{"alpha", "ALPHA"},
		{"delta", "DELTA"},
	}
	start := make(chan struct{})
	for i, ed := range edits {
		wg.Add(1)
		go func(i int, old, newer string) {
			defer wg.Done()
			<-start // maximise the overlap
			_, errs[i] = e.Execute(context.Background(), args(t, map[string]any{
				"path":  "f.txt",
				"edits": []map[string]string{{"oldText": old, "newText": newer}},
			}))
		}(i, ed.old, ed.new)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("edit %d failed: %v", i, err)
		}
	}
	if got, want := read(t, path), "ALPHA\nbeta\ngamma\nDELTA\n"; got != want {
		t.Errorf("content = %q, want %q: an update was lost", got, want)
	}
}

// The lock is keyed on the canonical path, so spellings of the same file must
// contend with each other rather than each taking its own lock.
func TestPathLockKeyIsCanonical(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "sub/f.txt", "one\ntwo\n")
	e := &Edit{Cwd: dir}

	spellings := []string{"sub/f.txt", "./sub/f.txt", filepath.Join(dir, "sub", "f.txt"), "sub/../sub/f.txt"}
	replacements := []string{"ONE", "1", "uno", "eins"}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, p := range spellings {
		wg.Add(1)
		go func(p, to string) {
			defer wg.Done()
			<-start
			// Each rewrites the "two" line, so the last writer wins; the point is
			// that no run observes a torn file.
			_, _ = e.Execute(context.Background(), args(t, map[string]any{
				"path":  p,
				"edits": []map[string]string{{"oldText": "two", "newText": to}},
			}))
		}(p, replacements[i])
		_ = i
	}
	close(start)
	wg.Wait()

	got := read(t, filepath.Join(dir, "sub", "f.txt"))
	if !strings.HasPrefix(got, "one\n") {
		t.Errorf("file was torn by concurrent edits: %q", got)
	}
	if strings.Count(got, "\n") != 2 {
		t.Errorf("line count changed under concurrency: %q", got)
	}
}

// Releasing a lock must drop it from the map, or a long session accumulates one
// entry per file it ever touched.
func TestPathLocksDoNotLeak(t *testing.T) {
	dir := t.TempDir()
	w := &Write{Cwd: dir}
	for i := range 50 {
		if _, err := w.Execute(context.Background(), args(t, map[string]any{
			"path": fmt.Sprintf("f%d.txt", i), "content": "x",
		})); err != nil {
			t.Fatal(err)
		}
	}
	fileMutations.mu.Lock()
	n := len(fileMutations.m)
	fileMutations.mu.Unlock()
	if n != 0 {
		t.Errorf("%d lock entries left behind, want 0", n)
	}
}

// Every tool's schema must survive the wire as a provider will read it.
//
// This exists because it did not. The ls tool declares no mandatory arguments and
// passed a nil slice, which Go marshals to JSON null; moonshot/kimi rejects the
// request with "required must be an array" and refuses the whole call, so one
// tool with no required fields broke every model on that provider. Nothing in the
// suite looked at the marshalled shape, so nothing caught it.
func TestToolSchemasMarshalAsProvidersRequire(t *testing.T) {
	for _, tool := range Default(t.TempDir()).All() {
		raw, err := json.Marshal(tool.InputSchema())
		if err != nil {
			t.Fatalf("%s: %v", tool.Name(), err)
		}
		var got struct {
			Type       string          `json:"type"`
			Required   json.RawMessage `json:"required"`
			Properties map[string]any  `json:"properties"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("%s: %v", tool.Name(), err)
		}

		if got.Type != "object" {
			t.Errorf("%s: type = %q, want object", tool.Name(), got.Type)
		}
		if string(got.Required) == "null" {
			t.Errorf("%s: required is null; it must be an array even when empty", tool.Name())
		}
		var required []string
		if err := json.Unmarshal(got.Required, &required); err != nil {
			t.Errorf("%s: required is not an array of strings: %s", tool.Name(), got.Required)
		}
		// A required name that is not among the properties is a schema the model
		// cannot satisfy.
		for _, name := range required {
			if _, ok := got.Properties[name]; !ok {
				t.Errorf("%s: %q is required but not declared as a property", tool.Name(), name)
			}
		}
		if len(got.Properties) == 0 {
			t.Errorf("%s: no properties declared", tool.Name())
		}
	}
}

// The path property must precede the content/edits property on the wire.
// Models tend to emit arguments in the schema's property order, and the
// streaming preview names the in-progress file from the first fragments only
// when the path arrives ahead of the content. (k3, 2026-08: an alphabetical —
// content-first — schema makes it emit the path last.)
func TestFileToolSchemasPutPathFirst(t *testing.T) {
	for _, tool := range []Tool{&Write{}, &Edit{}} {
		raw, err := json.Marshal(tool.InputSchema())
		if err != nil {
			t.Fatalf("%s: %v", tool.Name(), err)
		}
		pathAt := bytes.Index(raw, []byte(`"path"`))
		if pathAt < 0 {
			t.Fatalf("%s: no path property in %s", tool.Name(), raw)
		}
		for _, other := range []string{`"content"`, `"edits"`} {
			if at := bytes.Index(raw, []byte(other)); at >= 0 && at < pathAt {
				t.Errorf("%s: %s precedes \"path\" on the wire: %s", tool.Name(), other, raw)
			}
		}
	}
}
