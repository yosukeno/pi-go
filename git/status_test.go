package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// run drives git in the fixture. Identity is passed per call rather than
// configured, so the test never depends on the machine's git config.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{
		"-c", "user.name=test", "-c", "user.email=test@local",
		"-c", "commit.gpgsign=false",
	}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func repo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	return dir
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	abs := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A directory under no version control is a state, not a failure. This is the
// case the whole package exists for: it is what was true of this project's own
// sibling repository while a hundred and thirty files sat uncommitted next door,
// and nothing anywhere said so.
func TestProbeOutsideARepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	s := Probe(t.TempDir())
	if s.Repo || s.Unavailable != "" {
		t.Fatalf("Probe() = %+v, want Repo=false with no Unavailable", s)
	}
	section := s.PromptSection()
	if !strings.Contains(section, "not under version control") {
		t.Errorf("prompt section must say so plainly, got:\n%s", section)
	}
	// It must not tell the model to go and create one. Codex's app offers; an
	// agent that runs `git init` because a directory looked untidy has made a
	// decision that was not its to make.
	if !strings.Contains(section, "do not initialise a repository unasked") {
		t.Errorf("prompt section must forbid initialising unasked, got:\n%s", section)
	}
}

// `git init` and nothing else. Told apart from "no repository" because the advice
// differs: the history exists here and is empty, rather than being absent.
func TestProbeUnbornRepository(t *testing.T) {
	dir := repo(t)
	s := Probe(dir)
	if !s.Repo || !s.Unborn {
		t.Fatalf("Probe() = %+v, want Repo and Unborn", s)
	}
	if s.Branch != "main" {
		t.Errorf("Branch = %q, want main", s.Branch)
	}
	if s.Head != "" {
		t.Errorf("Head = %q, want empty: there is no commit to name", s.Head)
	}
	if got := s.PromptSection(); !strings.Contains(got, "no commits yet") {
		t.Errorf("prompt section = %q, want it to say there are no commits", got)
	}
}

// A partially staged file is counted in both columns, because that is what is
// true of it — and it is the case a single "dirty" boolean would erase.
func TestProbeCountsEachKindOfUncommitted(t *testing.T) {
	dir := repo(t)
	write(t, dir, "committed.txt", "v1\n")
	write(t, dir, "partly.txt", "v1\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-qm", "first")

	write(t, dir, "staged.txt", "new\n") // untracked -> staged
	gitRun(t, dir, "add", "staged.txt")
	write(t, dir, "committed.txt", "v2\n") // tracked, unstaged
	write(t, dir, "partly.txt", "v2\n")    // staged...
	gitRun(t, dir, "add", "partly.txt")
	write(t, dir, "partly.txt", "v3\n") // ...and modified again
	write(t, dir, "untracked.txt", "x\n")

	s := Probe(dir)
	if s.Staged != 2 {
		t.Errorf("Staged = %d, want 2 (staged.txt, partly.txt)", s.Staged)
	}
	if s.Unstaged != 2 {
		t.Errorf("Unstaged = %d, want 2 (committed.txt, partly.txt again)", s.Unstaged)
	}
	if s.Untracked != 1 {
		t.Errorf("Untracked = %d, want 1", s.Untracked)
	}
	if !s.Dirty() {
		t.Error("Dirty() must be true")
	}
	if got := s.PromptSection(); !strings.Contains(got, "2 staged, 2 unstaged, 1 untracked") {
		t.Errorf("prompt section = %q, want the three counts", got)
	}
}

// Untracked files count as dirty. The backlog this package exists because of was
// almost entirely untracked, so a "clean" that ignored them would have gone on
// reporting clean the whole time.
func TestUntrackedAloneIsDirty(t *testing.T) {
	dir := repo(t)
	write(t, dir, "a.txt", "x\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-qm", "first")
	write(t, dir, "b.txt", "y\n")

	s := Probe(dir)
	if s.Staged+s.Unstaged != 0 {
		t.Fatalf("Probe() = %+v, want nothing staged or unstaged", s)
	}
	if !s.Dirty() {
		t.Error("an untracked file must make the tree dirty")
	}
	if got := s.PromptSection(); strings.Contains(got, "uncommitted: none") {
		t.Errorf("prompt section = %q, must not call this clean", got)
	}
}

func TestProbeCleanTreeSaysSo(t *testing.T) {
	dir := repo(t)
	write(t, dir, "a.txt", "x\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-qm", "only commit")

	s := Probe(dir)
	if s.Dirty() {
		t.Fatalf("Probe() = %+v, want a clean tree", s)
	}
	if s.Head == "" || s.Subject != "only commit" {
		t.Errorf("Head/Subject = %q/%q, want the commit named", s.Head, s.Subject)
	}
	// "clean" has to be stated: its absence would read as "not measured".
	if got := s.PromptSection(); !strings.Contains(got, "uncommitted: none") {
		t.Errorf("prompt section = %q, want it to say the tree is clean", got)
	}
}

