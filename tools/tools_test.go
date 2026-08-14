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

// workdir is the alternative to a `cd` prefix, so the thing to pin is that it
// actually changes where the process starts — in both spellings, since a relative
// path resolving against the wrong base is the failure mode that produces a
// plausible wrong answer instead of an error.
func TestBashWorkdirRunsTheCommandThere(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "nested", "deeper")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	b := &Bash{Cwd: dir}

	for _, spelling := range []string{filepath.Join("nested", "deeper"), sub} {
		res, err := b.Execute(context.Background(), args(t, map[string]any{
			"command": "pwd", "workdir": spelling,
		}))
		if err != nil {
			t.Fatalf("workdir %q: %v", spelling, err)
		}
		if !strings.Contains(res.Text, "deeper") {
			t.Errorf("workdir %q: pwd = %q, want it inside the nested directory", spelling, res.Text)
		}
		// Recorded as the resolved absolute path, so the card and the transcript name
		// one directory rather than whichever spelling the model happened to use.
		if d := res.Details.(BashDetails); d.Workdir != sub {
			t.Errorf("workdir %q: Details.Workdir = %q, want %q", spelling, d.Workdir, sub)
		}
	}

	// Omitted, and explicitly ".", both mean the session's directory — and neither
	// records a workdir, because a card announcing the default on every call says
	// nothing.
	for _, spelling := range []any{nil, "", "."} {
		a := map[string]any{"command": "pwd"}
		if spelling != nil {
			a["workdir"] = spelling
		}
		res, err := b.Execute(context.Background(), args(t, a))
		if err != nil {
			t.Fatalf("workdir %v: %v", spelling, err)
		}
		if d := res.Details.(BashDetails); d.Workdir != "" {
			t.Errorf("workdir %v: Details.Workdir = %q, want it left empty", spelling, d.Workdir)
		}
	}
}

// A bad workdir must be answered by the tool, not by bash failing to start. The
// difference matters to the model: `chdir: no such file or directory` with exit code
// -1 describes the harness, while a message naming the resolved path and the base it
// was resolved against describes the mistake.
func TestBashWorkdirFailuresAreExplained(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "notadir.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &Bash{Cwd: dir}

	res, err := b.Execute(context.Background(), args(t, map[string]any{
		"command": "pwd", "workdir": "nope",
	}))
	if err == nil {
		t.Fatal("a nonexistent workdir was accepted")
	}
	for _, want := range []string{"does not exist", "nope", dir} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, missing %q", err, want)
		}
	}
	// Refused before anything ran, so there is nothing to report — same contract the
	// guard refusal holds to.
	if res.Details != nil {
		t.Errorf("Details = %+v, want nothing recorded for a command that never ran", res.Details)
	}

	if _, err := b.Execute(context.Background(), args(t, map[string]any{
		"command": "pwd", "workdir": "notadir.txt",
	})); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("workdir pointing at a file = %v, want it to say so", err)
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

