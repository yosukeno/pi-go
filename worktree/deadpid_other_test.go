//go:build !unix

package worktree_test

import "testing"

// unusedPID skips instead of returning a pid, because on these platforms there is
// no dead pid to hand back that would mean anything: alive() reports every pid in a
// lock reason as live (see alive_other.go), so the caller's premise — a lock whose
// owner has crashed — cannot be staged at all.
//
// Skipping rather than faking it. A test that ran here would assert Prune releases
// a stale lock, which is precisely the behaviour alive_other.go declines to
// provide; passing it would require the fake to lie about the platform.
func unusedPID(t *testing.T) int {
	t.Helper()
	t.Skip("staging a crashed lock owner needs signal 0; see alive_other.go")
	return 0
}
