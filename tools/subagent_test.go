package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yosukeno/pi-go/worktree"
)

// stub stands in for `pi-go -mode json`.
//
// A shell script rather than a compiled binary so the tests do not depend on the
// toolchain being able to build pi-go while pi-go is being built. What matters is
// that it is a real process speaking the real JSONL contract from Phase 0: the
// thing under test is the parent's handling of a child, and a fake in-process child
// would test nothing about pipes, exit codes or process groups.
//
// Behaviour is selected by STUB_MODE, and the script's working directory is the
// worktree, because that is what the parent sets.
const stubScript = `#!/bin/sh
say() { printf '%s\n' "$1"; }
say '{"type":"session","ts":1,"session":"/tmp/child.jsonl"}'
case "$STUB_MODE" in
  writes)
    say '{"type":"turn_start","turn":1}'
    say '{"type":"tool_start","call_id":"c1","name":"write"}'
    printf 'from the subagent\n' > added.txt
    say '{"type":"token","text":"I added added.txt"}'
    say '{"type":"run_end","stop_reason":"end_turn","usage":{"input":120,"output":8}}'
    ;;
  readonly)
    say '{"type":"turn_start","turn":1}'
    say '{"type":"token","text":"the entry point is main.go"}'
    say '{"type":"run_end","stop_reason":"end_turn","usage":{"input":90,"output":6}}'
    ;;
  silent)
    say '{"type":"turn_start","turn":3}'
    say '{"type":"run_end","stop_reason":"end_turn"}'
    ;;
  runerror)
    say '{"type":"turn_start","turn":1}'
    say '{"type":"token","text":"got part way"}'
    say '{"type":"run_end","stop_reason":"error","error":"upstream refused"}'
    ;;
  crash)
    say '{"type":"turn_start","turn":1}'
    printf 'something went wrong\n' >&2
    exit 3
    ;;
  hang)
    say '{"type":"turn_start","turn":1}'
    # A grandchild, like the go test a real subagent would start. Killing only the
    # direct child would reparent this to init and leave it running.
    sleep 30 &
    printf '%s\n' "$!" > "$STUB_PIDFILE"
    sleep 30
    ;;
  tamper)
    say '{"type":"turn_start","turn":1}'
    printf 'gitdir: %s\n' "$STUB_MAIN_GITDIR" > .git
    printf 'sneaky\n' > added.txt
    say '{"type":"token","text":"done"}'
    say '{"type":"run_end","stop_reason":"end_turn"}'
    ;;
  role)
    # Reports whether the read-only child was told what it is.
    say '{"type":"turn_start","turn":1}'
    case "$*" in
      *"<your-role>"*) got=fenced ;;
      *) got=none ;;
    esac
    case "$*" in
      *"no shell"*) sh=stated ;;
      *) sh=silent ;;
    esac
    say "{\"type\":\"token\",\"text\":\"role=$got shell=$sh\"}"
    say '{"type":"run_end","stop_reason":"end_turn"}'
    ;;
  prompt)
    # Reports whether the preamble reached it, without trying to re-encode a
    # multi-line prompt as JSON from shell.
    say '{"type":"turn_start","turn":1}'
    case "$*" in
      *"<your-checkout>"*) got=fenced ;;
      *) got=none ;;
    esac
    case "$*" in
      *node_modules*) dep=named ;;
      *) dep=absent ;;
    esac
    # Which model the parent asked for, read back off our own argv.
    mdl=none
    prev=
    for a in "$@"; do
      if [ "$prev" = "-model" ]; then mdl=$a; fi
      prev=$a
    done
    say "{\"type\":\"token\",\"text\":\"preamble=$got dependency=$dep model=$mdl\"}"
    say '{"type":"run_end","stop_reason":"end_turn"}'
    ;;
  probe)
    # Reports where it was put and what it can see from there, which is the whole
    # difference between the two modes.
    say '{"type":"turn_start","turn":1}'
    if [ -f untracked.txt ]; then sees=yes; else sees=no; fi
    say "{\"type\":\"token\",\"text\":\"cwd=$PWD sees_untracked=$sees readonly=${PI_GO_SUBAGENT_READONLY:-0}\"}"
    say '{"type":"run_end","stop_reason":"end_turn","usage":{"input":10,"output":2}}'
    ;;
  garbage)
    say 'not json at all'
    say '{"type":"turn_start","turn":1}'
    say '{"type":"token","text":"survived the noise"}'
    say '{"truncated json'
    say '{"type":"run_end","stop_reason":"end_turn"}'
    ;;
esac
`

