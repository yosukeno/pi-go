//go:build !unix

package tools

import "os/exec"

// Process groups are a Unix concept. On other platforms the default behaviour
// stands: the direct child is killed and its descendants are not. The bash tool
// already needs a Unix shell, so this exists to keep the package building rather
// than to be used.
func setProcessGroup(*exec.Cmd) {}

// KillGroup is exported for the same reason it is split by platform: the web
// terminal kills a pty's shell and every child it started, and that is the same
// question the bash tool asks, so it should not be a second answer.
func KillGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