// The repository root is not always the workspace. A session started one
// directory too high reports a parent repository's state, and the only way a
// person can notice is if the root is shown (anthropics/claude-code#5718).
func TestProbeReportsTheRepositoryRootFromASubdirectory(t *testing.T) {
	dir := repo(t)
	write(t, dir, "sub/a.txt", "x\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-qm", "first")

	s := Probe(filepath.Join(dir, "sub"))
	if !s.Repo {
		t.Fatal("a subdirectory of a repository is still in the repository")
	}
	// Compared through EvalSymlinks because a temp dir is behind one on macOS
	// and git reports the resolved path.
	want, _ := filepath.EvalSymlinks(dir)
	got, _ := filepath.EvalSymlinks(s.Root)
	if got != want {
		t.Errorf("Root = %q, want the repository top %q", s.Root, want)
	}
}

func TestProbeDetachedHead(t *testing.T) {
	dir := repo(t)
	write(t, dir, "a.txt", "x\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-qm", "first")
	gitRun(t, dir, "checkout", "-q", "--detach")

	s := Probe(dir)
	if !s.Detached || s.Branch != "" {
		t.Fatalf("Probe() = %+v, want Detached with no branch", s)
	}
	if got := s.PromptSection(); !strings.Contains(got, "HEAD: detached") {
		t.Errorf("prompt section = %q, want the detached state named", got)
	}
}

// branch.ab is "+<ahead> -<behind>", and getting the sign handling wrong would
// silently report zero divergence — which reads exactly like "in sync".
func TestProbeUpstreamDivergence(t *testing.T) {
	remote := t.TempDir()
	gitRun(t, remote, "init", "-q", "--bare", "-b", "main")

	dir := repo(t)
	write(t, dir, "a.txt", "x\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-qm", "first")
	gitRun(t, dir, "remote", "add", "origin", remote)
	gitRun(t, dir, "push", "-q", "-u", "origin", "main")

	write(t, dir, "b.txt", "y\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-qm", "second")

	s := Probe(dir)
	if s.Upstream != "origin/main" {
		t.Fatalf("Upstream = %q, want origin/main", s.Upstream)
	}
	if s.Ahead != 1 || s.Behind != 0 {
		t.Errorf("ahead/behind = %d/%d, want 1/0", s.Ahead, s.Behind)
	}
	if got := s.PromptSection(); !strings.Contains(got, "vs origin/main: 1 ahead, 0 behind") {
		t.Errorf("prompt section = %q, want the divergence spelled out", got)
	}
}

// The point of reporting counts instead of content: the section cannot grow with
// the repository. The only free-form text in it is the commit subject, so that is
// the only thing that has to be capped.
func TestPromptSectionStaysSmall(t *testing.T) {
	dir := repo(t)
	write(t, dir, "a.txt", "x\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-qm", strings.Repeat("long subject ", 60))
	// A hundred untracked files must not add a hundred lines.
	for i := 0; i < 100; i++ {
		write(t, dir, filepath.Join("many", string(rune('a'+i%26))+strings.Repeat("x", i)+".txt"), "y\n")
	}

	s := Probe(dir)
	section := s.PromptSection()
	if lines := strings.Count(section, "\n"); lines > 8 {
		t.Errorf("section has %d lines, want a handful:\n%s", lines, section)
	}
	if len(section) > 400 {
		t.Errorf("section is %d bytes, want it bounded:\n%s", len(section), section)
	}
	if !strings.Contains(section, "…") {
		t.Errorf("a 780-character subject must be truncated, got:\n%s", section)
	}
}

// A CJK subject cut on a byte boundary renders as a replacement character.
func TestSubjectTruncationRespectsRuneBoundaries(t *testing.T) {
	long := strings.Repeat("恶意代码分析", 40)
	got := truncate(long, 10)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncate() = %q, want it truncated", got)
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("truncate() = %q, want no replacement character", got)
	}
	if n := len([]rune(strings.TrimSuffix(got, "…"))); n != 10 {
		t.Errorf("kept %d runes, want 10", n)
	}
}

// Every failure has to be renderable. Nothing here may return an error, because
// the callers are a prompt builder and an HTTP handler and neither has anywhere
// to put one.
func TestProbeNeverPanicsOnAMissingDirectory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	s := Probe(filepath.Join(t.TempDir(), "does-not-exist"))
	if s.Repo {
		t.Errorf("Probe() = %+v, want no repository", s)
	}
	if got := s.PromptSection(); !strings.HasPrefix(got, "<git>") || !strings.HasSuffix(got, "</git>") {
		t.Errorf("prompt section = %q, want a well-formed block even so", got)
	}
}