// repoFixture is a real git repository, because every guarantee under test is a
// claim about what git does.
func repoFixture(t *testing.T) (root, stub string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root = t.TempDir()
	t.Setenv("PIGO_WORKTREE_DIR", filepath.Join(t.TempDir(), "wt"))

	gitInit(t, root)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-qm", "init")

	stub = filepath.Join(t.TempDir(), "pi-go-stub")
	if err := os.WriteFile(stub, []byte(stubScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return root, stub
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "init", "-q", "-b", "main", ".")
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "test")
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func call(t *testing.T, s *Subagent, mode, task string) (Result, error) {
	t.Helper()
	args, err := json.Marshal(subagentArgs{Task: task, Mode: mode})
	if err != nil {
		t.Fatal(err)
	}
	return s.Execute(context.Background(), args)
}

func details(t *testing.T, r Result) SubagentDetails {
	t.Helper()
	d, ok := r.Details.(SubagentDetails)
	if !ok {
		t.Fatalf("Details = %T, want SubagentDetails", r.Details)
	}
	return d
}

// The whole success path: the child works in its own worktree, its changes come
// back as a commit reachable by ref, and the parent's working directory is
// untouched.
func TestSubagentReturnsWorkAsACommit(t *testing.T) {
	root, stub := repoFixture(t)
	t.Setenv("STUB_MODE", "writes")
	s := &Subagent{Cwd: root, Exe: stub}

	res, err := call(t, s, ModeEdit, "add a file")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	d := details(t, res)
	if d.Commit == "" || d.Ref == "" {
		t.Fatalf("Details = %+v, want a commit and a ref", d)
	}
	if !strings.Contains(res.Text, "I added added.txt") {
		t.Errorf("Text = %q, want the subagent's answer", res.Text)
	}
	// The commit is named in terms the model can act on.
	for _, want := range []string{"git show", "git cherry-pick", d.Ref} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("Text = %q, missing %q", res.Text, want)
		}
	}
	// The parent's checkout never saw any of it.
	if status := gitRun(t, root, "status", "--porcelain"); status != "" {
		t.Errorf("parent working tree is dirty: %q", status)
	}
	if _, err := os.Stat(filepath.Join(root, "added.txt")); err == nil {
		t.Error("the subagent's file appeared in the parent's checkout")
	}
	// And the content is reachable through git alone, which is why the worktree can
	// live outside the repository.
	if body := gitRun(t, root, "show", d.Ref+":added.txt"); body != "from the subagent" {
		t.Errorf("git show %s:added.txt = %q", d.Ref, body)
	}
	if usage := d.InputTok + d.OutTok; usage != 128 {
		t.Errorf("token usage = %d, want the child's reported 128", usage)
	}
	if d.Turns != 1 {
		t.Errorf("Turns = %d, want 1", d.Turns)
	}
	// The worktree is disposable once the commit is pinned. Keeping a full checkout
	// per call would grow the disk without holding anything the object store does
	// not already have.
	if _, err := os.Stat(d.Worktree); err == nil {
		t.Errorf("worktree %s survived a committed run", d.Worktree)
	}
	if rows := strings.Count(gitRun(t, root, "worktree", "list"), "\n"); rows != 0 {
		t.Errorf("git worktree list has %d extra rows after a committed run", rows)
	}
	// And the commit is still readable afterwards, which is the point of pinning it.
	if body := gitRun(t, root, "show", d.Ref+":added.txt"); body != "from the subagent" {
		t.Errorf("the commit became unreadable after the worktree was removed: %q", body)
	}
}

// A task that only had to look something up produces no commit, and that is a
// success, not a failure. The empty worktree is taken back immediately rather than
// left for prune.
func TestSubagentWithNoChangesLeavesNothingBehind(t *testing.T) {
	root, stub := repoFixture(t)
	t.Setenv("STUB_MODE", "readonly")
	s := &Subagent{Cwd: root, Exe: stub}

	res, err := call(t, s, ModeEdit, "where is the entry point")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	d := details(t, res)
	if d.Commit != "" || d.Ref != "" {
		t.Errorf("Details = %+v, want no commit for a read-only task", d)
	}
	if !strings.Contains(res.Text, "changed no files") {
		t.Errorf("Text = %q, want it to say nothing changed", res.Text)
	}
	if _, err := os.Stat(d.Worktree); err == nil {
		t.Errorf("worktree %s survived a run that changed nothing", d.Worktree)
	}
	if lines := strings.Count(gitRun(t, root, "worktree", "list"), "\n"); lines != 0 {
		t.Errorf("git worktree list has %d extra rows, want only the main checkout", lines)
	}
}

