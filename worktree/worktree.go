// Package worktree creates isolated git checkouts for agents that must not write
// to the same files.
//
// What it does and does not give you, stated up front because getting this wrong
// is the whole risk. A linked worktree isolates *working files*: two agents each
// in their own worktree cannot overwrite each other's edits, and the path guard in
// package tools keeps each one inside its own directory. It does **not** isolate
// git metadata. Every worktree shares the main checkout's `.git`, that directory
// is writable, and a worktree's own `.git` is a plain text file pointing into it.
// Both are levers that reach the main checkout:
//
//   - rewrite `<worktree>/.git` to name the main repository and a plain
//     `git commit` from inside the worktree lands on the main branch, leaving the
//     main working tree dirty.
//   - `git config core.hooksPath ...` from inside a worktree writes to the shared
//     config, so the main checkout runs those hooks afterwards.
//
// Neither goes through a path check, because neither mentions a path outside the
// worktree. So this package does two things instead of pretending to prevent
// them: Verify is cheap and repeatable so callers can re-check identity
// immediately before every git invocation, and Commit refuses to run when the
// check fails. Detection, not prevention. A real boundary needs the process and
// filesystem isolation that this package deliberately does not attempt.
package worktree

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// RefPrefix is where a finished worktree's commit is pinned.
//
// Outside refs/heads on purpose: `git branch` does not list it, so a dozen
// subagent runs do not bury the user's own branches, and the "a branch can only
// be checked out in one place" rule never comes up. It is still a ref, so the
// commit survives gc — an unreferenced commit in a detached worktree does not.
const RefPrefix = "refs/pi-go/sub/"

// IncludeFile lists gitignored files to carry into a new worktree.
//
// The name is not ours: both Claude Code and Codex settled on `.worktreeinclude`
// with the same meaning, and a project that already has one should not need a
// second. Without it a fresh checkout has no `.env`, and the first thing a
// subagent does — run the tests — fails for a reason that has nothing to do with
// its task.
const IncludeFile = ".worktreeinclude"

// Repo is the main checkout that isolated worktrees are linked from.
type Repo struct {
	// Root is the main working tree's top level.
	Root string
	// GitDir is the shared .git, absolute. Shared is the operative word: see the
	// package comment.
	GitDir string
}

// Open resolves the repository containing dir.
//
// A non-git directory is a refusal, not a fallback to the shared directory. The
// fallback is what makes two agents silently overwrite each other, which is the
// exact failure this package exists to prevent, so it must not be reachable by
// accident.
func Open(dir string) (*Repo, error) {
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return nil, err
		}
	}
	root, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%s is not inside a git repository, so there is nowhere to "+
			"put an isolated worktree: run this from a checkout, or `git init` first", dir)
	}
	gitDir, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	return &Repo{Root: canonical(root), GitDir: canonical(gitDir)}, nil
}

// Available reports whether dir can host an isolated worktree at all.
//
// It exists so a caller can find out once, at startup, instead of discovering it
// on the first delegation. Open's refusal is correct but it arrives late: by then a
// model has already spent a turn asking for something this workspace cannot do,
// and it will spend another one asking again, because the error names a remedy
// (`git init`) that the model has no way to carry out.
//
// One `git rev-parse` per session, so the cost is not worth caching. It is a
// question about the directory rather than about the repository, which is why it
// returns a bool and not the *Repo — a caller that wants the repository should
// call Open and read the error.
func Available(dir string) bool {
	_, err := Open(dir)
	return err == nil
}

