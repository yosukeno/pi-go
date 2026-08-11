package diff

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// reconstruct rebuilds both sides from the chunks. If this ever disagrees with
// the inputs, the edit script is wrong regardless of how it looks.
func reconstruct(chunks []Chunk) (oldSide, newSide []string) {
	for _, c := range chunks {
		switch c.Kind {
		case Equal:
			oldSide = append(oldSide, c.Lines...)
			newSide = append(newSide, c.Lines...)
		case Delete:
			oldSide = append(oldSide, c.Lines...)
		case Insert:
			newSide = append(newSide, c.Lines...)
		}
	}
	return oldSide, newSide
}

func TestLinesRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
	}{
		{"identical", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"empty to content", nil, []string{"a", "b"}},
		{"content to empty", []string{"a", "b"}, nil},
		{"both empty", nil, nil},
		{"append", []string{"a"}, []string{"a", "b"}},
		{"prepend", []string{"a"}, []string{"b", "a"}},
		{"middle change", []string{"a", "b", "c"}, []string{"a", "x", "c"}},
		{"delete middle", []string{"a", "b", "c"}, []string{"a", "c"}},
		{"full replace", []string{"a", "b"}, []string{"x", "y"}},
		{"repeated lines", []string{"x", "x", "x"}, []string{"x", "x"}},
		{"reorder", []string{"a", "b", "c"}, []string{"c", "b", "a"}},
		{"interleaved", []string{"a", "1", "b", "2", "c"}, []string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chunks := Lines(tc.a, tc.b)
			gotOld, gotNew := reconstruct(chunks)
			if !equal(gotOld, tc.a) {
				t.Errorf("old side = %q, want %q (chunks %+v)", gotOld, tc.a, chunks)
			}
			if !equal(gotNew, tc.b) {
				t.Errorf("new side = %q, want %q (chunks %+v)", gotNew, tc.b, chunks)
			}
		})
	}
}

// A minimal edit inside a big file must produce a minimal script, not a wholesale
// replacement. This is the property the prefix/suffix trim exists for.
func TestLinesIsMinimalOnLargeFile(t *testing.T) {
	a := make([]string, 3000)
	for i := range a {
		a[i] = fmt.Sprintf("line %d", i)
	}
	b := append([]string(nil), a...)
	b[1500] = "changed"

	chunks := Lines(a, b)
	var added, removed int
	for _, c := range chunks {
		switch c.Kind {
		case Insert:
			added += len(c.Lines)
		case Delete:
			removed += len(c.Lines)
		}
	}
	if added != 1 || removed != 1 {
		t.Errorf("added=%d removed=%d, want 1/1: the trim did not narrow the search", added, removed)
	}
}

// Beyond maxMyersLines the region is reported as one replacement rather than
// spending unbounded time and memory on a prettier answer.
func TestLargeUnrelatedRegionFallsBack(t *testing.T) {
	n := maxMyersLines + 10
	a := make([]string, n)
	b := make([]string, n)
	for i := range a {
		a[i] = fmt.Sprintf("a%d", i)
		b[i] = fmt.Sprintf("b%d", i)
	}
	chunks := Lines(a, b)
	if len(chunks) != 2 || chunks[0].Kind != Delete || chunks[1].Kind != Insert {
		t.Fatalf("want a single delete+insert pair, got %d chunks", len(chunks))
	}
	gotOld, gotNew := reconstruct(chunks)
	if !equal(gotOld, a) || !equal(gotNew, b) {
		t.Error("fallback lost content")
	}
}

func TestStat(t *testing.T) {
	added, removed := Stat("a\nb\nc\n", "a\nx\ny\nc\n")
	if added != 2 || removed != 1 {
		t.Errorf("added=%d removed=%d, want 2/1", added, removed)
	}
}

func TestDisplayReportsFirstChangedLine(t *testing.T) {
	old := "a\nb\nc\nd\n"
	newer := "a\nb\nX\nd\n"
	text, first := Display(old, newer, DefaultContext)
	if first != 3 {
		t.Errorf("firstChanged = %d, want 3", first)
	}
	if !strings.Contains(text, "-3 c") || !strings.Contains(text, "+3 X") {
		t.Errorf("expected both sides of the change, got:\n%s", text)
	}
}

func TestDisplayCollapsesDistantContext(t *testing.T) {
	var oldB, newB strings.Builder
	for i := range 100 {
		fmt.Fprintf(&oldB, "line %d\n", i)
		if i == 50 {
			newB.WriteString("changed\n")
			continue
		}
		fmt.Fprintf(&newB, "line %d\n", i)
	}
	text, _ := Display(oldB.String(), newB.String(), DefaultContext)

	lines := strings.Count(text, "\n") + 1
	if lines > 12 {
		t.Errorf("expected a compact diff, got %d lines:\n%s", lines, text)
	}
	if strings.Contains(text, "line 10") {
		t.Error("a line far from the change leaked into the output")
	}
	if !strings.Contains(text, "line 48") {
		t.Errorf("nearby context is missing:\n%s", text)
	}
}

func TestDisplayIdenticalIsEmpty(t *testing.T) {
	text, first := Display("a\nb\n", "a\nb\n", DefaultContext)
	if text != "" || first != 0 {
		t.Errorf("want empty diff, got text=%q first=%d", text, first)
	}
}

// The strongest available check: hand the patch to git and let it decide.
func TestUnifiedPatchAppliesWithGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	cases := []struct{ name, old, new string }{
		{"single line", "a\nb\nc\n", "a\nX\nc\n"},
		{"insert", "a\nb\n", "a\nnew\nb\n"},
		{"delete", "a\nb\nc\n", "a\nc\n"},
		{"append at end", "a\nb\n", "a\nb\nc\n"},
		{"change at start", "a\nb\nc\n", "X\nb\nc\n"},
		{"two distant changes", lines(1, 40, map[int]string{3: "X", 35: "Y"}), lines(1, 40, map[int]string{3: "XX", 35: "YY"})},
		{"two adjacent changes", "a\nb\nc\nd\ne\n", "a\nB\nc\nD\ne\n"},
		{"whole file", "a\nb\n", "x\ny\nz\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			run := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
				}
			}
			run("init", "-q")

			const name = "f.txt"
			if err := os.WriteFile(filepath.Join(dir, name), []byte(tc.old), 0o644); err != nil {
				t.Fatal(err)
			}

			patch := Unified(name, tc.old, tc.new, DefaultContext)
			patchPath := filepath.Join(dir, "p.diff")
			if err := os.WriteFile(patchPath, []byte(patch), 0o644); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command("git", "apply", "-p1", "p.diff")
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git apply rejected the patch: %v\n%s\n--- patch ---\n%s", err, out, patch)
			}

			got, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.new {
				t.Errorf("applied result mismatch\ngot:\n%q\nwant:\n%q\n--- patch ---\n%s", got, tc.new, patch)
			}
		})
	}
}

// lines builds a numbered file with the given 1-based overrides.
func lines(from, to int, override map[int]string) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		if s, ok := override[i]; ok {
			b.WriteString(s + "\n")
			continue
		}
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