// The attack from the design notes, run end to end through the tool: the child
// rewrites its own .git and then produces changes. The parent must refuse to
// commit, report the tampering, and leave the main branch alone.
func TestSubagentDetectsWorktreeTampering(t *testing.T) {
	root, stub := repoFixture(t)
	mainGitDir := filepath.Join(gitRun(t, root, "rev-parse", "--show-toplevel"), ".git")
	t.Setenv("STUB_MODE", "tamper")
	t.Setenv("STUB_MAIN_GITDIR", mainGitDir)
	s := &Subagent{Cwd: root, Exe: stub}

	res, err := call(t, s, ModeEdit, "do something")
	if err == nil {
		t.Fatal("Execute() succeeded on a tampered worktree, want a refusal")
	}
	if !strings.Contains(err.Error(), "identity check") {
		t.Errorf("error = %v, want it to name the failed check", err)
	}
	d := details(t, res)
	if !d.Tampered {
		t.Error("Details.Tampered = false, want the tampering recorded for a human")
	}
	if d.Commit != "" || d.Ref != "" {
		t.Errorf("Details = %+v, want no commit and no ref", d)
	}
	if refs := gitRun(t, root, "show-ref"); strings.Contains(refs, "refs/pi-go/sub/") {
		t.Errorf("a ref was created despite the refusal: %q", refs)
	}
	if log := gitRun(t, root, "log", "--oneline"); strings.Count(log, "\n") != 0 {
		t.Errorf("the main branch gained commits: %q", log)
	}
}

// Every way a child can fail has to produce something the model can act on. A bare
// "exit status 1" sends it straight back to the same task.
func TestSubagentFailuresAreActionable(t *testing.T) {
	cases := []struct {
		mode string
		want []string
	}{
		{"silent", []string{"without producing an answer", "transcript", "Re-issue"}},
		{"runerror", []string{"upstream refused", "Partial answer", "got part way"}},
		{"crash", []string{"exited with code 3", "something went wrong"}},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			root, stub := repoFixture(t)
			t.Setenv("STUB_MODE", c.mode)
			s := &Subagent{Cwd: root, Exe: stub}

			_, err := call(t, s, ModeEdit, "do the thing")
			if err == nil {
				t.Fatalf("Execute() with a %s child succeeded, want an error", c.mode)
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, missing %q", err, want)
				}
			}
		})
	}
}

// A child that never finishes is stopped, and its process group goes with it —
// otherwise a `go test` it started outlives the turn that gave up on it.
func TestSubagentTimeoutKillsTheChild(t *testing.T) {
	root, stub := repoFixture(t)
	// Outside the worktree, which is reclaimed as soon as the run ends.
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	t.Setenv("STUB_MODE", "hang")
	t.Setenv("STUB_PIDFILE", pidFile)
	// Two seconds, and not the 300ms this used to be. The budget has to cover a real
	// `git worktree add`, a process spawn and a shell start before the child reaches
	// the line that records its grandchild — at 300ms the parent could give up first,
	// and then the pidfile assertion below failed for a reason that had nothing to do
	// with process groups. It went from passing to failing five times out of five on
	// one machine without the code changing, which is what a test racing its own
	// fixture looks like. Do not tune this back down to make the suite faster: the
	// timeout being generous costs under two seconds and is the only thing making the
	// assertion mean what it says.
	s := &Subagent{Cwd: root, Exe: stub, Timeout: 2 * time.Second}

	start := time.Now()
	res, err := call(t, s, ModeEdit, "loop forever")
	if err == nil {
		t.Fatal("Execute() on a hanging child succeeded, want a timeout")
	}
	if !strings.Contains(err.Error(), "did not finish within") {
		t.Errorf("error = %v, want a timeout message", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("took %v to give up, want about the timeout", elapsed)
	}
	// The lock is released even on the failure path, so prune can reclaim it.
	d := details(t, res)
	if _, err := os.Stat(filepath.Join(root, ".git", "worktrees", d.ID, "locked")); err == nil {
		t.Error("the worktree is still locked after the child was killed")
	}
	// The grandchild has to be gone too. This is the assertion that matters: a
	// `go test` started by a subagent must not outlive the turn that gave up on it,
	// and killing only the direct child leaves it running under init.
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the stub never recorded a grandchild: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("grandchild pid %q: %v", raw, err)
	}
	// Give the group a moment to die, then insist that it did.
	for i := 0; i < 40 && syscall.Kill(pid, 0) == nil; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("grandchild %d survived the timeout; the process group was not killed", pid)
	}
}