// Tree is one isolated worktree.
type Tree struct {
	// ID names the worktree, the ref its commit is pinned to, and the lock reason.
	ID   string
	Dir  string
	Repo *Repo

	// DirtyApplied reports whether the parent's uncommitted changes to tracked
	// files were carried in. A boolean rather than an error because failing to
	// carry them is not fatal: the agent can still work, it just starts from a
	// clean HEAD, and it needs to be told which.
	DirtyApplied bool
	// DirtyErr says why they were not carried, when they were not.
	DirtyErr error
	// Untracked lists files the parent has that are not in HEAD and were
	// deliberately not carried. Reported rather than silently dropped: `git diff
	// HEAD` does not see them, and the alternative — `git add -N` — would write to
	// the parent's index to read the parent's state.
	Untracked []string
	// Included lists files copied because of .worktreeinclude.
	Included []string
	// MissingIgnored names gitignored paths the parent has that this checkout does
	// not, in git's collapsed form — "node_modules/" rather than its ten thousand
	// files.
	//
	// This is the list that decides whether the first command an agent runs works.
	// A fresh worktree has no node_modules, no virtualenv, no build directory, so
	// `npm test` fails with a missing-module error that says nothing about the real
	// cause. Naming them lets the caller tell the agent what it is standing in,
	// instead of leaving it to diagnose a environment it cannot see the outside of.
	MissingIgnored []string
}

// Ref is where this worktree's commit gets pinned.
func (t *Tree) Ref() string { return RefPrefix + t.ID }

// inProgress names an unfinished git operation in the main checkout, or "".
//
// Detected by the marker files git itself uses, because there is no porcelain
// command that reports "an operation is underway" as a value.
func (r *Repo) inProgress() string {
	// A multi-commit cherry-pick or revert keeps its plan here, and the per-commit
	// heads below only exist while one commit is being applied.
	for _, c := range []struct{ path, what string }{
		{"CHERRY_PICK_HEAD", "cherry-pick"},
		{"MERGE_HEAD", "merge"},
		{"REVERT_HEAD", "revert"},
		{"rebase-merge", "rebase"},
		{"rebase-apply", "rebase"},
		{"sequencer", "cherry-pick or revert"},
	} {
		if _, err := os.Lstat(filepath.Join(r.GitDir, c.path)); err == nil {
			return c.what
		}
	}
	// A conflict can also outlive the operation that caused it, once the markers are
	// cleaned up but the files are still unmerged. Same problem either way: the
	// content on disk is not anybody's code.
	if out, err := git(r.Root, "diff", "--name-only", "--diff-filter=U"); err == nil {
		if files := strings.TrimSpace(out); files != "" {
			return "unresolved conflict in " + strings.ReplaceAll(files, "\n", ", ")
		}
	}
	return ""
}

// Add creates a detached worktree at the current HEAD.
//
// Detached rather than on a new branch, following Codex: several worktrees can
// exist without inventing several branch names, and nothing has to be cleaned out
// of the user's branch list afterwards. The commit produced at the end is reachable
// through Ref instead.
func (r *Repo) Add(id string) (*Tree, error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	// Refused rather than worked around, because both workarounds are worse than a
	// refusal. carryDirty copies `git diff HEAD` into the new checkout, and during a
	// conflict that diff *contains the conflict markers* — measured: the apply
	// succeeds, and the agent inside opens a file beginning `<<<<<<< HEAD`. It then
	// either reports on text that is nobody's code, or picks a side and commits a
	// merge resolution no one asked for. Starting from a clean HEAD instead would
	// silently discard whatever the parent was in the middle of.
	//
	// Mid-rebase counts even when nothing is conflicted: HEAD is a temporary commit
	// there, so a diff against it describes a state that will not exist afterwards.
	if what := r.inProgress(); what != "" {
		return nil, fmt.Errorf("this checkout is in the middle of a %s, so it is not a "+
			"coherent starting point for an isolated worktree — the uncommitted changes "+
			"carried into one would include the conflict itself. Finish or abort that "+
			"operation first (`git cherry-pick --continue` / `--abort`, `git rebase "+
			"--continue` / `--abort`, `git merge --abort`), then try again", what)
	}
	base, err := RootDir(r)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, err
	}
	// The two directories this package owns must be real directories, not links.
	// A link here would mean writes land somewhere other than where every check
	// below looked. Deliberately not a check on the whole path: on macOS /var is
	// itself a link to /private/var, so "no symlink anywhere above us" would refuse
	// every temporary directory on the platform.
	if err := notASymlink(base); err != nil {
		return nil, err
	}
	// Canonical from here on, so the paths git reports and the paths compared in
	// Verify are the same spelling.
	base = canonical(base)
	dir := filepath.Join(base, id)
	if err := notASymlink(dir); err != nil {
		return nil, err
	}
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("worktree %s already exists at %s", id, dir)
	}
	if _, err := git(r.Root, "worktree", "add", "--detach", dir, "HEAD"); err != nil {
		return nil, err
	}

	t := &Tree{ID: id, Dir: dir, Repo: r}
	// Identity is checked immediately, before anything is copied in: if git handed
	// back something that resolves into the main checkout, the next steps would be
	// operating on the user's own files.
	if err := t.Verify(); err != nil {
		_ = t.Remove(true)
		return nil, err
	}
	t.carryDirty()
	if err := t.carryIncluded(); err != nil {
		t.Included = nil
	}
	// Last, so that anything .worktreeinclude did carry is no longer reported
	// missing.
	if ignored, err := r.IgnoredPaths(); err == nil {
		t.MissingIgnored = t.Missing(ignored)
	}
	return t, nil
}

