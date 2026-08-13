package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func shadowHarness(t *testing.T) (*ShadowRepo, string) {
	t.Helper()
	if _, err := NewShadowRepo(t.TempDir(), t.TempDir()); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	root := t.TempDir()
	r, err := NewShadowRepo(filepath.Join(t.TempDir(), "shadow"), root)
	if err != nil {
		t.Fatal(err)
	}
	return r, root
}

func writeRootFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readRootFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func changeSet(changes []FileChange) map[string]string {
	out := make(map[string]string, len(changes))
	for _, c := range changes {
		out[c.Path] = c.Status
	}
	return out
}

func TestCheckpointRestoreRoundTrip(t *testing.T) {
	r, root := shadowHarness(t)
	writeRootFile(t, root, "keep.txt", "v1")
	writeRootFile(t, root, "gone.txt", "to be deleted")
	writeRootFile(t, root, "node_modules/dep/index.js", "generated")

	if err := r.Checkpoint("rec1"); err != nil {
		t.Fatal(err)
	}

	// Everything the next run might do: modify, delete, create — plus touch
	// an ignored path, which must stay out of the whole affair.
	writeRootFile(t, root, "keep.txt", "v2")
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeRootFile(t, root, "new.txt", "created after")
	writeRootFile(t, root, "node_modules/dep/index.js", "generated v2")

	changes, ok := r.Preview("rec1")
	if !ok {
		t.Fatal("preview says there is no checkpoint")
	}
	got := changeSet(changes)
	want := map[string]string{"keep.txt": "M", "gone.txt": "D", "new.txt": "A"}
	if len(got) != len(want) {
		t.Fatalf("preview = %v, want %v", got, want)
	}
	for p, s := range want {
		if got[p] != s {
			t.Errorf("preview[%s] = %q, want %q (full: %v)", p, got[p], s, got)
		}
	}

	if err := r.Restore("rec1"); err != nil {
		t.Fatal(err)
	}
	if got := readRootFile(t, root, "keep.txt"); got != "v1" {
		t.Errorf("modified file = %q, want v1", got)
	}
	if got := readRootFile(t, root, "gone.txt"); got != "to be deleted" {
		t.Errorf("deleted file = %q, want it back", got)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Error("created file must be gone after the restore")
	}
	if got := readRootFile(t, root, "node_modules/dep/index.js"); got != "generated v2" {
		t.Errorf("ignored path = %q, want it untouched at v2", got)
	}
}

// The preview's numbers describe the restore's own impact — what the rewind
// will add and remove — and CJK paths must survive the trip unescaped (git's
// default core.quotepath octal-escapes them).
func TestPreviewReportsLineImpactAndKeepsCJKPaths(t *testing.T) {
	r, root := shadowHarness(t)
	writeRootFile(t, root, "测试文件/说明.md", "一\n二\n三\n")

	if err := r.Checkpoint("rec1"); err != nil {
		t.Fatal(err)
	}

	// Two lines out, three lines in, one CJK-named file created.
	writeRootFile(t, root, "测试文件/说明.md", "一\n甲\n乙\n丙\n")
	writeRootFile(t, root, "新建.md", "x\ny\nz") // no trailing newline: still 3 lines

	changes, ok := r.Preview("rec1")
	if !ok {
		t.Fatal("preview says there is no checkpoint")
	}
	byPath := make(map[string]FileChange, len(changes))
	for _, c := range changes {
		byPath[c.Path] = c
	}
	m, ok := byPath["测试文件/说明.md"]
	if !ok {
		t.Fatalf("CJK path mangled or missing: %v", changes)
	}
	// Since the checkpoint the file traded 2 old lines for 3 new ones: the
	// restore adds back the 2 and removes the 3.
	if m.Status != "M" || m.Added != 2 || m.Removed != 3 {
		t.Errorf("modified = %+v, want M +2 -3", m)
	}
	a, ok := byPath["新建.md"]
	if !ok {
		t.Fatalf("untracked file missing: %v", changes)
	}
	if a.Status != "A" || a.Added != 0 || a.Removed != 3 {
		t.Errorf("created = %+v, want A +0 -3", a)
	}
}