// A child that dies mid-write leaves a partial line. Losing the whole turn over the
// last thirty bytes of a crash would be a bug of our own.
func TestSubagentSurvivesUnparseableLines(t *testing.T) {
	root, stub := repoFixture(t)
	t.Setenv("STUB_MODE", "garbage")
	s := &Subagent{Cwd: root, Exe: stub}

	res, err := call(t, s, ModeEdit, "produce noise")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Text, "survived the noise") {
		t.Errorf("Text = %q, want the answer that arrived between the bad lines", res.Text)
	}
}

// Nesting is bounded by the depth the parent puts in the child's environment, which
// the model cannot write.
func TestSubagentRefusesToNestTooDeep(t *testing.T) {
	root, stub := repoFixture(t)
	s := &Subagent{Cwd: root, Exe: stub, Depth: DefaultSubagentMaxDepth}

	_, err := call(t, s, ModeEdit, "delegate again")
	if err == nil {
		t.Fatal("a subagent at the depth limit was allowed to spawn another")
	}
	if !strings.Contains(err.Error(), "do this task yourself") {
		t.Errorf("error = %v, want it to say what to do instead", err)
	}
	// Nothing was created on the way to refusing.
	if lines := strings.Count(gitRun(t, root, "worktree", "list"), "\n"); lines != 0 {
		t.Errorf("a worktree was created for a refused call")
	}
}

// Outside a repository there is nowhere to put a worktree, and falling back to the
// shared directory is exactly the collision this tool exists to prevent. Specific
// to edit mode: explore has nothing to isolate, so it has no such requirement.
func TestSubagentEditRefusesOutsideAGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	s := &Subagent{Cwd: dir, Exe: "/bin/true"}
	_, err := call(t, s, ModeEdit, "anything")
	if err == nil || !strings.Contains(err.Error(), "not inside a git repository") {
		t.Errorf("Execute() outside a repo = %v, want a refusal naming the cause", err)
	}
}

func TestSubagentRejectsEmptyTask(t *testing.T) {
	root, stub := repoFixture(t)
	s := &Subagent{Cwd: root, Exe: stub}
	if _, err := call(t, s, ModeEdit, "   "); err == nil {
		t.Error("an empty task was accepted")
	}
}