// Verify checks that Dir is still a worktree of Repo and still separate from it.
//
// Cheap and idempotent on purpose: callers are expected to run it again right
// before every git invocation rather than once at creation. The tampering it looks for
// happens *after* creation — a single write to `<worktree>/.git` is enough — so a
// one-shot check at setup time proves nothing later.
func (t *Tree) Verify() error {
	// 1. The .git file must still point where git put it. This is the cheap check
	// and the one that catches the rewrite, because the rewrite has to change it.
	gitPath := filepath.Join(t.Dir, ".git")
	st, err := os.Lstat(gitPath)
	if err != nil {
		return fmt.Errorf("worktree %s has no .git entry: %w", t.ID, err)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("worktree %s: .git is not a regular file (mode %s); a linked "+
			"worktree's .git is a one-line pointer", t.ID, st.Mode())
	}
	raw, err := os.ReadFile(gitPath)
	if err != nil {
		return err
	}
	// Separators are normalised before comparing, and on Windows that is the
	// difference between this check working and rejecting every worktree git
	// creates: git writes the pointer with forward slashes ("gitdir: C:/…/worktrees/
	// sub1234") while filepath.Join builds it with backslashes, so a byte comparison
	// failed on a directory nobody had touched. It is a no-op on unix.
	//
	// Nothing is weakened. On Windows `/` and `\` name the same path, so treating
	// them as equal is what "points at the same place" means there; the rewrite this
	// check exists to catch has to point somewhere else, which no amount of
	// separator juggling turns into an equal string.
	want := "gitdir: " + filepath.Join(t.Repo.GitDir, "worktrees", t.ID)
	got := strings.TrimSpace(string(raw))
	if normalizePointer(got) != normalizePointer(want) {
		return fmt.Errorf("worktree %s: .git points at %q, want %q — it has been "+
			"rewritten, so git commands run here would act on the main checkout",
			t.ID, got, want)
	}

	// 2. Ask git the same question, so a redirect this code does not know about
	// (core.worktree and friends) still fails the check.
	common, err := git(t.Dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("worktree %s: %w", t.ID, err)
	}
	if canonical(common) != t.Repo.GitDir {
		return fmt.Errorf("worktree %s: git resolves its common dir to %s, not %s",
			t.ID, common, t.Repo.GitDir)
	}
	top, err := git(t.Dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("worktree %s: %w", t.ID, err)
	}
	if canonical(top) == t.Repo.Root {
		return fmt.Errorf("worktree %s: git resolves its working tree to the main "+
			"checkout %s; a `git reset --hard` here would discard the user's work",
			t.ID, t.Repo.Root)
	}
	return nil
}

