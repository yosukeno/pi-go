//go:build !unix

package worktree

// alive answers the one question this package asks about a pid, on platforms with
// no signal 0 to ask it with.
//
// It answers "yes" for any pid a lock reason actually carried, which is the
// conservative direction and not a neutral one: Prune keeps a worktree whose lock
// names a live process, so a "yes" here means keep the directory rather than
// unlock and remove it. Getting that wrong the other way would let a second agent
// into a worktree the first one is still working in, and nothing downstream can
// detect that — the same reason HoldsWork treats a worktree it cannot inspect as
// holding work.
//
// The cost is stated plainly: on these platforms Prune will not reclaim a lock
// left behind by a crash, which is the case the pid was put in the reason string
// for. `pi-go -worktrees` still shows the directory and it can be released by
// hand. Nothing here runs in practice anyway — a worktree exists to hold a
// subagent, and a subagent needs the Unix shell the bash tool spawns — so this
// file, like tools/procgroup_other.go, is here to keep the package building.
//
// Doing better needs os.FindProcess, whose error is meaningful on Windows and
// meaningless on Unix (it never fails there), so it would be a third file keyed on
// windows rather than a portable replacement for the two. Worth writing when
// someone prunes worktrees on Windows and not before.
func alive(pid int) bool {
	return pid > 0
}