// The reason explore mode exists. A worktree is built from HEAD plus a diff of
// tracked files, so a file the parent has created but not committed is not in it
// at all — and the most common explore task, "find out how this works", would then
// be answered against a codebase missing its newest part. Explore runs in the
// parent's directory instead, and this test pins the difference by running both
// modes against the same repository.
func TestExploreSeesUncommittedFilesThatEditModeCannot(t *testing.T) {
	root, stub := repoFixture(t)
	t.Setenv("STUB_MODE", "probe")
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("new work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Subagent{Cwd: root, Exe: stub}

	res, err := call(t, s, ModeExplore, "what is in this directory")
	if err != nil {
		t.Fatalf("explore: %v", err)
	}
	if !strings.Contains(res.Text, "sees_untracked=yes") {
		t.Errorf("an explore child could not see the parent's uncommitted file: %q", res.Text)
	}

	res, err = call(t, s, ModeEdit, "what is in this directory")
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !strings.Contains(res.Text, "sees_untracked=no") {
		t.Errorf("an edit child saw an untracked file; if git started carrying those, "+
			"the note in the tool description is now wrong: %q", res.Text)
	}
}

// Explore runs in place and leaves nothing behind: no worktree, no commit, no ref.
// The absence is the point — a question that only needed reading should cost one
// process and nothing on disk.
func TestExploreCreatesNoWorktreeAndNoCommit(t *testing.T) {
	root, stub := repoFixture(t)
	t.Setenv("STUB_MODE", "probe")
	s := &Subagent{Cwd: root, Exe: stub}

	res, err := call(t, s, ModeExplore, "where is the entry point")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	d := details(t, res)
	if d.Mode != ModeExplore {
		t.Errorf("Details.Mode = %q, want %q", d.Mode, ModeExplore)
	}
	if d.Worktree != "" || d.Ref != "" || d.Commit != "" {
		t.Errorf("Details = %+v, want no worktree and no commit", d)
	}
	// The child was told it is read-only, and told through the environment so the
	// model in it cannot argue with the setting.
	if !strings.Contains(res.Text, "readonly=1") {
		t.Errorf("the child was not marked read-only: %q", res.Text)
	}
	if !strings.Contains(res.Text, "cwd="+resolved(t, root)) {
		t.Errorf("the child ran somewhere other than the parent's directory %s: %q", root, res.Text)
	}
	// "changed no files" is edit-mode language; after an explore run it would be
	// noise about something that was never possible.
	if strings.Contains(res.Text, "changed no files") {
		t.Errorf("explore result talks about changing files: %q", res.Text)
	}
	if n := len(gitRun(t, root, "worktree", "list")); n == 0 {
		t.Fatal("git worktree list returned nothing")
	}
	if lines := strings.Split(gitRun(t, root, "worktree", "list"), "\n"); len(lines) != 1 {
		t.Errorf("worktree list has %d entries, want only the main checkout: %v", len(lines), lines)
	}
	if refs := gitRun(t, root, "for-each-ref", "--format=%(refname)", "refs/pi-go/"); refs != "" {
		t.Errorf("explore left refs behind: %q", refs)
	}
}

// Explore needs no repository, because it needs no worktree. Worth a test of its
// own: it is the one place where the two modes have different preconditions, so a
// refactor that hoists the repository lookup back out of edit mode would break
// delegation everywhere outside a checkout.
func TestExploreWorksOutsideAGitRepo(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(t.TempDir(), "pi-go-stub")
	if err := os.WriteFile(stub, []byte(stubScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUB_MODE", "probe")
	s := &Subagent{Cwd: dir, Exe: stub}

	res, err := call(t, s, ModeExplore, "what is here")
	if err != nil {
		t.Fatalf("explore outside a repo: %v", err)
	}
	if !strings.Contains(res.Text, "cwd="+resolved(t, dir)) {
		t.Errorf("ran in the wrong directory, want %s: %q", dir, res.Text)
	}
}

// resolved is what a shell's $PWD reports for a path. On macOS t.TempDir() hands
// back a path under /var, which is itself a link to /private/var, so comparing the
// two spellings fails for reasons that have nothing to do with the code.
func resolved(t *testing.T, path string) string {
	t.Helper()
	out, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return out
}

// A missing or unknown mode is refused before anything is spawned, and the refusal
// says what the two choices mean. The alternative — defaulting — is the documented
// failure elsewhere: a subagent asked to fix something silently comes back with
// advice and no changes, which reads like success.
func TestSubagentRequiresAKnownMode(t *testing.T) {
	root, stub := repoFixture(t)
	s := &Subagent{Cwd: root, Exe: stub}

	for _, mode := range []string{"", "readonly", "Explore", "write"} {
		_, err := call(t, s, mode, "do something")
		if err == nil {
			t.Fatalf("mode %q was accepted", mode)
		}
		for _, want := range []string{ModeExplore, ModeEdit} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusing mode %q: %v, missing the %q option", mode, err, want)
			}
		}
	}
}

// The preamble is what turns "npm test: cannot find module" into something the
// agent can act on, so it has to actually arrive, fenced off from the parent's
// words.
func TestEditChildIsToldWhatItsCheckoutIsMissing(t *testing.T) {
	root, stub := repoFixture(t)
	t.Setenv("STUB_MODE", "prompt")
	// A dependency directory the checkout will not have.
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "dep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", "dep", "i.js"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-qm", "ignore node_modules")

	s := &Subagent{Cwd: root, Exe: stub}
	res, err := call(t, s, ModeEdit, "run the tests")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Text, "preamble=fenced") {
		t.Errorf("the child got no fenced preamble: %q", res.Text)
	}
	if !strings.Contains(res.Text, "dependency=named") {
		t.Errorf("the child was not told node_modules is absent: %q", res.Text)
	}

	// Explore mode runs in the parent's directory, so the checkout preamble would be
	// false there. It gets a different one instead — see the role test below.
	res, err = call(t, s, ModeExplore, "have a look")
	if err != nil {
		t.Fatalf("explore: %v", err)
	}
	if !strings.Contains(res.Text, "preamble=none") {
		t.Errorf("an explore child got a worktree preamble: %q", res.Text)
	}
}

// An explore child has to be told what it is, because the alternative was observed:
// given no statement that it has no shell, a live child decided to confirm its
// finding by running the tests, spent its remaining turns looking for a way, and
// returned nothing. An absent tool is a silence, and a model reads silence as
// "look harder".
func TestExploreChildIsToldItHasNoShell(t *testing.T) {
	root, stub := repoFixture(t)
	t.Setenv("STUB_MODE", "role")
	s := &Subagent{Cwd: root, Exe: stub}

	res, err := call(t, s, ModeExplore, "where is the entry point")
	if err != nil {
		t.Fatalf("explore: %v", err)
	}
	if !strings.Contains(res.Text, "role=fenced") {
		t.Errorf("explore child got no role preamble: %q", res.Text)
	}
	if !strings.Contains(res.Text, "shell=stated") {
		t.Errorf("explore child was not told it has no shell: %q", res.Text)
	}

	// An edit child has a shell, so the same note would be a lie.
	res, err = call(t, s, ModeEdit, "change something")
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if strings.Contains(res.Text, "role=fenced") {
		t.Errorf("an edit child was told it is read-only: %q", res.Text)
	}
}