// normalizePointer puts a `.git` pointer line into one separator convention so two
// spellings of the same path compare equal. Only the path half is touched, so the
// "gitdir: " prefix still has to be present and spelled correctly.
func normalizePointer(line string) string {
	const prefix = "gitdir: "
	path, ok := strings.CutPrefix(line, prefix)
	if !ok {
		return line
	}
	return prefix + filepath.Clean(filepath.FromSlash(path))
}

// carryDirty replays the parent's uncommitted changes to tracked files.
//
// `git diff HEAD` rather than a stash: reading the parent's state must not write
// to the parent's index, which rules out the `git add -N` trick that would also
// pick up untracked files. Those are listed in Untracked instead, so the caller
// can tell the agent what it is not seeing rather than letting it wonder.
func (t *Tree) carryDirty() {
	t.Untracked, _ = lines(git(t.Repo.Root, "ls-files", "--others", "--exclude-standard"))

	patch, err := git(t.Repo.Root, "diff", "HEAD")
	if err != nil {
		t.DirtyErr = err
		return
	}
	if strings.TrimSpace(patch) == "" {
		t.DirtyApplied = true // nothing to carry is not a failure to carry
		return
	}
	cmd := exec.Command("git", "-C", t.Dir, "apply", "-")
	cmd.Stdin = strings.NewReader(patch + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.DirtyErr = fmt.Errorf("applying the parent's uncommitted changes: %w: %s",
			err, strings.TrimSpace(string(out)))
		return
	}
	t.DirtyApplied = true
}

