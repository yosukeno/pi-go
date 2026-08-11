package tools

import (
	"context"
	"encoding/json"
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
