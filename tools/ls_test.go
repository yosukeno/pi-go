package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLsMarksDirectoriesAndSorts(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "Beta.go", "")
	write(t, dir, "alpha.go", "")
	write(t, dir, ".hidden", "")
	write(t, dir, "sub/inner.go", "")

	res, err := (&Ls{Cwd: dir}).Execute(context.Background(), args(t, lsArgs{}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".hidden", "alpha.go", "Beta.go", "sub/"}
	got := strings.Split(res.Text, "\n")
	if len(got) != len(want) {
		t.Fatalf("got %d entries %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: got %q, want %q", i, got[i], want[i])
		}
	}
	d, ok := res.Details.(LsDetails)
	if !ok {
		t.Fatalf("details are %T, want LsDetails", res.Details)
	}
	if d.Dirs != 1 || d.Files != 3 {
		t.Errorf("counts: got %d dirs / %d files, want 1 / 3", d.Dirs, d.Files)
	}
}

// A directory the model asked to list one level deep must not be listed
// recursively: the point of the entry cap is defeated if a nested tree walks
// itself into the context.
func TestLsIsOneLevelDeep(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "sub/deep/leaf.txt", "")

	res, err := (&Ls{Cwd: dir}).Execute(context.Background(), args(t, lsArgs{}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, "leaf.txt") || strings.Contains(res.Text, "deep") {
		t.Errorf("listing recursed into subdirectories: %q", res.Text)
	}
}

// The entry cap must announce itself with a usable next step, or the model has
// no way to know the listing was partial.
func TestLsEntryLimitIsActionable(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 12; i++ {
		write(t, dir, string(rune('a'+i))+".txt", "")
	}
	res, err := (&Ls{Cwd: dir}).Execute(context.Background(), args(t, lsArgs{Limit: 5}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "limit=10") {
		t.Errorf("truncation note should suggest a larger limit, got %q", res.Text)
	}
	d := res.Details.(LsDetails)
	if !d.EntryLimited || d.Entries != 5 {
		t.Errorf("details: got entries=%d limited=%v, want 5 / true", d.Entries, d.EntryLimited)
	}
}

func TestLsRejectsFilesAndEscapes(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "f.txt", "x")
	l := &Ls{Cwd: dir}

	if _, err := l.Execute(context.Background(), args(t, lsArgs{Path: "f.txt"})); err == nil {
		t.Error("listing a regular file should fail")
	}
	if _, err := l.Execute(context.Background(), args(t, lsArgs{Path: ".."})); err == nil {
		t.Error("listing outside the working directory should fail")
	}
}

func TestLsEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := (&Ls{Cwd: dir}).Execute(context.Background(), args(t, lsArgs{Path: "empty"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "(empty directory)" {
		t.Errorf("got %q", res.Text)
	}
}

// --- read-only roots ---

// The whole point of the extra roots: skill bundles live outside the project and
// must be readable there, while staying unwritable. Both halves are asserted
// together because widening the guard for reads is only safe if writes stay put.
func TestExtraRootsAreReadableButNotWritable(t *testing.T) {
	project := t.TempDir()
	skills := t.TempDir()
	skill := write(t, skills, "demo/SKILL.md", "---\nname: demo\ndescription: d\n---\n\nbody\n")
	roots := CanonicalRoots([]string{skills})

	if res, err := (&Read{Cwd: project, Roots: roots}).Execute(
		context.Background(), args(t, readArgs{Path: skill})); err != nil {
		t.Errorf("reading inside an extra root failed: %v", err)
	} else if !strings.Contains(res.Text, "body") {
		t.Errorf("unexpected content %q", res.Text)
	}

	if _, err := (&Ls{Cwd: project, Roots: roots}).Execute(
		context.Background(), args(t, lsArgs{Path: filepath.Join(skills, "demo")})); err != nil {
		t.Errorf("listing inside an extra root failed: %v", err)
	}

	// No extra roots are handed to the mutating tools, so the same path that just
	// read fine must be refused here.
	if _, err := (&Write{Cwd: project}).Execute(
		context.Background(), args(t, writeArgs{Path: skill, Content: "hijacked"})); err == nil {
		t.Error("write into an extra root should have been refused")
	}
	if read(t, skill) == "hijacked" {
		t.Fatal("the skill file was modified")
	}
}

// A root only widens its own subtree. Reaching past it, including through a
// symlink planted inside it, must still fail.
func TestExtraRootsDoNotWidenTheirParent(t *testing.T) {
	project := t.TempDir()
	parent := t.TempDir()
	if err := os.Mkdir(filepath.Join(parent, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	secret := write(t, parent, "secret.txt", "s3cret")
	link := filepath.Join(parent, "skills", "out")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	r := &Read{Cwd: project, Roots: CanonicalRoots([]string{filepath.Join(parent, "skills")})}
	if _, err := r.Execute(context.Background(), args(t, readArgs{Path: secret})); err == nil {
		t.Error("a sibling of the root should not be readable")
	}
	if _, err := r.Execute(context.Background(), args(t, readArgs{Path: link})); err == nil {
		t.Error("a symlink out of the root should not be readable")
	}
}

// Roots that do not exist grant nothing and must not survive canonicalisation,
// or an error message would advertise a directory the user never created.
func TestCanonicalRootsDropsMissingAndDuplicates(t *testing.T) {
	dir := t.TempDir()
	got := CanonicalRoots([]string{"", dir, dir, filepath.Join(dir, "nope")})
	if len(got) != 1 {
		t.Fatalf("got %d roots %q, want 1", len(got), got)
	}
}