func TestPreviewMarksBinaryFilesWithoutALineCount(t *testing.T) {
	r, root := shadowHarness(t)
	writeRootFile(t, root, "bin.dat", "a\x00b")

	if err := r.Checkpoint("rec1"); err != nil {
		t.Fatal(err)
	}
	writeRootFile(t, root, "bin.dat", "a\x00b\x00c")

	changes, ok := r.Preview("rec1")
	if !ok {
		t.Fatal("preview says there is no checkpoint")
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %v", changes)
	}
	if changes[0].Added != -1 || changes[0].Removed != -1 {
		t.Errorf("binary = %+v, want -1/-1", changes[0])
	}
}

func TestPreviewWithoutACheckpoint(t *testing.T) {
	r, _ := shadowHarness(t)
	if _, ok := r.Preview("nope"); ok {
		t.Error("an unknown checkpoint must report unavailable")
	}
	if err := r.Restore("nope"); err != errNoCheckpoint {
		t.Errorf("restore = %v, want errNoCheckpoint", err)
	}
}

// The shadow branch forks with the transcript: a checkpoint taken after a
// restore must snapshot the restored state, not the abandoned one.
func TestCheckpointAfterRestoreForksTheHistory(t *testing.T) {
	r, root := shadowHarness(t)
	writeRootFile(t, root, "f.txt", "one")
	if err := r.Checkpoint("rec1"); err != nil {
		t.Fatal(err)
	}
	writeRootFile(t, root, "f.txt", "two")
	if err := r.Checkpoint("rec2"); err != nil {
		t.Fatal(err)
	}

	if err := r.Restore("rec1"); err != nil {
		t.Fatal(err)
	}
	writeRootFile(t, root, "f.txt", "three")
	if err := r.Checkpoint("rec3"); err != nil {
		t.Fatal(err)
	}

	// rec3's restore lands on "three"; the abandoned "two" is unreachable
	// from it even though rec2's ref still exists.
	if err := r.Restore("rec3"); err != nil {
		t.Fatal(err)
	}
	if got := readRootFile(t, root, "f.txt"); got != "three" {
		t.Errorf("after restore: %q, want three", got)
	}
	if err := r.Restore("rec2"); err != nil {
		t.Fatal(err)
	}
	if got := readRootFile(t, root, "f.txt"); got != "two" {
		t.Errorf("rec2 still restores its own state: %q, want two", got)
	}
}