// A read of a file a sibling call is rewriting must return the whole file — the
// old content or the new one — and never a prefix of either.
//
// os.WriteFile truncates before it writes, so there is a window in which the file
// on disk is genuinely shorter than both versions. read used to skip the per-path
// lock on the grounds that it does not mutate anything, which covered two writers
// racing each other and left a reader racing a writer wide open. The failure is
// silent: a truncated file is a valid short file, so the model gets a plausible
// answer from content that never existed.
//
// This test has teeth — verified by removing the lock from Read.Execute, which
// fails it within the first handful of iterations on a partial read.
func TestReadNeverObservesAHalfWrittenFile(t *testing.T) {
	dir := t.TempDir()
	// Wide enough that truncate-then-write is observably not atomic, and inside
	// both truncation limits so a clean read is comparable byte for byte.
	const lines = 800
	old := strings.Repeat(strings.Repeat("x", 39)+"\n", lines)
	newer := strings.Repeat(strings.Repeat("y", 39)+"\n", lines)
	write(t, dir, "f.txt", old)

	r := &Read{Cwd: dir}
	w := &Write{Cwd: dir}
	// Built on this goroutine: args calls t.Fatal on bad input, which a helper
	// goroutine may not do.
	readArgs := args(t, map[string]any{"path": "f.txt"})
	writeArgs := [2]json.RawMessage{
		args(t, map[string]any{"path": "f.txt", "content": old}),
		args(t, map[string]any{"path": "f.txt", "content": newer}),
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = w.Execute(context.Background(), writeArgs[i%2])
		}
	}()

	var failure string
	for i := 0; i < 300 && failure == ""; i++ {
		res, err := r.Execute(context.Background(), readArgs)
		switch {
		case err != nil:
			failure = fmt.Sprintf("read %d failed: %v", i, err)
		case res.Text != old && res.Text != newer:
			failure = fmt.Sprintf("read %d saw %d bytes, want the whole file (%d): a write was observed mid-truncation",
				i, len(res.Text), len(old))
		}
	}
	close(stop)
	wg.Wait()

	if failure != "" {
		t.Error(failure)
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

// --- multi-file read ---

// Several paths in one call is one round trip instead of one per file, which is the
// whole reason the parameter exists. The contents have to arrive attributed, in the
// order asked, and under one shared budget rather than one budget each.
func TestReadManyReturnsEveryFileAttributed(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", "package a\n")
	write(t, dir, "b.go", "package b\n")
	write(t, dir, "sub/c.go", "package c\n")
	r := &Read{Cwd: dir}

	res, err := r.Execute(context.Background(), args(t, map[string]any{
		"paths": []string{"a.go", "b.go", "sub/c.go"},
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"==> a.go <==", "package a", "==> b.go <==", "package b",
		"==> sub/c.go <==", "package c"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("output is missing %q:\n%s", want, res.Text)
		}
	}
	// Order as asked, not as the filesystem or a map felt like.
	if a, b := strings.Index(res.Text, "==> a.go <=="), strings.Index(res.Text, "==> b.go <=="); a > b {
		t.Error("files came back out of the order they were asked for")
	}
	d, ok := res.Details.(ReadManyDetails)
	if !ok {
		t.Fatalf("Details = %T, want ReadManyDetails", res.Details)
	}
	if len(d.Files) != 3 {
		t.Fatalf("Details has %d files, want 3", len(d.Files))
	}
	// Not ReadDetails, deliberately: the web UI discriminates on total_lines and its
	// single-file component cannot draw this. See ReadManyDetails.
	if _, wrong := res.Details.(ReadDetails); wrong {
		t.Error("a multi-file read must not report single-file ReadDetails")
	}
}

// The recorded byte ranges are what lets an interface show each file separately
// without parsing the concatenated text back apart, so this asserts the property the
// reader relies on: slicing Text at the range yields that file's content and nothing
// else. Non-ASCII content is in the fixture because the offsets are bytes, and a
// reader that treats them as character indices is wrong in a way a plain ASCII
// fixture would never reveal.
func TestReadManyRecordsWhereEachBodyIs(t *testing.T) {
	dir := t.TempDir()
	bodies := map[string]string{
		// A body that contains what looks like another file's section header: the
		// case that rules out splitting the text on the headers instead.
		"doc.md":   "example:\n==> b.go <==\nnot really b\n",
		"b.go":     "package b\n",
		"cjk.go":   "// 中文注释\npackage cjk\n",
		"emoji.md": "done ✅ 🎉\n",
	}
	order := []string{"doc.md", "b.go", "cjk.go", "emoji.md"}
	for _, p := range order {
		write(t, dir, p, bodies[p])
	}
	r := &Read{Cwd: dir}

	res, err := r.Execute(context.Background(), args(t, map[string]any{"paths": order}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	d := res.Details.(ReadManyDetails)
	if len(d.Files) != len(order) {
		t.Fatalf("Details has %d files, want %d", len(d.Files), len(order))
	}
	for _, f := range d.Files {
		if f.BodyOffset <= 0 || f.BodyLength <= 0 {
			t.Errorf("%s: BodyOffset=%d BodyLength=%d, want a real range", f.Path, f.BodyOffset, f.BodyLength)
			continue
		}
		end := f.BodyOffset + f.BodyLength
		if end > len(res.Text) {
			t.Errorf("%s: range %d..%d runs past the %d-byte text", f.Path, f.BodyOffset, end, len(res.Text))
			continue
		}
		if got := res.Text[f.BodyOffset:end]; got != bodies[f.Path] {
			t.Errorf("%s: text[%d:%d] = %q, want %q", f.Path, f.BodyOffset, end, got, bodies[f.Path])
		}
	}
}

// A file that could not be read gets no range, which is how a reader tells "show the
// error" from "show the content". A truncated one gets a range around the content
// only: the note after it is addressed to the model, and rendering it as part of the
// file would put a sentence about budgets inside someone's source.
func TestReadManyBodyRangeExcludesTheErrorAndTheNote(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "ok.txt", "fine\n")
	write(t, dir, "big.txt", strings.Repeat("line\n", MaxLines))
	r := &Read{Cwd: dir}

	res, err := r.Execute(context.Background(), args(t, map[string]any{
		"paths": []string{"ok.txt", "gone.txt", "big.txt"},
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	d := res.Details.(ReadManyDetails)

	if f := d.Files[1]; f.Error == "" || f.BodyOffset != 0 || f.BodyLength != 0 {
		t.Errorf("unreadable path: %+v, want an error and no range", f)
	}
	big := d.Files[2]
	if !big.Truncated {
		t.Fatalf("big.txt was not truncated: %+v", big)
	}
	body := res.Text[big.BodyOffset : big.BodyOffset+big.BodyLength]
	if strings.Contains(body, "budget split") {
		t.Errorf("the truncation note is inside the recorded body:\n%s", body)
	}
	if !strings.HasPrefix(body, "line\n") {
		t.Errorf("body does not start at the file's first line: %q", body[:min(20, len(body))])
	}
	// And the note is still in the text, for the model, immediately after the range.
	if !strings.Contains(res.Text[big.BodyOffset+big.BodyLength:], "budget split") {
		t.Error("the truncation note is missing from the text the model reads")
	}
}

// One unreadable path must not waste the round trip the call existed to save.
func TestReadManySurvivesOneBadPath(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", "package a\n")
	r := &Read{Cwd: dir}

	res, err := r.Execute(context.Background(), args(t, map[string]any{
		"paths": []string{"a.go", "gone.go"},
	}))
	if err != nil {
		t.Fatalf("Execute: %v, want the good file back", err)
	}
	if !strings.Contains(res.Text, "package a") {
		t.Errorf("the readable file is missing:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "[error:") {
		t.Errorf("the failure is not reported:\n%s", res.Text)
	}
	d := res.Details.(ReadManyDetails)
	if d.Files[1].Error == "" {
		t.Error("Details does not record which file failed")
	}
	// All of them failing is a different answer: nothing was read, so the model has
	// to fix the paths rather than reason about empty sections.
	if _, err := r.Execute(context.Background(), args(t, map[string]any{
		"paths": []string{"nope.go", "also-nope.go"},
	})); err == nil {
		t.Error("a call where every path failed returned success")
	}
}

// The budget is divided, not repeated. Five files at the full ceiling each is five
// times the limit this package exists to enforce.
func TestReadManyDividesTheBudget(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("line\n", MaxLines)
	write(t, dir, "a.txt", big)
	write(t, dir, "b.txt", big)
	r := &Read{Cwd: dir}

	res, err := r.Execute(context.Background(), args(t, map[string]any{
		"paths": []string{"a.txt", "b.txt"},
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	d := res.Details.(ReadManyDetails)
	for _, f := range d.Files {
		if !f.Truncated {
			t.Errorf("%s was not truncated; each file must fit its share, not the whole limit", f.Path)
		}
		if f.ShownLines > MaxLines/2 {
			t.Errorf("%s shows %d lines, want at most half of %d", f.Path, f.ShownLines, MaxLines)
		}
	}
	// Truncation has to say how to get the rest, and it has to say it with path:
	// offset is refused alongside paths.
	if !strings.Contains(res.Text, "offset=") || !strings.Contains(res.Text, "path=") {
		t.Errorf("a truncated file does not say how to continue:\n%s", res.Text[:min(400, len(res.Text))])
	}
}

// The one-or-many argument has exactly one meaning per call, and every refusal has to
// name the fix — a model that cannot tell what to change tries the same thing again.
func TestReadRefusesAmbiguousArguments(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", "package a\n")
	r := &Read{Cwd: dir}

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"both", map[string]any{"path": "a.go", "paths": []string{"a.go"}}, "not both"},
		{"neither", map[string]any{}, "required"},
		{"offset with paths", map[string]any{"paths": []string{"a.go"}, "offset": 2}, "cannot be used with paths"},
		{"empty path in paths", map[string]any{"paths": []string{"a.go", ""}}, "empty string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.Execute(context.Background(), args(t, tc.args))
			if err == nil {
				t.Fatal("Execute succeeded, want a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}

	// A single-element paths array is not ambiguous, and takes the single-file path
	// so that the UI still gets ReadDetails.
	res, err := r.Execute(context.Background(), args(t, map[string]any{"paths": []string{"a.go"}}))
	if err != nil {
		t.Fatalf("Execute with one path: %v", err)
	}
	if _, ok := res.Details.(ReadDetails); !ok {
		t.Errorf("Details = %T, want ReadDetails for a one-file call however it was spelled", res.Details)
	}

	// A repeated path is read once: the second copy says nothing and would be
	// charged twice against a budget the other files are sharing.
	res, err = r.Execute(context.Background(), args(t, map[string]any{
		"paths": []string{"a.go", "a.go"},
	}))
	if err != nil {
		t.Fatalf("Execute with a duplicate: %v", err)
	}
	if _, ok := res.Details.(ReadDetails); !ok {
		t.Errorf("Details = %T: a duplicated single path should collapse to one file", res.Details)
	}
}