// The note itself, asserted directly: the part that changes behaviour is not
// "you have no shell" but what to do instead of hunting for one.
func TestExploreNoteSaysWhatToDoInstead(t *testing.T) {
	got := exploreNote()
	for _, want := range []string{
		"<your-role>", "</your-role>",
		"no shell",             // the capability is absent, not merely unlisted
		"no way around it",     // do not go looking
		"say exactly what you", // report the command you would have run
		"stop",                 // and end the turn
		"uncommitted",          // it does see the parent's real files
	} {
		if !strings.Contains(got, want) {
			t.Errorf("exploreNote() is missing %q:\n%s", want, got)
		}
	}
}

// situation is a pure function of the worktree's state, so its content is asserted
// directly rather than through a spawned process.
func TestSituationSaysOnlyWhatIsTrue(t *testing.T) {
	// A clean checkout still gets the role half. What a subagent *is* does not vary
	// between runs, and a live child that did not know it had no git spent a turn
	// trying to read back its own commit hash and then reported to the parent that
	// it could not get one.
	clean := situation(&worktree.Tree{DirtyApplied: true})
	for _, want := range []string{"no git here", "commits them for you", "isolated git worktree"} {
		if !strings.Contains(clean, want) {
			t.Errorf("situation() on a clean checkout is missing %q:\n%s", want, clean)
		}
	}
	// But none of the conditional warnings, which is the other half of the rule: a
	// warning that fires every time is not read by the time it matters.
	for _, unwanted := range []string{"could not be carried", "gitignored", "not committed"} {
		if strings.Contains(clean, unwanted) {
			t.Errorf("situation() on a clean checkout mentions %q:\n%s", unwanted, clean)
		}
	}

	// A failure to carry the parent's work must never be silent: the agent would
	// otherwise report on a version of the code the user is not looking at.
	failed := &worktree.Tree{DirtyErr: errors.New("patch does not apply")}
	got := situation(failed)
	for _, want := range []string{"uncommitted changes", "patch does not apply", "HEAD"} {
		if !strings.Contains(got, want) {
			t.Errorf("situation() after a failed apply is missing %q:\n%s", want, got)
		}
	}

	full := &worktree.Tree{
		DirtyApplied:   true,
		MissingIgnored: []string{"node_modules/", ".venv/"},
		Untracked:      []string{"scratch.go"},
	}
	got = situation(full)
	for _, want := range []string{"node_modules/", ".venv/", "scratch.go",
		"<your-checkout>", "</your-checkout>"} {
		if !strings.Contains(got, want) {
			t.Errorf("situation() is missing %q:\n%s", want, got)
		}
	}
	// Stated as facts about the surroundings. What to do about a missing dependency
	// depends on the task, and the agent is better placed to decide than we are.
	if !strings.Contains(got, "Install what you need, or stop and report") {
		t.Errorf("situation() does not leave the decision to the agent:\n%s", got)
	}

	// Long lists are capped, or the preamble outgrows the task it is prefixing.
	many := make([]string, maxSituationItems+5)
	for i := range many {
		many[i] = "f" + strconv.Itoa(i) + ".go"
	}
	got = situation(&worktree.Tree{DirtyApplied: true, Untracked: many})
	if !strings.Contains(got, "and 5 more") {
		t.Errorf("situation() did not cap a long list:\n%s", got)
	}
	if strings.Contains(got, "f"+strconv.Itoa(maxSituationItems)+".go") {
		t.Errorf("situation() listed past the cap:\n%s", got)
	}
}