// IgnoredPaths lists what git ignores in the main checkout, collapsed.
//
// The collapsed form is what makes this usable at all: --directory reports
// "node_modules/" instead of the ten thousand files inside it, so the result is a
// handful of names, and git does not walk into the directories to produce them.
// Measured on an 8500-file ignored tree: 28ms, which is why a listing command can
// afford to call this every time rather than only at creation.
//
// Separated from Missing so that a caller inspecting several worktrees asks the
// main checkout once. They share one parent, so they share one answer.
func (r *Repo) IgnoredPaths() ([]string, error) {
	return lines(git(r.Root, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory"))
}

// Missing reports which of the parent's ignored paths this worktree does not have.
//
// This is the list that decides whether the first command an agent runs works. A
// fresh checkout has no node_modules, no virtualenv, no build output, because all
// of that is ignored and a checkout does not carry it — and the resulting failure
// points somewhere else entirely, at a dependency rather than at a directory that
// was never populated.
func (t *Tree) Missing(ignored []string) []string {
	var out []string
	for _, rel := range ignored {
		if _, err := os.Lstat(filepath.Join(t.Dir, rel)); err == nil {
			continue // carried in, or the checkout produced it
		}
		out = append(out, rel)
	}
	return out
}

// carryIncluded copies the files named by .worktreeinclude.
//
// Two filters, and the order matters. git decides what is ignored, so a tracked
// file can never be copied on top of the checkout no matter what the patterns say.
// The patterns then select among the ignored files. Existing files are never
// overwritten and symlinks are skipped, both following Codex.
//
// Copying, not linking or cloning. A shared symlink is what people reach for first
// and it is the wrong answer twice over: two parallel subagents would share one
// mutable dependency tree, and a link out of the worktree is a hole in the only
// isolation this package provides. Copy-on-write cloning is safe but was measured
// and does not pay for itself: 1000 small files took 0.19s with a plain copy and
// 0.13s with APFS clonefile, because a dependency tree is bottlenecked on file
// count rather than bytes. Platform-specific code for 30% of a fifth of a second
// is not worth the fallback path it needs.
func (t *Tree) carryIncluded() error {
	patterns, err := readPatterns(filepath.Join(t.Repo.Root, IncludeFile))
	if err != nil || len(patterns) == 0 {
		return err
	}
	ignored, err := lines(git(t.Repo.Root, "ls-files", "--others", "--ignored", "--exclude-standard"))
	if err != nil {
		return err
	}
	for _, rel := range ignored {
		if !matchAny(patterns, rel) {
			continue
		}
		src := filepath.Join(t.Repo.Root, rel)
		dst := filepath.Join(t.Dir, rel)
		if st, err := os.Lstat(src); err != nil || st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			continue
		}
		if _, err := os.Lstat(dst); err == nil {
			continue // never overwrite what the checkout already produced
		}
		if err := copyFile(src, dst); err != nil {
			return err
		}
		t.Included = append(t.Included, rel)
	}
	return nil
}

// Lock marks the worktree as in use by pid, so a concurrent prune cannot remove
// it. The pid travels in the lock reason because that is the only durable place
// git offers, and prune needs it to tell "still running" from "crashed".
func (t *Tree) Lock(pid int) error {
	_, err := git(t.Repo.Root, "worktree", "lock", "--reason", "pi-go pid="+strconv.Itoa(pid), t.Dir)
	return err
}

func (t *Tree) Unlock() error {
	_, err := git(t.Repo.Root, "worktree", "unlock", t.Dir)
	return err
}

// Commit records everything in the worktree and pins it to Ref.
//
// The parent process does this, not the agent working inside: it is the one git
// invocation that has to be trusted, so it is the one that runs after Verify and
// from outside. An empty worktree returns ok=false rather than an error — nothing
// to hand back is a normal outcome for a subagent that only read.
func (t *Tree) Commit(message string) (sha string, ok bool, err error) {
	if err := t.Verify(); err != nil {
		return "", false, err
	}
	if _, err := git(t.Dir, "add", "-A"); err != nil {
		return "", false, err
	}
	staged, err := git(t.Dir, "status", "--porcelain")
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(staged) == "" {
		return "", false, nil
	}
	if _, err := git(t.Dir, "commit", "--no-verify", "-m", message); err != nil {
		return "", false, err
	}
	sha, err = git(t.Dir, "rev-parse", "HEAD")
	if err != nil {
		return "", false, err
	}
	// Pinned from the main checkout: the ref namespace is shared, and doing it
	// here keeps every write to refs/ on the trusted side.
	if _, err := git(t.Repo.Root, "update-ref", t.Ref(), sha); err != nil {
		return "", false, err
	}
	return sha, true, nil
}

// Remove deletes the worktree. force is needed once it has changes in it, which is
// why discarding a subagent's work is a separate decision from finishing it.
func (t *Tree) Remove(force bool) error {
	_ = t.Unlock() // an unlocked worktree is not an error to unlock
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	if _, err := git(t.Repo.Root, append(args, t.Dir)...); err != nil {
		return err
	}
	return nil
}

// Listed is one row of List.
type Listed struct {
	Dir string
	// Head is the commit the worktree is sitting on.
	Head string
	// Detached is true for the worktrees this package creates.
	Detached bool
	// LockPID is the process recorded in the lock reason, or 0 when the worktree
	// is unlocked or was locked by someone else.
	LockPID int
	// Locked is whether git considers it locked at all.
	Locked bool
	// Mine is whether this worktree lives under our root, i.e. we created it.
	// Worktrees the user made with `git worktree add` are listed but never touched.
	Mine bool
}

// List reports every worktree git knows about.
func (r *Repo) List() ([]Listed, error) {
	out, err := git(r.Root, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	base, err := RootDir(r)
	if err != nil {
		return nil, err
	}
	// Canonical, because git reports canonical paths and Mine is a prefix test.
	// Comparing the two spellings is how "we created this" silently became false
	// for every worktree, which made Prune a no-op.
	base = canonical(base)
	var all []Listed
	var cur *Listed
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			if cur != nil {
				all = append(all, *cur)
			}
			dir := strings.TrimPrefix(line, "worktree ")
			cur = &Listed{Dir: dir, Mine: within(canonical(dir), base)}
		case cur == nil:
		case strings.HasPrefix(line, "HEAD "):
			cur.Head = strings.TrimPrefix(line, "HEAD ")
		case line == "detached":
			cur.Detached = true
		case strings.HasPrefix(line, "locked"):
			cur.Locked = true
			cur.LockPID = parsePID(strings.TrimPrefix(line, "locked"))
		}
	}
	if cur != nil {
		all = append(all, *cur)
	}
	return all, nil
}

