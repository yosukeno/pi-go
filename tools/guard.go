package tools

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Guard restricts what bash may run inside a subagent's worktree.
//
// Read the honest version first, because the alternative is a false sense of
// safety. A worktree isolates working files: write and edit go through the path
// guard in tool.go and cannot leave it. bash has no such limit, and two levers
// reach the main checkout without naming a path outside the worktree:
//
//   - `<worktree>/.git` is a plain text file pointing into the shared repository.
//     Rewrite it and a plain `git commit` lands on the main branch.
//   - `git config` writes to the shared config, so `core.hooksPath` set from
//     inside a worktree runs in the main checkout afterwards.
//
// Both are git. So the rule is that a subagent does not get git at all — it needs
// to run tests, not manage branches, and the parent does the one commit that
// matters after verifying the worktree's identity. That removes the whole class
// rather than enumerating its members.
//
// What is left here is an enumeration, and enumerations leak: a shell script, a
// program the agent writes, an `env` invocation. It is the same standard as the
// rest of pi-go's containment — it stops a mistake, not an adversary. Real
// isolation needs the process and filesystem boundary that a container provides.
type Guard struct {
	// Worktree is the directory the child is confined to.
	Worktree string
	// MainCheckout is the repository the worktree was made from. Naming it is
	// enough to be refused, because a subagent has no legitimate reason to.
	MainCheckout string
}

// deniedEnv are assignments that redirect git regardless of any path check.
var deniedEnv = []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_COMMON_DIR=", "GIT_CONFIG"}

// Check reports why a command may not run, or nil.
//
// The messages say what to do instead. A refusal the model cannot act on turns
// into three more attempts at the same thing.
func (g *Guard) Check(command string) error {
	if g == nil {
		return nil
	}
	for _, word := range shellWords(command) {
		if isGit(word) {
			return fmt.Errorf("git is not available to a subagent. You are working in an "+
				"isolated worktree and your changes are committed for you when you finish, "+
				"so you never need to run git here. To see what you have changed, read the "+
				"files. (refused: %s)", firstLine(command))
		}
	}
	for _, env := range deniedEnv {
		if strings.Contains(command, env) {
			return fmt.Errorf("setting %s would point git at another repository, which a "+
				"subagent may not do. (refused: %s)", strings.TrimSuffix(env, "="), firstLine(command))
		}
	}
	if g.MainCheckout != "" && strings.Contains(command, g.MainCheckout) {
		return fmt.Errorf("this command names %s, which is the main checkout your worktree "+
			"was copied from. Work only inside your own directory; the parent agent applies "+
			"your changes there when you are done. (refused: %s)",
			g.MainCheckout, firstLine(command))
	}
	if dir := absoluteCD(command); dir != "" && !g.inside(dir) {
		return fmt.Errorf("changing directory to %s would leave your worktree. Everything "+
			"you need is under %s. (refused: %s)", dir, g.Worktree, firstLine(command))
	}
	return nil
}

// inside reports whether a `cd` target stays in the worktree.
//
// Shares within with the path guard rather than repeating the comparison, because
// it had the same case-sensitivity defect for the same reason: a `cd` typed with
// different case than the worktree's real spelling was refused on a
// case-insensitive volume, and the refusal named two paths a reader would say were
// identical. One predicate means the next repair lands in both places.
//
// Not canonicalised first, deliberately. The argument comes out of the command
// text, and resolving it would answer a question about a different string than the
// one bash will run.
func (g *Guard) inside(dir string) bool {
	return within(filepath.Clean(dir), filepath.Clean(g.Worktree))
}

// isGit matches the command name whether it was called plainly or by path.
func isGit(word string) bool {
	word = strings.Trim(word, `"'`)
	if word == "git" {
		return true
	}
	return filepath.Base(word) == "git" && strings.ContainsRune(word, filepath.Separator)
}

// shellWords splits on whitespace and the operators that start a new command, so
// that `ls && git log` is seen as two commands rather than one string that merely
// contains the word git.
//
// Not a shell parser, and not trying to be: see the type comment. It exists to
// catch the command the model actually wrote, not to survive deliberate quoting.
func shellWords(command string) []string {
	fields := strings.FieldsFunc(command, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', ';', '|', '&', '(', ')', '`':
			return true
		}
		return false
	})
	return fields
}

// absoluteCD returns the target of a `cd /abs/path`, or "".
//
// Only absolute targets are checked. A relative `cd ..` can also climb out, but
// the path guard already refuses to read or write outside the worktree, so the
// case worth catching is the one that jumps somewhere specific.
func absoluteCD(command string) string {
	words := shellWords(command)
	for i, w := range words {
		if w != "cd" || i+1 >= len(words) {
			continue
		}
		target := strings.Trim(words[i+1], `"'`)
		if filepath.IsAbs(target) {
			return target
		}
	}
	return ""
}
