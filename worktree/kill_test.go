package worktree_test

import "syscall"

// syscallKill asks whether a pid exists, the same way the package itself does.
// Kept in its own file so the test's use of syscall does not look like the
// package's.
func syscallKill(pid int) error { return syscall.Kill(pid, 0) }
