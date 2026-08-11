package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/yosukeno/pi-go/tui"
	"github.com/yosukeno/pi-go/worktree"
)

// worktreeCommand implements -worktrees and -worktrees-prune.
//
// Cleanup is a command rather than a background sweep because pi-go has no
// daemon: nothing runs between invocations, so the only honest options are "when
// a person asks" and "never". Both Claude Code and Codex sweep on a timer, which
// they can because they are long-lived; here it would mean deleting someone's work
// from inside an unrelated run.
//
// The writer is a parameter rather than stdout at each call site so the output can
// be asserted on. This is a listing, so it belongs on stdout — unlike the notes
// that go to diagOut — and the seam is what makes that claim checkable.
func worktreeCommand(out io.Writer, cwd string, prune bool) error {
	repo, err := worktree.Open(cwd)
	if err != nil {
		return err
	}
	root, err := worktree.RootDir(repo)
	if err != nil {
		return err
	}

	if prune {
		removed, kept, unlocked, err := repo.Prune()
		if err != nil {
			return err
		}
		for _, d := range unlocked {
			fmt.Fprintf(out, "%sunlocked %s (the process that held it is gone)%s\n", tui.Dim, filepath.Base(d), tui.Reset)
		}
		for _, d := range removed {
			fmt.Fprintf(out, "removed %s\n", filepath.Base(d))
		}
		// Kept is printed, not silent: "nothing happened" and "three of them still
		// hold work" look identical otherwise, and the second one is the answer to
		// "why is my disk still full".
		for _, d := range kept {
			fmt.Fprintf(out, "%skept    %s (holds work or is in use)%s\n", tui.Dim, filepath.Base(d), tui.Reset)
		}
		if len(removed)+len(kept)+len(unlocked) == 0 {
			fmt.Fprintf(out, "%sno isolated worktrees to prune%s\n", tui.Dim, tui.Reset)
		}
		return nil
	}

	list, err := repo.List()
	if err != nil {
		return err
	}
	// Asked once for the whole listing: every worktree here has the same parent, so
	// the parent's ignored set is the same answer for all of them.
	ignored, ignoredErr := repo.IgnoredPaths()

	shown := 0
	for _, w := range list {
		if w.Dir == repo.Root {
			continue // the main checkout is not an isolated worktree
		}
		var notes []string
		switch {
		case !w.Mine:
			notes = append(notes, "yours")
		case w.Locked && w.LockPID > 0:
			notes = append(notes, fmt.Sprintf("in use by pid %d", w.LockPID))
		case w.Locked:
			notes = append(notes, "locked")
		}
		tree := repo.Attach(w.Dir)
		// The same question -worktrees-prune will ask. Answering it here turns "why
		// did prune keep three of these" into something you can see beforehand.
		if w.Mine && tree.HoldsWork() {
			notes = append(notes, "holds work")
		}
		head := w.Head
		if len(head) > 12 {
			head = head[:12]
		}
		fmt.Fprintf(out, " %-24s %s %s%s%s\n", filepath.Base(w.Dir), head, tui.Dim, strings.Join(notes, ", "), tui.Reset)
		// Printed under its worktree rather than as a summary, because it is the
		// answer to "why did the tests fail in there" and that question is asked
		// about one worktree at a time. Until now this only ever reached the agent
		// working inside; the person debugging it could not see it at all.
		if w.Mine && ignoredErr == nil {
			if missing := tree.Missing(ignored); len(missing) > 0 {
				fmt.Fprintf(out, "   %smissing: %s%s\n", tui.Dim, strings.Join(missing, " "), tui.Reset)
			}
		}
		shown++
	}
	if shown == 0 {
		fmt.Fprintf(out, "%sno worktrees besides the main checkout%s\n", tui.Dim, tui.Reset)
	}
	// Said once, not per worktree: it is a property of the project, and repeating it
	// under every entry would bury the per-worktree lines that differ.
	if shown > 0 && ignoredErr == nil && len(ignored) > 0 {
		fmt.Fprintf(out, "%s“missing” means gitignored in this project and absent from that "+
			"checkout; list what a build needs in %s%s\n", tui.Dim, worktree.IncludeFile, tui.Reset)
	}
	// The location is printed even when empty: "none" is much less useful than
	// "none, and here is where they would go".
	fmt.Fprintf(out, "%s%s%s\n", tui.Dim, root, tui.Reset)
	return nil
}
