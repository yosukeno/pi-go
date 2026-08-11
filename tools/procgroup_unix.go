//go:build unix

package tools

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the command in a new process group so its descendants can
// be signalled as a unit.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup signals the whole group. The negative pid is what makes it reach the
// grandchildren; signalling cmd.Process alone would leave them running.
//
// SIGKILL rather than SIGTERM: the group is being cancelled, and a build tool
// that traps SIGTERM to clean up would delay a stop the user already asked for.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		// The group may already be gone, or setpgid may not have taken effect;
		// fall back to the direct child so cancellation is never a no-op.
		return cmd.Process.Kill()
	}
	return nil
}
