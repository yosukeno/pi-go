//go:build unix

package worktree

import (
	"errors"
	"syscall"
)

// alive reports whether a process exists. Signal 0 is the portable "does this pid
// exist" question; EPERM means it exists and belongs to someone else.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