// Prune removes worktrees this package created that nobody is using any more.
//
// There is no daemon here, so nothing sweeps in the background: this runs when a
// person asks. Two rules make it safe to run at any time. A worktree whose lock
// names a live process is left alone. A worktree holding work — anything
// uncommitted, or a commit no ref points at — is left alone too, because the only
// thing worse than a stale directory is a deleted afternoon.
func (r *Repo) Prune() (removed, kept, unlocked []string, err error) {
	list, err := r.List()
	if err != nil {
		return nil, nil, nil, err
	}
	for _, w := range list {
		if !w.Mine {
			continue
		}
		t := r.Attach(w.Dir)
		if w.Locked {
			if w.LockPID > 0 && alive(w.LockPID) {
				kept = append(kept, w.Dir)
				continue
			}
			// The process that took this lock is gone. Releasing it is the whole
			// reason the pid is in the reason string: without this, one crash
			// leaves a directory that can never be cleaned up automatically.
			if err := t.Unlock(); err == nil {
				unlocked = append(unlocked, w.Dir)
			}
		}
		if t.HoldsWork() {
			kept = append(kept, w.Dir)
			continue
		}
		if err := t.Remove(false); err != nil {
			kept = append(kept, w.Dir)
			continue
		}
		removed = append(removed, w.Dir)
	}
	return removed, kept, unlocked, nil
}

// HoldsWork reports whether removing this worktree would destroy something.
//
// Exported because it is the question Prune answers and therefore the question a
// listing has to answer too. Telling someone afterwards that three worktrees were
// "kept because they hold work" is less useful than letting them see it before
// they run the command.
func (t *Tree) HoldsWork() bool {
	if status, err := git(t.Dir, "status", "--porcelain"); err != nil || strings.TrimSpace(status) != "" {
		// An unreadable worktree counts as holding work: refusing to remove what
		// we cannot inspect is the conservative direction.
		return true
	}
	head, err := git(t.Dir, "rev-parse", "HEAD")
	if err != nil {
		return true
	}
	// A commit no ref names is about to become unreachable. Ours are pinned under
	// RefPrefix by Commit, so this catches the case where the agent committed by
	// itself and nothing recorded it.
	names, err := git(t.Repo.Root, "for-each-ref", "--points-at", head, "--format=%(refname)")
	if err != nil {
		return true
	}
	return strings.TrimSpace(names) == ""
}

// Attach builds a Tree for a worktree that already exists, so that the methods
// which inspect one can be used on a listing rather than only on a fresh Add.
func (r *Repo) Attach(dir string) *Tree {
	return &Tree{ID: filepath.Base(dir), Dir: dir, Repo: r}
}

// RootDir is where this repository's worktrees live: outside the repository, one
// directory per repository.
//
// Outside is the decision that matters. In-repo worktrees are copies of the
// project, and the search tools would walk them: `find` and `grep` cap their
// results, dot-directories sort first, and the parent agent would get a screenful
// of duplicates instead of its own source. Out here the parent needs no filesystem
// access at all — the shared object store means `git show <ref>:<path>` answers
// everything.
func RootDir(r *Repo) (string, error) {
	base := os.Getenv("PIGO_WORKTREE_DIR")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".pi-go", "worktrees")
	}
	// Keyed by path so two checkouts of the same project do not share worktrees,
	// with the basename kept for a human reading `ls`.
	sum := sha256.Sum256([]byte(r.Root))
	return filepath.Join(base, filepath.Base(r.Root)+"-"+hex.EncodeToString(sum[:4])), nil
}

