package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pi-go/worktree"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// listingFixture is a repository with an ignored dependency directory and one
// isolated worktree, which is the situation the listing has to describe.
func listingFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	t.Setenv("PIGO_WORKTREE_DIR", filepath.Join(t.TempDir(), "wt"))

	gitRun(t, root, "init", "-q", "-b", "main", ".")
	gitRun(t, root, "config", "user.email", "t@e")
	gitRun(t, root, "config", "user.name", "t")
	write(t, root, ".gitignore", "node_modules/\n.venv/\n")
	write(t, root, "main.go", "package main\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-qm", "init")
	write(t, root, "node_modules/dep/i.js", "x\n")
	write(t, root, ".venv/bin/python", "x\n")
	return root
}

func mustOpen(t *testing.T, root string) *worktree.Repo {
	t.Helper()
	repo, err := worktree.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// What an isolated checkout is missing decided whether a subagent's first command
// worked, and until now only the agent inside could see it. The person debugging
// "why did the tests fail in there" could not.
func TestWorktreeListingShowsWhatIsMissing(t *testing.T) {
	root := listingFixture(t)
	repo := mustOpen(t, root)
	tree, err := repo.Add("w1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	var out bytes.Buffer
	if err := worktreeCommand(&out, root, false); err != nil {
		t.Fatalf("worktreeCommand: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "w1") {
		t.Errorf("listing does not name the worktree:\n%s", got)
	}
	for _, want := range []string{"missing:", "node_modules/", ".venv/"} {
		if !strings.Contains(got, want) {
			t.Errorf("listing is missing %q:\n%s", want, got)
		}
	}
	// The legend names the file that fixes it, or the reader learns there is a
	// problem and not what to do about it.
	if !strings.Contains(got, ".worktreeinclude") {
		t.Errorf("listing does not say how to fix it:\n%s", got)
	}

	// Carried in means not missing: a listing that reports something the agent can
	// actually see would send the reader looking for the wrong cause.
	write(t, root, ".worktreeinclude", "node_modules\n")
	_ = tree.Remove(true)
	if _, err := repo.Add("w2"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	out.Reset()
	if err := worktreeCommand(&out, root, false); err != nil {
		t.Fatalf("worktreeCommand: %v", err)
	}
	got = out.String()
	if strings.Contains(got, "node_modules/") {
		t.Errorf("node_modules was carried but still reported missing:\n%s", got)
	}
	if !strings.Contains(got, ".venv/") {
		t.Errorf(".venv is still absent and should still be reported:\n%s", got)
	}
}

// The listing answers the question prune answers, before prune runs. Being told
// afterwards that three worktrees were "kept because they hold work" is less useful
// than being able to see which ones.
func TestWorktreeListingSaysWhichOnesHoldWork(t *testing.T) {
	root := listingFixture(t)
	repo := mustOpen(t, root)
	if _, err := repo.Add("empty"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	busy, err := repo.Add("busy")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	write(t, busy.Dir, "unsaved.txt", "work in progress\n")

	var out bytes.Buffer
	if err := worktreeCommand(&out, root, false); err != nil {
		t.Fatalf("worktreeCommand: %v", err)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		switch {
		case strings.Contains(line, "busy") && !strings.Contains(line, "holds work"):
			t.Errorf("a worktree with uncommitted changes is not flagged: %q", line)
		case strings.Contains(line, "empty") && strings.Contains(line, "holds work"):
			t.Errorf("a clean worktree is flagged as holding work: %q", line)
		}
	}
}

// A clean project has nothing to report, and the legend must not appear either:
// explaining a column that has no entries is noise.
func TestWorktreeListingStaysQuietWithNothingToSay(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	t.Setenv("PIGO_WORKTREE_DIR", filepath.Join(t.TempDir(), "wt"))
	gitRun(t, root, "init", "-q", "-b", "main", ".")
	gitRun(t, root, "config", "user.email", "t@e")
	gitRun(t, root, "config", "user.name", "t")
	write(t, root, "main.go", "package main\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-qm", "init")

	var out bytes.Buffer
	if err := worktreeCommand(&out, root, false); err != nil {
		t.Fatalf("worktreeCommand: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "no worktrees besides the main checkout") {
		t.Errorf("listing does not say the set is empty:\n%s", got)
	}
	for _, unwanted := range []string{"missing:", ".worktreeinclude", "holds work"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("listing mentions %q with nothing to report:\n%s", unwanted, got)
		}
	}
}
