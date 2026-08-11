package worktree_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangy/pi-go/worktree"
)

// newRepo builds a real git repository in a temp directory.
//
// Real git, not a fake: every guarantee in this package is a claim about what git
// does — that a linked worktree's .git is a pointer file, that a commit made in one
// is visible from the other, that a lock blocks removal. A mock would only test
// that this package agrees with itself.
func newRepo(t *testing.T) (root string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root = t.TempDir()
	// Worktrees are keyed by the repository path, so each test gets its own root.
	t.Setenv("PIGO_WORKTREE_DIR", filepath.Join(t.TempDir(), "wt"))

	run(t, root, "init", "-q", "-b", "main", ".")
	run(t, root, "config", "user.email", "test@example.com")
	run(t, root, "config", "user.name", "test")
	write(t, root, "f.txt", "tracked\n")
	write(t, root, "keep/a.go", "package keep\n")
	run(t, root, "add", "-A")
	run(t, root, "commit", "-qm", "init")
	return root
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func open(t *testing.T, root string) *worktree.Repo {
	t.Helper()
	r, err := worktree.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return r
}

// A directory with no repository gets a refusal, never a fallback to working in
// the shared directory: that fallback is exactly how two agents come to overwrite
// each other.
func TestOpenRefusesNonGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	before := entries(t, dir)
	_, err := worktree.Open(dir)
	if err == nil {
		t.Fatal("Open() on a plain directory succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), "not inside a git repository") {
		t.Errorf("error = %v, want it to name the cause", err)
	}
	if after := entries(t, dir); after != before {
		t.Errorf("Open() created %d entries in a directory it rejected", after-before)
	}
}

// The worktree is created detached and outside the repository. Both are load-bearing:
// detached keeps the user's branch list clean, and outside keeps the parent's find
// and grep from filling up with copies of the project.
func TestAddIsDetachedAndOutsideTheRepo(t *testing.T) {
	root := newRepo(t)
	r := open(t, root)

	tr, err := r.Add("w1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if strings.HasPrefix(tr.Dir, r.Root+string(filepath.Separator)) {
		t.Errorf("worktree at %s is inside the repository %s", tr.Dir, r.Root)
	}
	// `rev-parse --abbrev-ref` answers "HEAD" for a detached checkout, and unlike
	// symbolic-ref it does not exit non-zero to say so.
	if head := run(t, tr.Dir, "rev-parse", "--abbrev-ref", "HEAD"); head != "HEAD" {
		t.Errorf("worktree HEAD is on branch %q, want detached", head)
	}
	if branches := run(t, root, "branch", "--list"); strings.Count(branches, "\n") > 0 {
		t.Errorf("branch list grew: %q", branches)
	}
	if err := tr.Verify(); err != nil {
		t.Errorf("Verify() on a fresh worktree: %v", err)
	}
}

// The rewrite that makes a worktree act on the main checkout: one write to a plain
// text file inside the worktree, which no path check can see because the path is
// inside the worktree. Verify has to catch it, and it has to keep catching it after
// creation — which is why it is a method and not a step in Add.
func TestVerifyRejectsTamperedGitFile(t *testing.T) {
	root := newRepo(t)
	r := open(t, root)
	tr, err := r.Add("w1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := tr.Verify(); err != nil {
		t.Fatalf("Verify before tampering: %v", err)
	}

	// Exactly what an agent with a write tool can do.
	write(t, tr.Dir, ".git", "gitdir: "+filepath.Join(r.GitDir)+"\n")

	err = tr.Verify()
	if err == nil {
		t.Fatal("Verify() accepted a .git rewritten to name the main repository")
	}
	if !strings.Contains(err.Error(), "rewritten") {
		t.Errorf("error = %v, want it to say the pointer was rewritten", err)
	}
	// And the consequence that matters: nothing gets committed or pinned.
	if _, ok, cerr := tr.Commit("subagent: work"); cerr == nil || ok {
		t.Errorf("Commit() on a tampered worktree = ok %v, err %v; want a refusal", ok, cerr)
	}
	if refs := run(t, root, "show-ref"); strings.Contains(refs, worktree.RefPrefix) {
		t.Errorf("a ref was created despite the refusal: %q", refs)
	}
	if log := run(t, root, "log", "--oneline"); strings.Count(log, "\n") != 0 {
		t.Errorf("main checkout gained commits: %q", log)
	}
}

// Verify also has to fail when the directory is not a separate checkout at all,
// which is the case where `git reset --hard` inside it would discard the user's
// work.
func TestVerifyRejectsDirectoryThatIsTheMainCheckout(t *testing.T) {
	root := newRepo(t)
	r := open(t, root)
	tr, err := r.Add("w1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Point the tree at the main checkout, keeping the same repo.
	fake := &worktree.Repo{Root: r.Root, GitDir: r.GitDir}
	_ = fake
	tr.Dir = r.Root
	if err := tr.Verify(); err == nil {
		t.Error("Verify() accepted the main checkout as an isolated worktree")
	}
}

// Uncommitted work on tracked files is carried in, because a subagent asked to fix
// a test the user is halfway through writing needs to see it. Untracked files are
// not, and are reported rather than silently dropped: reading them would mean
// writing to the parent's index.
func TestCarriesDirtyTrackedChangesAndReportsUntracked(t *testing.T) {
	root := newRepo(t)
	write(t, root, "f.txt", "tracked\nlocal edit\n")
	write(t, root, "brand-new.txt", "not staged anywhere\n")
	r := open(t, root)

	tr, err := r.Add("w1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !tr.DirtyApplied {
		t.Errorf("DirtyApplied = false, err %v; want the parent's edit carried", tr.DirtyErr)
	}
	got, err := os.ReadFile(filepath.Join(tr.Dir, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "tracked\nlocal edit\n"; string(got) != want {
		t.Errorf("f.txt in worktree = %q, want %q", got, want)
	}
	if len(tr.Untracked) != 1 || tr.Untracked[0] != "brand-new.txt" {
		t.Errorf("Untracked = %v, want [brand-new.txt] reported as not carried", tr.Untracked)
	}
	if _, err := os.Stat(filepath.Join(tr.Dir, "brand-new.txt")); err == nil {
		t.Error("an untracked file was carried; only tracked changes travel")
	}
}

// .worktreeinclude carries the ignored files a build needs. Two properties are
// asserted because both are ways to get this wrong: a tracked file must never be
// copied over the checkout, and an ignored file nobody asked for must not appear.
func TestIncludeCopiesOnlyIgnoredMatches(t *testing.T) {
	root := newRepo(t)
	write(t, root, ".gitignore", ".env\n*.local\nsecrets/\n")
	write(t, root, ".worktreeinclude", "# what the tests need\n.env\nsecrets/token.txt\n")
	write(t, root, ".env", "KEY=1\n")
	write(t, root, "notes.local", "ignored but not requested\n")
	write(t, root, "secrets/token.txt", "shhh\n")
	run(t, root, "add", "-A")
	run(t, root, "commit", "-qm", "ignore rules")
	r := open(t, root)

	tr, err := r.Add("w1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(tr.Dir, ".env")); err != nil || string(body) != "KEY=1\n" {
		t.Errorf(".env in worktree = %q, %v; want it carried", body, err)
	}
	if _, err := os.Stat(filepath.Join(tr.Dir, "secrets/token.txt")); err != nil {
		t.Errorf("secrets/token.txt was not carried: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tr.Dir, "notes.local")); err == nil {
		t.Error("notes.local was carried; it is ignored but .worktreeinclude never named it")
	}
	// A tracked file cannot be a candidate at all: git decides what is ignored, so
	// no pattern can put an older copy of a source file on top of the checkout.
	for _, rel := range tr.Included {
		if out := run(t, root, "ls-files", "--", rel); out != "" {
			t.Errorf("copied %q, which is tracked", rel)
		}
	}
}

// A bare directory name has to carry everything under it. This is the only reason
// anyone writes a .worktreeinclude — the dependency directory is the thing a fresh
// checkout is missing — and matching only files named exactly `node_modules` made
// the file silently useless for that case.
func TestIncludeCarriesAWholeDirectory(t *testing.T) {
	root := newRepo(t)
	write(t, root, ".gitignore", "node_modules/\nbuild/\npackages/*/node_modules/\n")
	// Three spellings a person might reasonably use, and one directory left out so
	// that "carries everything" cannot pass by carrying everything ignored.
	write(t, root, ".worktreeinclude", "node_modules\n/packages/*/node_modules/\n")
	write(t, root, "node_modules/dep/index.js", "one\n")
	write(t, root, "node_modules/dep/nested/deep/mod.js", "two\n")
	write(t, root, "packages/a/node_modules/x/i.js", "three\n")
	write(t, root, "build/out.o", "not requested\n")
	run(t, root, "add", "-A")
	run(t, root, "commit", "-qm", "ignore rules")
	r := open(t, root)

	tr, err := r.Add("w1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	for _, rel := range []string{
		"node_modules/dep/index.js",
		"node_modules/dep/nested/deep/mod.js",
		"packages/a/node_modules/x/i.js",
	} {
		if _, err := os.Stat(filepath.Join(tr.Dir, rel)); err != nil {
			t.Errorf("%s was not carried: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(tr.Dir, "build/out.o")); err == nil {
		t.Error("build/out.o was carried; .worktreeinclude never named build")
	}
}

// What the checkout is missing has to be reportable, because it is what breaks the
// first command an agent runs. Reported in git's collapsed form, so the answer is a
// couple of names rather than every file underneath them.
func TestMissingIgnoredNamesWhatIsNotThere(t *testing.T) {
	root := newRepo(t)
	write(t, root, ".gitignore", "node_modules/\n.venv/\n.env\n")
	write(t, root, ".worktreeinclude", ".env\n")
	write(t, root, "node_modules/dep/index.js", "one\n")
	write(t, root, ".venv/bin/python", "two\n")
	write(t, root, ".env", "KEY=1\n")
	run(t, root, "add", "-A")
	run(t, root, "commit", "-qm", "ignore rules")
	r := open(t, root)

	tr, err := r.Add("w1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	got := strings.Join(tr.MissingIgnored, " ")
	for _, want := range []string{"node_modules/", ".venv/"} {
		if !strings.Contains(got, want) {
			t.Errorf("MissingIgnored = %v, want it to name %q", tr.MissingIgnored, want)
		}
	}
	// Carried in, so not missing. Reporting it would send the agent looking for
	// something that is right there.
	if strings.Contains(got, ".env") {
		t.Errorf("MissingIgnored = %v, but .env was carried by .worktreeinclude", tr.MissingIgnored)
	}
	// Collapsed, not enumerated: one entry per directory, not one per file.
	for _, m := range tr.MissingIgnored {
		if strings.Count(m, "/") > 1 {
			t.Errorf("MissingIgnored contains %q; want git's collapsed directory form", m)
		}
	}
}

// A clean repository has nothing to report, and saying nothing is the point: a
// preamble that fires every time is ignored by the time it matters.
func TestNothingMissingInACleanRepo(t *testing.T) {
	root := newRepo(t)
	r := open(t, root)
	tr, err := r.Add("w1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(tr.MissingIgnored) != 0 || len(tr.Untracked) != 0 || !tr.DirtyApplied {
		t.Errorf("clean repo reported missing=%v untracked=%v dirtyApplied=%v",
			tr.MissingIgnored, tr.Untracked, tr.DirtyApplied)
	}
}

// A commit made in the worktree is reachable from the main checkout without any
// filesystem access to the worktree, and it does not appear in `git branch`. This
// is the property that makes putting worktrees outside the repository free: the
// parent inspects results through the shared object store.
func TestCommitPinsToRefAndStaysOffBranchList(t *testing.T) {
	root := newRepo(t)
	r := open(t, root)
	tr, err := r.Add("w1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	write(t, tr.Dir, "added.txt", "from the subagent\n")

	sha, ok, err := tr.Commit("subagent: add a file")
	if err != nil || !ok {
		t.Fatalf("Commit() = %q, %v, %v; want a commit", sha, ok, err)
	}
	if got := run(t, root, "rev-parse", tr.Ref()); got != sha {
		t.Errorf("%s = %s, want %s", tr.Ref(), got, sha)
	}
	// Read the content from the main checkout, through git only.
	if body := run(t, root, "show", tr.Ref()+":added.txt"); body != "from the subagent" {
		t.Errorf("git show %s:added.txt = %q", tr.Ref(), body)
	}
	if branches := run(t, root, "branch", "--list"); strings.Contains(branches, "w1") {
		t.Errorf("branch list mentions the worktree: %q", branches)
	}
	if status := run(t, root, "status", "--porcelain"); status != "" {
		t.Errorf("main working tree is dirty after the subagent committed: %q", status)
	}
	// The ref is what keeps the commit alive; a detached commit would be collected.
	run(t, root, "gc", "--prune=now", "-q")
	if got := run(t, root, "rev-parse", "--verify", tr.Ref()); got != sha {
		t.Errorf("ref did not survive gc: %q", got)
	}
}

// A subagent that only read has nothing to hand back. That is a normal outcome, so
// it is reported as "no commit" rather than as a failure.
func TestCommitOnUnchangedWorktreeIsNotAnError(t *testing.T) {
	root := newRepo(t)
	r := open(t, root)
	tr, err := r.Add("w1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	sha, ok, err := tr.Commit("subagent: nothing")
	if err != nil || ok || sha != "" {
		t.Errorf("Commit() on a clean worktree = %q, %v, %v; want no commit and no error", sha, ok, err)
	}
}

// Prune is the whole cleanup story, since there is no daemon. Three rules at once:
// a live lock is untouchable, a dead lock is released, and work is never deleted.
func TestPruneKeepsWorkReleasesDeadLocksAndSparesLiveOnes(t *testing.T) {
	root := newRepo(t)
	r := open(t, root)

	// 1. Locked by a process that is still running: must survive untouched.
	live, err := r.Add("live")
	if err != nil {
		t.Fatal(err)
	}
	if err := live.Lock(os.Getpid()); err != nil {
		t.Fatal(err)
	}

	// 2. Locked by a process that is gone, and clean: the lock is released and the
	// directory removed. Without this, one crash leaves a directory forever.
	dead, err := r.Add("dead")
	if err != nil {
		t.Fatal(err)
	}
	if err := dead.Lock(unusedPID(t)); err != nil {
		t.Fatal(err)
	}

	// 3. Holding uncommitted work: must survive even though nothing locks it.
	work, err := r.Add("work")
	if err != nil {
		t.Fatal(err)
	}
	write(t, work.Dir, "wip.txt", "half an afternoon\n")

	removed, kept, unlocked, err := r.Prune()
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if !has(removed, dead.Dir) {
		t.Errorf("removed = %v, want the crashed worktree %s", removed, dead.Dir)
	}
	if !has(unlocked, dead.Dir) {
		t.Errorf("unlocked = %v, want the dead lock released", unlocked)
	}
	if !has(kept, live.Dir) {
		t.Errorf("kept = %v, want the live-locked worktree %s", kept, live.Dir)
	}
	if !has(kept, work.Dir) {
		t.Errorf("kept = %v, want the worktree holding uncommitted work %s", kept, work.Dir)
	}
	if _, err := os.Stat(filepath.Join(work.Dir, "wip.txt")); err != nil {
		t.Errorf("Prune destroyed uncommitted work: %v", err)
	}
	if _, err := os.Stat(live.Dir); err != nil {
		t.Errorf("Prune removed a worktree locked by a live process: %v", err)
	}
}

// A lock is not advisory: while an agent is working, a concurrent cleanup must not
// be able to pull the directory out from under it.
func TestLockBlocksRemoval(t *testing.T) {
	root := newRepo(t)
	r := open(t, root)
	tr, err := r.Add("w1")
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Lock(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "worktree", "remove", tr.Dir)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("git worktree remove succeeded on a locked worktree: %s", out)
	}
	list, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range list {
		if w.Dir == tr.Dir {
			if !w.Locked || w.LockPID != os.Getpid() {
				t.Errorf("List() = %+v, want it locked by pid %d", w, os.Getpid())
			}
			if !w.Mine {
				t.Errorf("List() = %+v, want Mine true for a worktree we created", w)
			}
			return
		}
	}
	t.Errorf("List() did not report %s", tr.Dir)
}

// Worktrees the user made themselves are listed but never pruned. Cleaning up
// after ourselves must not mean cleaning up after them.
func TestPruneIgnoresWorktreesWeDidNotCreate(t *testing.T) {
	root := newRepo(t)
	r := open(t, root)
	theirs := filepath.Join(t.TempDir(), "theirs")
	run(t, root, "worktree", "add", "-q", "--detach", theirs, "HEAD")

	removed, _, _, err := r.Prune()
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if has(removed, theirs) {
		t.Errorf("Prune removed %s, which the user created", theirs)
	}
	if _, err := os.Stat(theirs); err != nil {
		t.Errorf("the user's worktree is gone: %v", err)
	}
}

func TestIDsThatWouldEscapeAreRefused(t *testing.T) {
	root := newRepo(t)
	r := open(t, root)
	for _, id := range []string{"", "../evil", "a/b", "with space", "dot.name", "-flag", "glob*"} {
		if _, err := r.Add(id); err == nil {
			t.Errorf("Add(%q) succeeded, want a refusal", id)
		}
	}
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func entries(t *testing.T, dir string) int {
	t.Helper()
	e, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(e)
}

// unusedPID finds a pid that no process holds, so a lock can be made to look like
// it was left behind by a crash.
func unusedPID(t *testing.T) int {
	t.Helper()
	for pid := 30000; pid < 40000; pid += 7 {
		if err := syscallKill(pid); err != nil {
			return pid
		}
	}
	t.Skip("could not find an unused pid")
	return 0
}

// The two halves of "what is missing" have to work on a worktree that already
// exists, not only on one being created. That is the whole reason they are separate
// exported calls: a listing command inspects worktrees it did not make, and asks the
// parent for its ignored set once for all of them.
func TestMissingWorksOnAnExistingWorktree(t *testing.T) {
	root := newRepo(t)
	write(t, root, ".gitignore", "node_modules/\n.venv/\n")
	write(t, root, ".worktreeinclude", ".venv\n")
	write(t, root, "node_modules/dep/i.js", "x\n")
	write(t, root, ".venv/bin/python", "x\n")
	run(t, root, "add", "-A")
	run(t, root, "commit", "-qm", "ignore rules")
	r := open(t, root)

	tr, err := r.Add("w1")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	ignored, err := r.IgnoredPaths()
	if err != nil {
		t.Fatalf("IgnoredPaths: %v", err)
	}
	// Collapsed: one entry per ignored directory, not one per file inside it. This is
	// what makes the call cheap enough to run on every listing.
	if len(ignored) != 2 {
		t.Errorf("IgnoredPaths = %v, want the two directories collapsed", ignored)
	}

	// Attach forgets everything Add computed, which is the point: the answer has to
	// come from looking at the checkout, not from remembering.
	fresh := r.Attach(tr.Dir)
	got := fresh.Missing(ignored)
	if len(got) != 1 || got[0] != "node_modules/" {
		t.Errorf("Missing = %v, want only node_modules/ (.venv was carried in)", got)
	}
	// And it agrees with what Add recorded, or the listing and the agent's preamble
	// would tell a person two different stories.
	if strings.Join(got, " ") != strings.Join(tr.MissingIgnored, " ") {
		t.Errorf("Missing = %v but Add recorded %v", got, tr.MissingIgnored)
	}
}

// HoldsWork is what Prune consults, so a listing has to be able to consult it too.
// Both answers come from the same call rather than from two implementations that
// could disagree about whether a directory is safe to delete.
func TestHoldsWorkAgreesWithPrune(t *testing.T) {
	root := newRepo(t)
	r := open(t, root)
	clean, err := r.Add("clean")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	dirty, err := r.Add("dirty")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	write(t, dirty.Dir, "unsaved.txt", "work\n")

	if clean.HoldsWork() {
		t.Error("a clean worktree reports holding work; prune would never remove it")
	}
	if !dirty.HoldsWork() {
		t.Error("a worktree with uncommitted changes reports holding nothing")
	}

	removed, kept, _, err := r.Prune()
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(removed) != 1 || filepath.Base(removed[0]) != "clean" {
		t.Errorf("removed = %v, want the clean one", removed)
	}
	if len(kept) != 1 || filepath.Base(kept[0]) != "dirty" {
		t.Errorf("kept = %v, want the one holding work", kept)
	}
}

// A checkout in the middle of a git operation is not a starting point for anything.
// The failure this prevents was measured rather than imagined: carryDirty copies
// `git diff HEAD`, and during a conflict that diff contains the conflict markers, so
// the apply succeeds and the agent inside opens a file beginning "<<<<<<< HEAD".
func TestAddRefusesWhileTheCheckoutIsMidOperation(t *testing.T) {
	root := newRepo(t)
	write(t, root, "shared.txt", "line1\nline2\nline3\n")
	run(t, root, "add", "-A")
	run(t, root, "commit", "-qm", "base")

	r := open(t, root)

	// Two commits off the same base touching the same line, made the way Add and
	// Commit make them. Cherry-picking both leaves the parent conflicted.
	for _, c := range []struct{ id, body string }{
		{"subA", "line1\nBY A\nline3\n"},
		{"subB", "line1\nBY B\nline3\n"},
	} {
		tr, err := r.Add(c.id)
		if err != nil {
			t.Fatalf("Add %s: %v", c.id, err)
		}
		write(t, tr.Dir, "shared.txt", c.body)
		if _, ok, err := tr.Commit("subagent: " + c.id); err != nil || !ok {
			t.Fatalf("Commit %s: ok=%v err=%v", c.id, ok, err)
		}
		if err := tr.Remove(true); err != nil {
			t.Fatalf("Remove %s: %v", c.id, err)
		}
	}

	run(t, root, "cherry-pick", worktree.RefPrefix+"subA")
	// This one is expected to fail, so it does not go through run(), which fatals.
	cherryPick(t, root, worktree.RefPrefix+"subB")

	if _, err := r.Add("next"); err == nil {
		t.Fatal("Add succeeded while the checkout was mid-cherry-pick")
	} else {
		for _, want := range []string{"cherry-pick", "--abort", "coherent"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to mention %q", err, want)
			}
		}
	}

	// And it works again once the operation is resolved, or the guard would be a
	// dead end rather than a detour.
	cherryPickAbort(t, root)
	if _, err := r.Add("after"); err != nil {
		t.Errorf("Add after aborting the cherry-pick: %v", err)
	}
}

// An unresolved conflict can outlive the operation that caused it, once the marker
// files are gone but the index still has unmerged paths. The content on disk is
// still nobody's code, so it is refused for the same reason.
func TestAddRefusesWithUnmergedPathsAlone(t *testing.T) {
	root := newRepo(t)
	write(t, root, "shared.txt", "one\n")
	run(t, root, "add", "-A")
	run(t, root, "commit", "-qm", "base")
	r := open(t, root)

	run(t, root, "checkout", "-q", "-b", "side")
	write(t, root, "shared.txt", "side\n")
	run(t, root, "commit", "-qam", "side")
	run(t, root, "checkout", "-q", "main")
	write(t, root, "shared.txt", "main\n")
	run(t, root, "commit", "-qam", "main")
	mergeExpectingConflict(t, root, "side")

	if _, err := r.Add("x"); err == nil {
		t.Fatal("Add succeeded with unmerged paths in the index")
	} else if !strings.Contains(err.Error(), "merge") {
		t.Errorf("error = %v, want it to name the merge", err)
	}
}

func cherryPick(t *testing.T, dir, ref string) {
	t.Helper()
	cmd := exec.Command("git", "cherry-pick", ref)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("cherry-pick %s was expected to conflict but succeeded:\n%s", ref, out)
	}
}

func cherryPickAbort(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "cherry-pick", "--abort")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cherry-pick --abort: %v\n%s", err, out)
	}
}

func mergeExpectingConflict(t *testing.T, dir, branch string) {
	t.Helper()
	cmd := exec.Command("git", "merge", branch)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("merge %s was expected to conflict but succeeded:\n%s", branch, out)
	}
}
