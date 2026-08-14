package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The guard's job is to remove git from a subagent's reach, because git is the one
// tool that can act on the shared repository without naming a path outside the
// worktree. Both known levers are in this table, and both are the reason the answer
// is "no git" rather than "no `git -C`".
func TestGuardRefusesTheWaysOutOfAWorktree(t *testing.T) {
	g := &Guard{Worktree: "/wt/sub1", MainCheckout: "/home/me/proj"}
	denied := map[string]string{
		// Plain git: enough on its own, because `git commit` after a rewritten
		// .git lands on the main branch.
		"git status":       "git is not available",
		"git commit -m x":  "git is not available",
		"/usr/bin/git log": "git is not available",
		"ls && git push":   "git is not available",
		"echo hi; git config core.hooksPath /tmp": "git is not available",
		// Redirects that need no path at all.
		"GIT_DIR=/home/me/proj/.git git log": "git is not available",
		"env GIT_WORK_TREE=/elsewhere true":  "GIT_WORK_TREE",
		// Naming the main checkout: a subagent has no legitimate reason to.
		"cat /home/me/proj/main.go": "main checkout",
		"cp x /home/me/proj/x":      "main checkout",
		// Leaving the worktree outright.
		"cd /etc && ls": "would leave your worktree",
	}
	for cmd, want := range denied {
		err := g.Check(cmd)
		if err == nil {
			t.Errorf("Check(%q) = nil, want a refusal", cmd)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Check(%q) = %v, want it to mention %q", cmd, err, want)
		}
		// Every refusal has to tell the model what to do instead, or it just
		// tries the same thing again.
		if !strings.Contains(err.Error(), "refused:") {
			t.Errorf("Check(%q) = %v, want the refused command quoted back", cmd, err)
		}
	}
}

// Under shared isolation the ban is unchanged and the reason is not: there is no
// worktree and nothing is committed for the child, so the default wording would
// tell it to expect a commit that never arrives. It would then report success on
// work it believes was recorded.
//
// The check that the mode did not weaken anything is the first assertion here. If
// anything the ban matters more in this mode — a stray `git commit` lands in the
// user's own repository, on their branch, mixed into their uncommitted work.
func TestGuardRefusesGitWithATrueReasonWhenShared(t *testing.T) {
	shared := &Guard{Worktree: "/home/me/proj", Shared: true}
	err := shared.Check("git commit -am wip")
	if err == nil {
		t.Fatal("Check(git commit) = nil under shared isolation, want the same refusal")
	}
	for _, want := range []string{"git is not available", "already in place", "refused:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Check = %v, missing %q", err, want)
		}
	}
	// The promise that does not hold here.
	for _, wrong := range []string{"isolated worktree", "committed for you"} {
		if strings.Contains(err.Error(), wrong) {
			t.Errorf("Check = %v, repeats %q, which is false under shared isolation", err, wrong)
		}
	}
	// And the default keeps saying the thing that is true there.
	if e := (&Guard{Worktree: "/wt/sub1"}).Check("git commit -am wip"); e == nil ||
		!strings.Contains(e.Error(), "committed for you") {
		t.Errorf("worktree guard = %v, want it to still promise the commit", e)
	}
}

// The work a subagent is actually for must go through untouched. A guard that
// blocks `go test` has removed the reason to have a subagent at all.
func TestGuardAllowsTheWorkItself(t *testing.T) {
	g := &Guard{Worktree: "/wt/sub1", MainCheckout: "/home/me/proj"}
	for _, cmd := range []string{
		"go test ./...",
		"go build ./... && go vet ./...",
		"npm test",
		"ls -la",
		"grep -rn TODO .",
		"cd ./internal && go test .",
		"cd /wt/sub1/pkg && go test .",
		// "digit" contains "git" as a substring; word matching must not be fooled
		// into refusing ordinary commands.
		"echo digital",
		"cat legit.txt",
	} {
		if err := g.Check(cmd); err != nil {
			t.Errorf("Check(%q) = %v, want it allowed", cmd, err)
		}
	}
}

// A nil guard is the terminal and web case: bash stays exactly as unrestricted as
// it has always been, which is documented behaviour rather than an oversight.
func TestNilGuardAllowsEverything(t *testing.T) {
	var g *Guard
	if err := g.Check("git push --force"); err != nil {
		t.Errorf("a nil guard refused %v; top-level bash must not change", err)
	}
}

