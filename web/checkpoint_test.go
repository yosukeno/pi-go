package web

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
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