// --- helpers ---

// git runs one command and returns its trimmed stdout. Failures carry the
// command's own output, because "exit status 128" on its own has never helped
// anyone.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(string(out)), nil
}

func lines(s string, err error) ([]string, error) {
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	return strings.Split(s, "\n"), nil
}

// validID keeps an id usable as a directory name and a ref component. Rejecting
// rather than sanitising: a silently rewritten id would not match the ref the
// caller was told about.
func validID(id string) error {
	if id == "" {
		return errors.New("worktree id must not be empty")
	}
	if strings.ContainsAny(id, `/\ .:~*?[]^`) || strings.HasPrefix(id, "-") {
		return fmt.Errorf("worktree id %q must be a plain name: no separators, dots or glob characters", id)
	}
	return nil
}

// readPatterns reads .worktreeinclude.
//
// A deliberate subset of gitignore syntax: comments, blank lines, and one pattern
// per line. No `**`, no negation. Leading and trailing slashes are stripped, so
// `/node_modules/` and `node_modules` are the same pattern — gitignore treats those
// differently and this does not, which is a simplification rather than a trap,
// because both spellings mean the thing a person meant.
//
// The exactness that matters is not here but in the caller, where git decides what
// counts as ignored — so no pattern can ever copy a tracked file over the checkout.
// Extending the syntax later cannot break that.
func readPatterns(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if p := strings.Trim(line, "/"); p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// matchAny reports whether rel is selected by any pattern.
//
// rel is always a file path, because git lists ignored files individually. The
// directory case is therefore a prefix test, and it is the case that matters most:
// a `.worktreeinclude` containing the obvious line `node_modules` has to carry the
// ten thousand files under it, not the zero files named exactly that. Without the
// prefix test the file is silently useless for every dependency directory there is,
// which is the only reason anyone writes one.
func matchAny(patterns []string, rel string) bool {
	for _, p := range patterns {
		if p == rel {
			return true
		}
		// A pattern naming a directory selects everything beneath it, at any depth.
		if strings.HasPrefix(rel, p+"/") {
			return true
		}
		if ok, _ := filepath.Match(p, rel); ok {
			return true
		}
		if ok, _ := filepath.Match(p, filepath.Base(rel)); ok {
			return true
		}
		// A glob naming directories, `packages/*/node_modules`, has to be matched
		// against rel's leading segments rather than the whole path, because
		// filepath.Match will not let `*` cross a separator.
		if strings.ContainsAny(p, "*?[") && matchDirPrefix(p, rel) {
			return true
		}
	}
	return false
}

// matchDirPrefix reports whether pattern matches a leading directory of rel.
func matchDirPrefix(pattern, rel string) bool {
	n := strings.Count(pattern, "/") + 1
	parts := strings.Split(rel, "/")
	if len(parts) <= n {
		return false // no directory left below the match, so the file case covered it
	}
	ok, _ := filepath.Match(pattern, strings.Join(parts[:n], "/"))
	return ok
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// parsePID pulls the pid back out of a lock reason we wrote. Anything else — a
// lock the user set by hand — yields 0, which Prune reads as "not ours to
// release".
func parsePID(reason string) int {
	i := strings.Index(reason, "pi-go pid=")
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(reason[i+len("pi-go pid="):]))
	if err != nil {
		return 0
	}
	return n
}

// alive is per-platform; see alive_unix.go and alive_other.go. It lives outside
// this file because asking "does this pid exist" needs signal 0, which does not
// exist everywhere, and one unguarded syscall.Kill kept the whole binary from
// building on Windows.

// notASymlink refuses a path that exists and is a symlink. A path that does not
// exist yet is fine: Add is about to create it as a directory.
func notASymlink(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return nil
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; refusing to use it as a worktree location, "+
			"because writes would land outside the directory every check here looked at", path)
	}
	return nil
}

func canonical(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

func within(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}