// The guard has to be wired into bash, not merely exist. This runs the tool.
func TestBashRefusesGuardedCommandsBeforeRunning(t *testing.T) {
	dir := t.TempDir()
	b := &Bash{Cwd: dir, Guard: &Guard{Worktree: dir, MainCheckout: "/home/me/proj"}}
	args, err := json.Marshal(bashArgs{Command: "git init /tmp/should-not-happen"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := b.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("bash ran a command the guard refuses")
	}
	if !strings.Contains(err.Error(), "git is not available") {
		t.Errorf("error = %v, want the guard's message", err)
	}
	// Refused before anything ran, so there is no exit code and no output to show.
	if res.Details != nil {
		t.Errorf("Details = %+v, want nothing recorded for a command that never ran", res.Details)
	}
}

// bash's workdir parameter reaches the same place a `cd` does, without appearing in
// the command text the guard parses. So adding it without this check would have
// handed a subagent a way out of its worktree that reads as an ordinary argument —
// which is the whole reason CheckDir exists, and why this test runs the tool rather
// than calling the guard directly.
func TestBashWorkdirCannotLeaveTheWorktree(t *testing.T) {
	wt := t.TempDir()
	main := t.TempDir()
	if err := os.MkdirAll(filepath.Join(main, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(wt, "pkg")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	b := &Bash{Cwd: wt, Guard: &Guard{Worktree: wt, MainCheckout: main}}

	for _, c := range []struct {
		workdir string
		want    string
	}{
		// The escape the `cd` check would have caught in the command text.
		{os.TempDir(), "outside your worktree"},
		{"..", "outside your worktree"},
		// And the one it would not: naming the repository the worktree came from.
		{main, "main checkout"},
		{filepath.Join(main, "src"), "main checkout"},
	} {
		res, err := b.Execute(context.Background(), args(t, map[string]any{
			"command": "pwd", "workdir": c.workdir,
		}))
		if err == nil {
			t.Errorf("workdir %q was accepted", c.workdir)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("workdir %q: error = %v, want it to mention %q", c.workdir, err, c.want)
		}
		if res.Details != nil {
			t.Errorf("workdir %q: Details = %+v, want nothing for a refused call", c.workdir, res.Details)
		}
	}

	// The work the parameter is for still goes through: a directory inside the
	// child's own worktree.
	if _, err := b.Execute(context.Background(), args(t, map[string]any{
		"command": "pwd", "workdir": "pkg",
	})); err != nil {
		t.Errorf("a workdir inside the worktree was refused: %v", err)
	}

	// And a top-level session is unrestricted, which is the documented existing
	// behaviour of bash and must not change because a guarded path grew a check.
	plain := &Bash{Cwd: wt}
	if _, err := plain.Execute(context.Background(), args(t, map[string]any{
		"command": "pwd", "workdir": main,
	})); err != nil {
		t.Errorf("unguarded bash refused a workdir outside its cwd: %v", err)
	}
}

// The two sides of the fork are asymmetric, and the asymmetry is the safety
// property: a child has no way to delegate further and no unrestricted bash.
func TestChildRegistryHasNoSubagentAndAGuardedBash(t *testing.T) {
	dir := t.TempDir()
	child := New(Options{
		Cwd:   dir,
		Guard: &Guard{Worktree: dir, MainCheckout: "/home/me/proj"},
	})
	if _, ok := child.Get("subagent"); ok {
		t.Error("a child registry has the subagent tool; nesting must be structurally bounded")
	}
	if n := len(child.All()); n != 7 {
		t.Errorf("child has %d tools, want the 7 built-ins", n)
	}
	bash, ok := child.Get("bash")
	if !ok {
		t.Fatal("child has no bash; it could not run tests")
	}
	if bash.(*Bash).Guard == nil {
		t.Error("the child's bash is unguarded")
	}

	parent := New(Options{Cwd: dir, Subagent: &Subagent{Cwd: dir}})
	if _, ok := parent.Get("subagent"); !ok {
		t.Error("a parent registry is missing the subagent tool")
	}
	if n := len(parent.All()); n != 8 {
		t.Errorf("parent has %d tools, want 8", n)
	}
	pb, _ := parent.Get("bash")
	if pb.(*Bash).Guard != nil {
		t.Error("a top-level bash grew a guard; that would change existing behaviour")
	}
}

// An explore child's isolation is the absence of the tools, not a rule about them.
// This is what lets that mode skip the worktree, so if a mutating tool ever appears
// in a read-only registry the justification for running in the parent's own
// directory disappears with it.
func TestReadOnlyRegistryHasNothingThatCanWrite(t *testing.T) {
	dir := t.TempDir()
	child := New(Options{
		Cwd:      dir,
		ReadOnly: true,
		// Offered and expected to be ignored: a read-only child must not be able to
		// spawn a writing one, whatever the caller passes.
		Subagent: &Subagent{Cwd: dir},
	})
	for _, name := range []string{"write", "edit", "bash", "subagent"} {
		if _, ok := child.Get(name); ok {
			t.Errorf("a read-only child has %q; it can change things and must not run in the parent's directory", name)
		}
	}
	for _, name := range []string{"read", "ls", "find", "grep"} {
		if _, ok := child.Get(name); !ok {
			t.Errorf("a read-only child is missing %q; it could not answer anything", name)
		}
	}
	if n := len(child.All()); n != 4 {
		t.Errorf("read-only child has %d tools, want exactly the 4 read-only ones", n)
	}
}
