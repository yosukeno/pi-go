package tools

import (
	"context"
	"os"
	"strings"
	"testing"
)

// bash spills output that did not fit into a temp file and names it in the result.
// That note is the only thing telling the model its view was cut short, so it is
// the last place in the tool allowed to be wrong.

// The success path is asserted by reading the file back. The note promises the
// full output, so the file has to hold all of it rather than the truncated view.
func TestBashSpillsTheCompleteOutputAndNamesWhereItWent(t *testing.T) {
	b := &Bash{Cwd: t.TempDir()}
	res, err := b.Execute(context.Background(), args(t, bashArgs{Command: "seq 1 2500"}))
	if err != nil {
		t.Fatal(err)
	}
	d, ok := res.Details.(BashDetails)
	if !ok {
		t.Fatalf("details are %T, want BashDetails", res.Details)
	}
	if !d.Truncated {
		t.Fatalf("2500 lines should not have fit in %d", MaxLines)
	}
	if d.FullOutputPath == "" {
		t.Fatal("no spill path reported for a truncated result")
	}
	t.Cleanup(func() { os.Remove(d.FullOutputPath) })

	// The two channels have to agree: the model reads the note, the interface reads
	// the details, and a path in one but not the other is a silent disagreement.
	if !strings.Contains(res.Text, d.FullOutputPath) {
		t.Error("the note does not name the file the details point at")
	}

	full := read(t, d.FullOutputPath)
	if got := strings.Count(full, "\n"); got != 2500 {
		t.Errorf("the spill file holds %d lines, want 2500", got)
	}
	if !strings.HasPrefix(full, "1\n") {
		t.Error("the spill file does not start at the beginning of the output")
	}
	// The distinction worth pinning: the file is the whole output, not a second copy
	// of the tail the model already has.
	if len(full) <= len(res.Text) {
		t.Errorf("spill file is %d bytes and the truncated result is %d: it is not the full output",
			len(full), len(res.Text))
	}
}

// When the output cannot be saved, no path may be reported. Until this was fixed
// every error here was discarded and the path handed over anyway, so a full disk
// produced a note promising the complete output at a file holding nothing — or
// holding part of it, which is worse, because the model can read that one and get a
// confident answer out of a fragment it was told was whole.
func TestBashReportsNoPathWhenTheOutputCannotBeSaved(t *testing.T) {
	dir := t.TempDir()
	// A regular file as the temp directory, so no temp file can be created there.
	// os.CreateTemp consults os.TempDir on every call, which reads TMPDIR each time.
	t.Setenv("TMPDIR", write(t, dir, "not-a-directory", ""))

	b := &Bash{Cwd: dir}
	res, err := b.Execute(context.Background(), args(t, bashArgs{Command: "seq 1 2500"}))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Details.(BashDetails)
	if !d.Truncated {
		t.Fatal("2500 lines should not have fit")
	}
	if d.FullOutputPath != "" {
		t.Errorf("reported %q although nothing was written there", d.FullOutputPath)
	}
	if strings.Contains(res.Text, "Full output") {
		t.Errorf("the note still promises full output:\n%s", noteOf(res.Text))
	}
	if !strings.Contains(res.Text, "could not be saved") {
		t.Errorf("the note does not say the rest is unavailable:\n%s", noteOf(res.Text))
	}
	// The truncation itself must still be announced. Losing the spill is a degraded
	// result; losing the fact that output was cut would be a wrong one, because the
	// model would read a partial tail as the whole answer.
	if !strings.Contains(res.Text, "of 2500") {
		t.Errorf("the note no longer says how much was cut:\n%s", noteOf(res.Text))
	}
}

// spill reports a path only together with a complete file, so callers can treat a
// non-empty path as a promise. Tested directly because Bash.render only ever asks
// the question one way round.
func TestSpillReportsNothingWhenItCannotWrite(t *testing.T) {
	dir := t.TempDir()
	if path, ok := spill("payload"); !ok || path == "" {
		t.Fatalf("spill to a usable temp dir failed: path=%q ok=%v", path, ok)
	} else {
		t.Cleanup(func() { os.Remove(path) })
		if got := read(t, path); got != "payload" {
			t.Errorf("spilled %q, want %q", got, "payload")
		}
	}

	t.Setenv("TMPDIR", write(t, dir, "not-a-directory", ""))
	path, ok := spill("payload")
	if ok {
		t.Error("spill claimed success with no writable temp directory")
	}
	if path != "" {
		t.Errorf("spill returned %q alongside a failure", path)
	}
}

// noteOf returns the trailing bracketed note, so a failure message shows the part
// under test instead of two thousand lines of seq output.
func noteOf(text string) string {
	if i := strings.LastIndex(text, "["); i >= 0 {
		return text[i:]
	}
	return text
}