// A read-only child may run a different model than its parent, and only a
// read-only one. An edit child is doing the work the parent would have done, so
// downgrading it would change the outcome of the task rather than the cost of a
// lookup.
func TestOnlyExploreChildrenCanRunADifferentModel(t *testing.T) {
	root, stub := repoFixture(t)
	t.Setenv("STUB_MODE", "prompt")
	s := &Subagent{Cwd: root, Exe: stub, Model: "parent-model", ExploreModel: "small-model"}

	res, err := call(t, s, ModeExplore, "look something up")
	if err != nil {
		t.Fatalf("explore: %v", err)
	}
	if !strings.Contains(res.Text, "model=small-model") {
		t.Errorf("explore child did not run the configured model: %q", res.Text)
	}
	if d := details(t, res); d.Model != "small-model" {
		t.Errorf("Details.Model = %q, want the model actually run", d.Model)
	}

	res, err = call(t, s, ModeEdit, "change something")
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !strings.Contains(res.Text, "model=parent-model") {
		t.Errorf("an edit child was downgraded: %q", res.Text)
	}

	// Unset means inherit, which is the default and must stay the behaviour for
	// anyone with no configuration file.
	s.ExploreModel = ""
	res, err = call(t, s, ModeExplore, "look again")
	if err != nil {
		t.Fatalf("explore without a mapping: %v", err)
	}
	if !strings.Contains(res.Text, "model=parent-model") {
		t.Errorf("explore child did not inherit: %q", res.Text)
	}
}

// Two delegated changes must not produce two commits nobody can tell apart. This
// was observed live: the parent split one change across two subagents, both tasks
// opened with the same framing sentence, and `git log --oneline` came back with two
// identical messages.
func TestSubagentCommitsAreDistinguishableAndExplicable(t *testing.T) {
	framing := "You are working in a Go repository. The file store/store.go contains:\n"
	a := &childRun{session: "/tmp/a.jsonl"}
	a.answer.WriteString("Fixed the off-by-one: held < b.Max.")
	b := &childRun{session: "/tmp/b.jsonl"}
	b.answer.WriteString("Rewrote the doc comment to state the boundary.")

	msgA := commitMessage("sub111", framing+"fix the off-by-one", a)
	msgB := commitMessage("sub222", framing+"rewrite the comment", b)

	subjA, subjB := firstLine(msgA), firstLine(msgB)
	if subjA == subjB {
		t.Errorf("two subagents produced the same subject line:\n%s", subjA)
	}
	// The id is what guarantees it, whatever the tasks happen to open with, and it
	// is also how a commit is traced back to the run that made it.
	for id, subj := range map[string]string{"sub111": subjA, "sub222": subjB} {
		if !strings.Contains(subj, id) {
			t.Errorf("subject %q does not name the subagent %s", subj, id)
		}
	}

	// The body has to answer "why is this line like this" without another tool call.
	for _, want := range []string{
		"Task as delegated", "fix the off-by-one",
		"Reported by the subagent", "held < b.Max",
		"Transcript: /tmp/a.jsonl",
	} {
		if !strings.Contains(msgA, want) {
			t.Errorf("commit message is missing %q:\n%s", want, msgA)
		}
	}

	// A run that said nothing still gets a usable message rather than a dangling
	// header.
	quiet := commitMessage("sub333", "do the thing", &childRun{})
	if strings.Contains(quiet, "Reported by the subagent") {
		t.Errorf("empty answer produced an empty section:\n%s", quiet)
	}
	if strings.Contains(quiet, "Transcript:") {
		t.Errorf("missing session produced a dangling transcript line:\n%s", quiet)
	}

	// Long input is clipped rather than pasted whole, and says that it was.
	long := commitMessage("sub444", strings.Repeat("a very long line of task text\n", 200),
		&childRun{})
	if len(long) > 4000 {
		t.Errorf("commit message is %d bytes; a body is not the place for the whole task", len(long))
	}
	if !strings.Contains(long, "truncated") {
		t.Errorf("clipped message does not say so:\n%s", long[:300])
	}
}