// Reopening the same directory must keep the refs — a server restart cannot
// cost the checkpoints, or resuming a session loses its rewinds.
func TestCheckpointsSurviveReopening(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(t.TempDir(), "shadow")
	writeRootFile(t, root, "f.txt", "one")
	r, err := NewShadowRepo(dir, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Checkpoint("rec1"); err != nil {
		t.Fatal(err)
	}

	r2, err := NewShadowRepo(dir, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r2.Preview("rec1"); !ok {
		t.Error("reopened shadow repo lost the checkpoint")
	}
}

// The session dir can live inside the workspace (PIGO_SESSION_DIR pointing
// into the project): the shadow repo must not snapshot its own database, and
// the cleanup must not delete it.
func TestShadowRepoInsideTheWorkspaceIsExcluded(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, ".pi-go-data", "checkpoints")
	r, err := NewShadowRepo(inside, root)
	if err != nil {
		t.Fatal(err)
	}
	writeRootFile(t, root, "f.txt", "one")
	if err := r.Checkpoint("rec1"); err != nil {
		t.Fatal(err)
	}
	writeRootFile(t, root, "f.txt", "two")

	changes, ok := r.Preview("rec1")
	if !ok {
		t.Fatal("no checkpoint")
	}
	for _, c := range changes {
		if strings.HasPrefix(c.Path, ".pi-go-data/") {
			t.Fatalf("the repo snapshots its own dir: %v", changes)
		}
	}
	if err := r.Restore("rec1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(inside, "HEAD")); err != nil {
		t.Error("the cleanup ate the shadow repository itself")
	}
	if got := readRootFile(t, root, "f.txt"); got != "one" {
		t.Errorf("after restore: %q, want one", got)
	}
}

func TestFileChangeSortsForStablePreviews(t *testing.T) {
	// Not a property of the repo — a guard for the preview's consumers: the
	// diff order git emits is not an API, so anything showing a list wants a
	// stable one.
	changes := []FileChange{{Path: "b.txt", Status: "M"}, {Path: "a.txt", Status: "A"}}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	if changes[0].Path != "a.txt" {
		t.Errorf("got %v", changes)
	}
}

// The budget is the only guard against a workspace whose bulk has no name the
// noise list could have known: a vendored toolchain, a downloaded dataset. It
// must refuse before writing the objects, and it must refuse in a way the next
// run can recover from on its own.
func TestCheckpointDeclinesAnOversizedWorkTree(t *testing.T) {
	r, root := shadowHarness(t)
	r.maxBytes = 4 << 10

	writeRootFile(t, root, "src/main.go", "package main")
	writeRootFile(t, root, "vendor/blob.bin", strings.Repeat("x", 16<<10))

	err := r.Checkpoint("rec1")
	if !errors.Is(err, errTooLarge) {
		t.Fatalf("Checkpoint() = %v, want errTooLarge", err)
	}
	// The diagnostic has to name what to exclude; a number alone leaves the
	// person with nothing to act on.
	if !strings.Contains(err.Error(), "vendor/") {
		t.Errorf("error must name the biggest offender, got %q", err)
	}
	if _, ok := r.Preview("rec1"); ok {
		t.Error("a declined checkpoint must not leave a restore point behind")
	}
}

// Refusing is not the same as giving up: git decides what would be staged, so an
// oversized directory the project already ignores must not cost anything. This
// is why the budget asks git instead of walking the tree itself.
func TestCheckpointBudgetRespectsGitignore(t *testing.T) {
	r, root := shadowHarness(t)
	r.maxBytes = 4 << 10

	writeRootFile(t, root, ".gitignore", "vendor/\n")
	writeRootFile(t, root, "src/main.go", "package main")
	writeRootFile(t, root, "vendor/blob.bin", strings.Repeat("x", 16<<10))

	if err := r.Checkpoint("rec1"); err != nil {
		t.Fatalf("Checkpoint() = %v, want the ignored bulk not to count", err)
	}
	changes, ok := r.Preview("rec1")
	if !ok {
		t.Fatal("preview says there is no checkpoint")
	}
	for _, c := range changes {
		if strings.HasPrefix(c.Path, "vendor/") {
			t.Errorf("ignored path %q must stay out of the snapshot", c.Path)
		}
	}
}

// A nested repository's metadata is not the outer project's content, and
// snapshotting it would put a second .git under our own add -A. Its working
// files are still tracked: they are files in this tree like any other, and the
// alternative — disabling checkpointing entirely, which some agents do — trades
// a whole feature for an edge case.
func TestNestedGitDirIsExcluded(t *testing.T) {
	r, root := shadowHarness(t)
	writeRootFile(t, root, "sub/.git/HEAD", "ref: refs/heads/main")
	writeRootFile(t, root, "sub/app.go", "package app")

	if err := r.Checkpoint("rec1"); err != nil {
		t.Fatal(err)
	}
	out, err := r.git("ls-tree", "-r", "--name-only", "refs/checkpoints/rec1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, ".git/") {
		t.Errorf("a nested .git must not be snapshotted, tree:\n%s", out)
	}
	if !strings.Contains(out, "sub/app.go") {
		t.Errorf("the nested repository's files must still be tracked, tree:\n%s", out)
	}
}

// Every checkpoint commit parents the one before it, so deleting refs alone
// frees nothing — the branch keeps the whole chain reachable. This is the test
// that would fail if Prune stopped rewriting the survivors.
func TestPruneDropsOldPointsAndReclaimsTheDisk(t *testing.T) {
	r, root := shadowHarness(t)
	// Distinct, incompressible-enough content per point, so each one owns
	// objects that only it references.
	for i := 0; i < 6; i++ {
		writeRootFile(t, root, "big.bin", strings.Repeat(string(rune('a'+i)), 256<<10))
		if err := r.Checkpoint(fmt.Sprintf("rec%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	res, err := r.Prune(2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 4 || res.Kept != 2 {
		t.Errorf("Prune() removed %d kept %d, want 4 and 2", res.Removed, res.Kept)
	}
	if res.AfterKiB >= res.BeforeKiB {
		t.Errorf("store %dKiB -> %dKiB: pruning must actually free objects",
			res.BeforeKiB, res.AfterKiB)
	}
	// The discarded points are gone as restore points...
	for i := 0; i < 4; i++ {
		if _, ok := r.Preview(fmt.Sprintf("rec%d", i)); ok {
			t.Errorf("rec%d must be gone after the prune", i)
		}
	}
	// ...and the kept ones still restore, under the same names, despite their
	// commit ids having been rewritten.
	if err := r.Restore("rec4"); err != nil {
		t.Fatalf("Restore(rec4) after prune = %v", err)
	}
	if got := readRootFile(t, root, "big.bin"); got != strings.Repeat("e", 256<<10) {
		t.Errorf("restored content is not what rec4 held (len %d)", len(got))
	}
}

// The age rule and the count rule are separate policies; with a count that keeps
// everything, age alone must still collect.
func TestPruneDropsPointsPastTheMaxAge(t *testing.T) {
	r, root := shadowHarness(t)
	writeRootFile(t, root, "f.txt", "one")
	if err := r.Checkpoint("rec1"); err != nil {
		t.Fatal(err)
	}
	res, err := r.Prune(0, -1) // every point is already older than "now minus a negative age"
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 0 {
		t.Fatalf("a non-positive maxAge must disable the age rule, removed %d", res.Removed)
	}

	res, err = r.Prune(0, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 1 || res.Kept != 0 {
		t.Errorf("Prune(0, 1ns) removed %d kept %d, want 1 and 0", res.Removed, res.Kept)
	}
	// Emptying the store must leave a repository the next run can commit into.
	writeRootFile(t, root, "f.txt", "two")
	if err := r.Checkpoint("rec2"); err != nil {
		t.Fatalf("Checkpoint after a full prune = %v", err)
	}
	if _, ok := r.Preview("rec2"); !ok {
		t.Error("the run after a full prune must still get a restore point")
	}
}

// Nothing to prune is an answer, not an error: a workspace may simply never have
// been checkpointed, and the command still has to say where it looked.
func TestPruneCheckpointsWithoutAStore(t *testing.T) {
	sessionDir, cwd := t.TempDir(), t.TempDir()
	var out strings.Builder
	if err := PruneCheckpoints(&out, sessionDir, cwd, 100, time.Hour); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no checkpoints") {
		t.Errorf("output = %q, want it to say there are none", out.String())
	}
	if !strings.Contains(out.String(), CheckpointDir(sessionDir, cwd)) {
		t.Errorf("output = %q, want the location it looked in", out.String())
	}
	// Reporting must not create the store it just said was absent.
	if _, err := os.Stat(CheckpointDir(sessionDir, cwd)); !os.IsNotExist(err) {
		t.Error("a report must not create the shadow repository")
	}
}

// The version control endpoint answers with a state, always. A workspace that is
// not a repository is the normal case for a scratch directory, and an error there
// would be the panel claiming something is broken when nothing is.
func TestWorkspaceGitReportsAStateNotAnError(t *testing.T) {
	// filesHarness because this endpoint must not reach the model either.
	h := filesHarness(t)

	resp := h.do(http.MethodGet, "/api/workspace/git", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/workspace/git = %d, want 200 even outside a repository", resp.StatusCode)
	}
	var got struct {
		Repo        bool   `json:"repo"`
		Unavailable string `json:"unavailable"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	// The harness workspace is a bare temp directory. Either answer is a state:
	// no repository, or git missing from the image running the tests.
	if got.Repo {
		t.Errorf("a fresh temp workspace must not report a repository: %+v", got)
	}
}

// GitContext is off in the zero Config, and that has to mean the prompt says
// nothing about git — not that it says "unknown". A Manager nobody asked must not
// shell out to git on every session it creates.
func TestGitSectionIsSilentWhenNotAskedFor(t *testing.T) {
	h := filesHarness(t)
	if got := h.mgr.gitSection(h.mgr.Cwd()); got != "" {
		t.Errorf("gitSection() = %q, want empty with GitContext off", got)
	}
}