// The paths exist for one rule, so the test is about that rule: what was already
// uncommitted when the session started is not the agent's, and the prompt has to
// name it. Counting cannot express that.
func TestProbeNamesWhatWasAlreadyDirty(t *testing.T) {
	dir := repo(t)
	write(t, dir, "tracked.txt", "v1\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-qm", "first")

	write(t, dir, "tracked.txt", "v2\n") // unstaged
	write(t, dir, "staged.txt", "new\n")
	gitRun(t, dir, "add", "staged.txt")
	write(t, dir, "untracked.txt", "x\n")

	s := Probe(dir)
	got := strings.Join(s.DirtyPaths, ",")
	if want := "staged.txt,tracked.txt,untracked.txt"; got != want {
		t.Errorf("DirtyPaths = %q, want %q (sorted, all three kinds)", got, want)
	}
	section := s.PromptSection()
	for _, p := range []string{"staged.txt", "tracked.txt", "untracked.txt"} {
		if !strings.Contains(section, p) {
			t.Errorf("prompt section is missing %q:\n%s", p, section)
		}
	}
	if !strings.Contains(section, "not yours") || !strings.Contains(section, "do not stage these") {
		t.Errorf("prompt section must say what the list means:\n%s", section)
	}
}

// A clean tree says nothing about pre-existing work, because there is none. The
// paragraph appearing with an empty list would read as "and here are none of
// them".
func TestProbeSaysNothingAboutDirtyPathsWhenClean(t *testing.T) {
	dir := repo(t)
	write(t, dir, "a.txt", "v1\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-qm", "first")

	s := Probe(dir)
	if len(s.DirtyPaths) != 0 {
		t.Fatalf("DirtyPaths = %v, want none", s.DirtyPaths)
	}
	if got := s.PromptSection(); strings.Contains(got, "not yours") {
		t.Errorf("prompt section = %q, want no pre-existing paragraph", got)
	}
}

// Porcelain v2 does not quote or escape paths, so a filename with a space arrives
// as the remainder of the line. Splitting on every space would truncate it, and a
// truncated path in this list is worse than no path: it names a file that does not
// exist while omitting one that does.
func TestProbeKeepsPathsWithSpacesAndCJKIntact(t *testing.T) {
	dir := repo(t)
	write(t, dir, "a.txt", "v1\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-qm", "first")

	write(t, dir, "two words.txt", "x\n")
	write(t, dir, "说明 文档.md", "x\n")
	// A wholly untracked directory: git collapses it to the directory itself,
	// which is the default this package keeps so its counts match what the user's
	// own `git status` reports. For the rule these paths serve, a collapsed
	// directory is still actionable — "do not stage 恶意代码/" is a usable
	// instruction, and expanding it could mean thousands of lines.
	write(t, dir, "恶意代码/样本.md", "x\n")

	s := Probe(dir)
	joined := strings.Join(s.DirtyPaths, "\n")
	for _, want := range []string{"two words.txt", "说明 文档.md", "恶意代码/"} {
		if !strings.Contains(joined, want) {
			t.Errorf("DirtyPaths = %v, want it to contain %q", s.DirtyPaths, want)
		}
	}
	// The octal escaping git does by default would show up here as backslashes.
	if strings.Contains(joined, `\3`) {
		t.Errorf("DirtyPaths = %v, want unescaped paths", s.DirtyPaths)
	}
}

// A rename entry carries a similarity score before the path and the original path
// after a tab. Only the new name is wanted, and the extra field must not shift the
// parse.
func TestProbeReadsRenamedPaths(t *testing.T) {
	dir := repo(t)
	write(t, dir, "old-name.txt", "v1\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-qm", "first")
	gitRun(t, dir, "mv", "old-name.txt", "new name.txt")

	s := Probe(dir)
	got := strings.Join(s.DirtyPaths, ",")
	if got != "new name.txt" {
		t.Errorf("DirtyPaths = %q, want just the new name", got)
	}
}

// Bounded, and honest about being bounded: the total still has to be reachable or
// "20 files" reads as the whole story.
func TestProbeCapsTheDirtyPathListAndSaysSo(t *testing.T) {
	dir := repo(t)
	write(t, dir, "a.txt", "v1\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-qm", "first")
	for i := 0; i < maxDirtyPaths+5; i++ {
		write(t, dir, fmt.Sprintf("f%02d.txt", i), "x\n")
	}

	s := Probe(dir)
	if len(s.DirtyPaths) != maxDirtyPaths {
		t.Errorf("DirtyPaths has %d entries, want the cap of %d", len(s.DirtyPaths), maxDirtyPaths)
	}
	section := s.PromptSection()
	if !strings.Contains(section, "and 5 more") {
		t.Errorf("prompt section must account for the remainder:\n%s", section)
	}
	// Still bounded overall: twenty short paths plus a header, not a thousand.
	if lines := strings.Count(section, "\n"); lines > 30 {
		t.Errorf("section has %d lines, want it bounded:\n%s", lines, section)
	}
}