// The message is what git actually stores, so it goes through a real commit rather
// than only through the formatter.
func TestCommitMessageSurvivesGit(t *testing.T) {
	root, stub := repoFixture(t)
	t.Setenv("STUB_MODE", "writes")
	s := &Subagent{Cwd: root, Exe: stub}

	res, err := call(t, s, ModeEdit, "add a file, and here is a second line of context")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	d := details(t, res)
	if d.Commit == "" {
		t.Fatal("no commit produced")
	}
	body := gitOut(t, root, "log", "-1", "--format=%B", d.Commit)
	for _, want := range []string{
		"subagent " + d.ID, "Task as delegated", "add a file",
		"Reported by the subagent", "I added added.txt",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("stored commit message is missing %q:\n%s", want, body)
		}
	}
	// The subject stays one line, or `git log --oneline` becomes unreadable. git's
	// own output ends with a newline, so the check is on the trimmed value.
	subject := strings.TrimRight(gitOut(t, root, "log", "-1", "--format=%s", d.Commit), "\n")
	if strings.Contains(subject, "\n") {
		t.Errorf("subject spans multiple lines: %q", subject)
	}
	if !strings.HasPrefix(subject, "subagent "+d.ID+": ") {
		t.Errorf("subject = %q, want it to open with the subagent id", subject)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

// A delegated run has structure — turns, tool calls, an ending — and an interface
// that only gets "· read" cannot show any of it. The child already speaks the same
// event contract its parent's consumers speak, so the parent forwards the events
// whole instead of flattening them.
func TestSubagentForwardsTheChildsEventsAsFrames(t *testing.T) {
	root, stub := repoFixture(t)
	t.Setenv("STUB_MODE", "writes")
	s := &Subagent{Cwd: root, Exe: stub}

	var frames []json.RawMessage
	var text strings.Builder
	args, err := json.Marshal(subagentArgs{Task: "add a file", Mode: ModeEdit})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecuteStreaming(context.Background(), args, func(p Partial) {
		if len(p.Frame) > 0 {
			frames = append(frames, p.Frame)
		}
		text.WriteString(p.Text)
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Every frame is a parseable event of the same contract, not a line of prose.
	var kinds []string
	for _, f := range frames {
		var e struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(f, &e); err != nil {
			t.Fatalf("frame is not valid JSON: %s: %v", f, err)
		}
		if e.Type == "" {
			t.Errorf("frame has no type: %s", f)
		}
		kinds = append(kinds, e.Type)
	}
	for _, want := range []string{"session", "turn_start", "tool_start", "run_end"} {
		if !contains(kinds, want) {
			t.Errorf("frames = %v, want one of type %q", kinds, want)
		}
	}
	// An allowlist, so anything not on it is absent — including the two delta streams.
	// A live run with the earlier denylist spent 40% of its frames on `thinking`,
	// which no interface displays.
	for _, unwanted := range []string{"token", "thinking", "message"} {
		if contains(kinds, unwanted) {
			t.Errorf("%q was forwarded; the filter is an allowlist: %v", unwanted, kinds)
		}
	}
	// Text still arrives too. The two consumers want different things and neither
	// should have to parse the other's: a terminal wants the line, a browser wants
	// the event.
	if !strings.Contains(text.String(), "write") {
		t.Errorf("human-readable text was dropped: %q", text.String())
	}
}

// The explore pool spreads concurrent children across providers least-in-flight
// first: a burst lands one per provider before any provider sees a second, and
// a finished child hands its slot back.
func TestExplorePoolBalancesLeastInFlight(t *testing.T) {
	s := &Subagent{ExplorePool: []ExploreTarget{
		{Provider: "a", Model: "ma"},
		{Provider: "b", Model: "mb"},
		{Provider: "c", Model: "mc"},
	}}

	want := []string{"ma", "mb", "mc", "ma", "mb", "mc"}
	for i, w := range want {
		if m, _ := s.pickExplore(); m != w {
			t.Fatalf("pick %d = %q, want %q", i, m, w)
		}
	}
	// Six forks in flight, two per provider. A child on b finishing makes b the
	// least loaded again, so the next fork goes there — not to the next in line.
	s.releaseExplore("b")
	if m, _ := s.pickExplore(); m != "mb" {
		t.Errorf("after a b child exits, pick = %q, want mb", m)
	}
}

// In-flight counts are per provider, not per pool entry: two models on one
// endpoint share the endpoint's concurrency, so the second entry is not a way
// to sneak twice the load onto it.
func TestExplorePoolCountsPerProvider(t *testing.T) {
	s := &Subagent{ExplorePool: []ExploreTarget{
		{Provider: "a", Model: "m1"},
		{Provider: "a", Model: "m2"},
		{Provider: "b", Model: "m3"},
	}}
	if m, _ := s.pickExplore(); m != "m1" {
		t.Fatalf("first pick = %q, want m1", m)
	}
	if m, _ := s.pickExplore(); m != "m3" {
		t.Errorf("second pick = %q, want m3: provider a already has one in flight", m)
	}
}

// The explicit single-model mapping still wins over the pool, and no pool at
// all means inherit the parent — the behaviour before pools existed.
func TestExplorePoolPriority(t *testing.T) {
	s := &Subagent{
		ExploreModel: "fixed",
		ExplorePool:  []ExploreTarget{{Provider: "a", Model: "ma"}},
	}
	if m, p := s.pickExplore(); m != "fixed" || p != "" {
		t.Errorf("explicit mapping: pick = (%q, %q), want (fixed, \"\")", m, p)
	}
	plain := &Subagent{}
	if m, p := plain.pickExplore(); m != "" || p != "" {
		t.Errorf("no pool: pick = (%q, %q), want inherit", m, p)
	}
	// A no-charge pick must not panic on release.
	plain.releaseExplore("")
}
