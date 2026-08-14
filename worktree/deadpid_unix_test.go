//go:build unix

package worktree_test

import (
	"syscall"
	"testing"
)

// unusedPID finds a pid that no process holds, so a lock can be made to look like
// it was left behind by a crash.
//
// It asks the same way the package itself does — signal 0 — and it is kept in its
// own platform-tagged file for the same reason alive() is: signal 0 does not exist
// everywhere, and a single unguarded syscall.Kill is what stopped the whole binary
// from building on Windows.
func unusedPID(t *testing.T) int {
	t.Helper()
	for pid := 30000; pid < 40000; pid += 7 {
		if err := syscall.Kill(pid, 0); err != nil {
			return pid
		}
	}
	t.Skip("could not find an unused pid")
	return 0
}
