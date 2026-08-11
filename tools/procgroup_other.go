//go:build !unix

package tools

import "os/exec"

// Process groups are a Unix concept. On other platforms the default behaviour
// stands: the direct child is killed and its descendants are not. The bash tool
// already needs a Unix shell, so this exists to keep the package building rather
// than to be used.
func setProcessGroup(*exec.Cmd) {}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
