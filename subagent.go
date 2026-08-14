package main

import (
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/yosukeno/pi-go/config"
	"github.com/yosukeno/pi-go/tools"
	"github.com/yosukeno/pi-go/worktree"
)

// toolOptions decides which tools this process gets.
//
// One function for both sides of the fork, because the interesting part is that
// they are not symmetric and the asymmetry has to be visible in one place: a
// parent gets the subagent tool and unrestricted bash, a child gets neither. Two
// separate constructions would drift.
func toolOptions(cwd, model string, readRoots, writeRoots []string) tools.Options {
	o := tools.Options{Cwd: cwd, ReadRoots: readRoots, ReadOnly: readOnlyChild()}
	depth := subagentDepth()

	// Memory belongs to the top-level session only, and is withheld the same way the
	// task list is: by not granting the root, so a child cannot spend a turn finding
	// out it is refused.
	//
	// The argument is the todo one almost word for word. A child's notes have no
	// reader: it lives for one run under a ten-minute timeout, so it never crosses a
	// session boundary, which is the only thing memory is for. What would be left is a
	// second writer for "what do I know about this project", and that question has to
	// have one answer — the measured way long-term state decays is through ordinary
	// use, and two writers with no shared view of each other is the fastest version of
	// that.
	//
	// A read-only child is refused too, though it could do no harm writing. Reading is
	// not the risk; being handed a set of conclusions is. An explore child is asked one
	// narrow question and answers from the code, and notes it did not write are context
	// it cannot verify and did not ask for.
	if depth == 0 {
		o.WriteRoots = writeRoots
	}

	// Only the top-level session keeps a task list. Withheld from every child
	// regardless of mode, for the reasons in tools.Options.Todo — and withheld the
	// same way the subagent tool is, by not registering it, so a child cannot spend
	// a turn discovering that it exists but refuses.
	o.Todo = depth == 0

	// A child is confined; a top-level run is not. This is where the existing
	// behaviour is preserved deliberately: with depth 0 the guard is nil and bash
	// is exactly as unrestricted as it has always been.
	//
	// Skipped for a read-only child, and not merely because there is no bash to
	// guard: an explore child runs in the parent's own directory, so a guard built
	// here would be told its worktree is the main checkout and would refuse
	// everything, including the reads it is supposed to allow.
	if depth > 0 && !o.ReadOnly {
		o.Guard = &tools.Guard{
			Worktree: cwd, MainCheckout: os.Getenv(tools.EnvMainCheckout),
			// From the environment for the same reason the depth and read-only
			// markers are: the parent decided it, and a child that could talk its
			// way into the other wording would be told a comforting untruth about
			// where its commits would land.
			Shared: os.Getenv(tools.EnvSubagentShared) == "1",
		}
	}
	// Registered only while there is room to nest. Not registering it is a stronger
	// bound than refusing the call: a tool that is absent costs no schema tokens
	// and cannot be attempted, whereas one that always refuses invites retries.
	//
	// A read-only child never gets it, whatever the depth. Delegating is not itself
	// a write, but an explore child that could spawn an edit child would be a way
	// to reach write tools from a session that was promised not to have any.
	if depth < tools.DefaultSubagentMaxDepth && !o.ReadOnly {
		// MaxTurns is deliberately absent: see Subagent.MaxTurns. The parent's turn
		// limit bounds the parent's run, and the child's is a different run.
		// Both env settings are validated in main before anything runs, so the errors
		// here cannot be new information; the safe value comes back with them.
		isolation, _ := tools.IsolationFromEnv()
		concurrency, _ := tools.ConcurrencyFromEnv()
		o.Subagent = &tools.Subagent{
			Cwd: cwd, Model: model, Depth: depth,
			// Resolved here rather than inside the tool so that package tools keeps
			// knowing nothing about the model catalogue. Empty unless the user's
			// config names one, in which case explore children run it instead.
			ExploreModel: config.SubagentModel(model),
			ExplorePool:  explorePool(),
			Isolation:    isolation,
			Concurrency:  concurrency,
			// Asked once, here, because this is where the working directory is known
			// and package tools does no git. Set unconditionally: whether a missing
			// repository actually withholds edit mode depends on the isolation, and
			// Subagent.exploreOnly is the one place that combines them.
			ExploreOnly: !worktree.Available(cwd),
		}
	}
	return o
}

// checkSubagentEnv validates the two environment settings that shape delegation,
// and announces the one that changes a safety property.
//
// The notice is not decoration. Shared isolation removes the guarantee that a
// delegated run cannot touch the working directory, and it is switched on by an
// environment variable — the least visible place a decision can live. An operator
// who inherited a wrapper script, a compose file or a Dockerfile from someone else
// should not have to read them to find out. Printed to the same stream as the other
// startup notices, and only when the setting is not the default, so an ordinary run
// is unchanged.
//
// Silent for a child process: it prints once per session, not once per delegation,
// and a child has no subagent tool for the setting to affect anyway.
func checkSubagentEnv(notices io.Writer) error {
	isolation, err := tools.IsolationFromEnv()
	if err != nil {
		return err
	}
	if _, err := tools.ConcurrencyFromEnv(); err != nil {
		return err
	}
	if isolation == tools.IsolationShared && subagentDepth() == 0 {
		fmt.Fprintf(notices, "pi-go: %s=%s — edit subagents will work directly in this "+
			"directory, with no isolated worktree and no commit. Their changes are immediate "+
			"and cannot be reviewed before they land.\n", tools.EnvIsolation, tools.IsolationShared)
	}
	return nil
}

// subagentDepth reads how deep this process is.
//
// From the environment rather than a flag on purpose. A model can propose flags
// through the tool it is calling; it never writes the environment of a process its
// parent spawns. That makes the depth bound something the model cannot argue with.
func subagentDepth() int {
	n, err := strconv.Atoi(os.Getenv(tools.EnvSubagentDepth))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// readOnlyChild reports whether this process was spawned as an explore-mode
// subagent. Same reasoning as subagentDepth: it arrives in the environment
// because the model must not be able to argue with it.
func readOnlyChild() bool {
	return os.Getenv(tools.EnvSubagentReadOnly) == "1"
}

// explorePool adapts the configured subagent pool into the tool's plain shape;
// package tools stays ignorant of the catalogue.
func explorePool() []tools.ExploreTarget {
	pool := config.SubagentPool()
	out := make([]tools.ExploreTarget, 0, len(pool))
	for _, m := range pool {
		out = append(out, tools.ExploreTarget{Provider: m.Provider, Model: m.ID})
	}
	return out
}
