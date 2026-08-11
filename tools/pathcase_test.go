package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The path guard compares canonical paths as bytes, and canonical does not
// normalise case. On a case-insensitive volume that refused legal paths: one
// directory has two spellings there, and only one of them matched.
//
// Every test here probes the volume instead of switching on runtime.GOOS. A
// case-sensitive volume can be mounted on macOS and a case-insensitive one on
// Linux, and what the guard should do depends on the volume, not the OS.

// caseFolding reports whether dir's filesystem treats two spellings of one name as
// the same file.
func caseFolding(t *testing.T, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, "casefoldprobe")
	if err := os.Mkdir(probe, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(probe)
	a, err := os.Stat(probe)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(filepath.Join(dir, "CASEFOLDPROBE"))
	if err != nil {
		return false
	}
	return os.SameFile(a, b)
}

// The symptom, exercised through the tools rather than the predicate: a model that
// spells the workspace in another case used to be told its own project file was
// outside the working directory. Both halves are asserted because a read that works
// while write still refuses is the shape that made this hard to recognise.
func TestToolsAcceptTheWorkspaceSpelledInAnotherCase(t *testing.T) {
	tmp := t.TempDir()
	if !caseFolding(t, tmp) {
		t.Skip("case-sensitive volume: the two spellings are genuinely different directories here")
	}
	root := filepath.Join(tmp, "Workspace")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, root, "f.txt", "hello\n")

	// One directory, the other spelling. Built lexically, so this is the string a
	// model would produce and not something already resolved for it.
	other := filepath.Join(tmp, "workspace")

	r := &Read{Cwd: root}
	if _, err := r.Execute(context.Background(), args(t, map[string]any{
		"path": filepath.Join(other, "f.txt"),
	})); err != nil {
		t.Errorf("read through an equivalent spelling was refused: %v", err)
	}

	w := &Write{Cwd: root}
	if _, err := w.Execute(context.Background(), args(t, map[string]any{
		"path": filepath.Join(other, "new.txt"), "content": "x\n",
	})); err != nil {
		t.Errorf("write through an equivalent spelling was refused: %v", err)
	}
	// And it landed in the one real directory, not somewhere new.
	if got := read(t, filepath.Join(root, "new.txt")); got != "x\n" {
		t.Errorf("new.txt = %q, want %q", got, "x\n")
	}
}

// The fold fallback must still require a component boundary. Without it a sibling
// whose name merely begins with the root's would be let in — the classic prefix
// mistake, reintroduced one layer down where the original test would not see it.
//
// Independent of the volume: these two names differ in length, so no filesystem
// makes them the same directory.
func TestPathGuardRefusesASiblingThatOnlySharesThePrefix(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "app")
	sibling := filepath.Join(tmp, "APPLE")
	for _, d := range []string{root, sibling} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if within(filepath.Join(sibling, "x.txt"), root) {
		t.Error("a sibling that folds to a superstring of the root was accepted as inside it")
	}
	if foldWithin(filepath.Join(sibling, "x.txt"), root) {
		t.Error("foldWithin matched without a separator at the boundary")
	}
}

// On a case-sensitive volume two directories may differ only in case, and they are
// two directories. Folding the comparison rather than asking the filesystem would
// have accepted one as the other, which is a hole in the only check keeping the
// tools inside the workspace.
func TestPathGuardKeepsDistinctCaseVariantsApart(t *testing.T) {
	tmp := t.TempDir()
	if caseFolding(t, tmp) {
		t.Skip("case-insensitive volume: two such directories cannot both exist here")
	}
	lower := filepath.Join(tmp, "proj")
	upper := filepath.Join(tmp, "PROJ")
	for _, d := range []string{lower, upper} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The names fold together, so the cheap check passes and only the filesystem can
	// settle it. Asserted so a future change from SameFile back to string folding
	// fails here rather than silently.
	if !foldWithin(filepath.Join(upper, "x.txt"), lower) {
		t.Fatal("these names should fold together; the test is not reaching the filesystem check")
	}
	if within(filepath.Join(upper, "x.txt"), lower) {
		t.Error("a different directory was accepted as the workspace because the names fold together")
	}
}

// A prefix that does not resolve is not the same as anything. This keeps the
// fallback's failure direction identical to the rest of the guard, and it is what
// stops a folded spelling from granting access to a directory that is simply absent.
func TestPathGuardRefusesWhenTheFoldedPrefixDoesNotExist(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "gone") // deliberately never created
	if within(filepath.Join(tmp, "GONE", "x.txt"), root) {
		t.Error("accepted a path under a root that does not exist")
	}
	if sameDir(filepath.Join(tmp, "GONE"), root) {
		t.Error("sameDir called two unresolvable paths equal")
	}
}

// The exact-spelling answers must not have moved. The fallback is meant to sit
// behind the byte comparison, not to replace it.
func TestPathGuardIsUnchangedForExactSpellings(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "proj")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if !within(root, root) {
		t.Error("the root is not inside itself")
	}
	if !within(filepath.Join(root, "sub", "f.go"), root) {
		t.Error("a plain child was refused")
	}
	if within(tmp, root) {
		t.Error("the parent was accepted as inside its own child")
	}
	if within(filepath.Join(tmp, "elsewhere", "f.go"), root) {
		t.Error("an unrelated sibling was accepted")
	}
}

// The guard shares the predicate, so it had the same defect: a cd typed in another
// case was refused with a message naming two paths a reader would call identical.
func TestGuardAllowsCdToTheWorktreeInAnotherCase(t *testing.T) {
	tmp := t.TempDir()
	if !caseFolding(t, tmp) {
		t.Skip("case-sensitive volume: the two spellings are genuinely different directories here")
	}
	wt := filepath.Join(tmp, "Worktree")
	if err := os.Mkdir(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	g := &Guard{Worktree: wt}

	if err := g.Check("cd " + filepath.Join(tmp, "worktree") + " && go test ./..."); err != nil {
		t.Errorf("cd to the worktree under an equivalent spelling was refused: %v", err)
	}
	// The refusal that has to survive: the parent is not the worktree under any
	// spelling, and widening for case must not widen for containment.
	if err := g.Check("cd " + tmp + " && ls"); err == nil {
		t.Error("cd out of the worktree was allowed")
	}
}
