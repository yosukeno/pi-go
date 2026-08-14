package main

import (
	"io"
	"os"

	"github.com/yosukeno/pi-go/session"
	"github.com/yosukeno/pi-go/web"
)

// checkpointCommand implements -checkpoints-prune.
//
// Cleanup is a command rather than a background sweep for the same reason
// -worktrees-prune is (see worktrees.go): pi-go has no daemon, so the only
// honest options are "when a person asks" and "never". Both Claude Code and
// Codex sweep on a timer, which they can because they are long-lived; here it
// would mean deleting someone's restore points from inside an unrelated run.
//
// Until this existed the answer was "never": a checkpoint was taken at every run
// start and nothing ever removed one, so a workspace's shadow repo grew for as
// long as the workspace was worked in.
//
// The retention numbers are not flags. A knob here would need a reason to be
// turned, and the honest default — a hundred points or thirty days, whichever
// runs out first — is the same answer for every project. Add the flag when
// someone has a workspace that disproves that.
func checkpointCommand(out io.Writer, cwd string) error {
	// -C defaults to the empty string and every consumer resolves it the same
	// way (see serveWeb, printSkills, printMemory). Resolving it here too is not
	// tidiness: the shadow repo is addressed by a hash of this exact string, so
	// a command that skipped this step would look for the store under
	// sha256("") and truthfully report that a busy workspace has no checkpoints.
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return err
		}
	}
	return web.PruneCheckpoints(out, session.DefaultDir(), cwd,
		web.DefaultCheckpointKeep, web.DefaultCheckpointMaxAge)
}
