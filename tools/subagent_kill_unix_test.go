//go:build unix

package tools

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A child that never finishes is stopped, and its process group goes with it —
// otherwise a `go test` it started outlives the turn that gave up on it.
//
// Unix-tagged, and not because syscall.Kill happens to be the way it asks. The
// guarantee it checks is a process-group guarantee, and setProcessGroup is a
// deliberate no-op off Unix (see procgroup_other.go), so on Windows this would be
// asserting something the package does not claim. Splitting it out is also what
// lets `go vet ./...` and `go test` pass for GOOS=windows — vet type-checks test
// files, so one unguarded syscall.Kill in a _test.go file fails the whole package
// even when the library builds.
func TestSubagentTimeoutKillsTheChild(t *testing.T) {
	root, stub := repoFixture(t)
	// Outside the worktree, which is reclaimed as soon as the run ends.
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	t.Setenv("STUB_MODE", "hang")
	t.Setenv("STUB_PIDFILE", pidFile)
	// Two seconds, and not the 300ms this used to be. The budget has to cover a real
	// `git worktree add`, a process spawn and a shell start before the child reaches
	// the line that records its grandchild — at 300ms the parent could give up first,
	// and then the pidfile assertion below failed for a reason that had nothing to do
	// with process groups. It went from passing to failing five times out of five on
	// one machine without the code changing, which is what a test racing its own
	// fixture looks like. Do not tune this back down to make the suite faster: the
	// timeout being generous costs under two seconds and is the only thing making the
	// assertion mean what it says.
	s := &Subagent{Cwd: root, Exe: stub, Timeout: 2 * time.Second}

	start := time.Now()
	res, err := call(t, s, ModeEdit, "loop forever")
	if err == nil {
		t.Fatal("Execute() on a hanging child succeeded, want a timeout")
	}
	if !strings.Contains(err.Error(), "did not finish within") {
		t.Errorf("error = %v, want a timeout message", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("took %v to give up, want about the timeout", elapsed)
	}
	// The lock is released even on the failure path, so prune can reclaim it.
	d := details(t, res)
	if _, err := os.Stat(filepath.Join(root, ".git", "worktrees", d.ID, "locked")); err == nil {
		t.Error("the worktree is still locked after the child was killed")
	}
	// The grandchild has to be gone too. This is the assertion that matters: a
	// `go test` started by a subagent must not outlive the turn that gave up on it,
	// and killing only the direct child leaves it running under init.
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the stub never recorded a grandchild: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("grandchild pid %q: %v", raw, err)
	}
	// Give the group a moment to die, then insist that it did.
	for i := 0; i < 40 && syscall.Kill(pid, 0) == nil; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("grandchild %d survived the timeout; the process group was not killed", pid)
	}
}
