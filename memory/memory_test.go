package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

// userAt points the user layer at a temporary directory.
func userAt(t *testing.T, dir string) {
	t.Helper()
	t.Setenv(DirEnv, dir)
}

// The directory is created, not merely tolerated when absent.
//
// tools.CanonicalRoots drops a root that does not exist — reasonably, since a missing
// directory cannot grant anything — so without this the agent would have nowhere to
// write on a fresh install and no way to create it, the only place it could being the
// place it is not allowed to reach.
func TestLoadCreatesTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-yet")
	userAt(t, dir)

	s, diags := Load(Options{User: true})
	if len(diags) != 0 {
		t.Fatalf("diagnostics: %v", diags)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("the memory directory was not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("the memory path is not a directory")
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("mode = %o, want 0700: notes accumulate paths and hostnames from a "+
			"private codebase, and no other account needs them", got)
	}
	if roots := s.Roots(); len(roots) != 1 {
		t.Errorf("Roots() = %v, want the created directory", roots)
	}
}

// An empty memory produces no prompt section at all, so someone who never uses this
// feature pays nothing for it — not even the protocol sentences. Same rule skills
// follows.
func TestEmptyMemoryCostsNothingInThePrompt(t *testing.T) {
	userAt(t, t.TempDir())
	s, _ := Load(Options{User: true})

	if !s.Empty() {
		t.Error("a fresh directory does not report itself as empty")
	}
	if got := s.PromptSection(); got != "" {
		t.Errorf("PromptSection() = %q, want empty", got)
	}
	// But the root is still granted, or the agent could never write a first note.
	if len(s.Roots()) != 1 {
		t.Errorf("Roots() = %v, want the directory even though it is empty", s.Roots())
	}
}

func TestProjectLayerIsOffUnlessAsked(t *testing.T) {
	userAt(t, t.TempDir())
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, ".pi-go", DirName, "repo-note.md"), "from the repo", time.Time{})

	off, _ := Load(Options{Cwd: cwd, User: true})
	if strings.Contains(off.PromptSection(), "repo-note") {
		t.Error("a project note was loaded without -project-memory; notes arrive with a " +
			"checkout and speak as the model's own earlier conclusions")
	}
	for _, r := range off.Roots() {
		if strings.Contains(r, cwd) {
			t.Errorf("the project directory %s was granted as a root without -project-memory", r)
		}
	}

	on, _ := Load(Options{Cwd: cwd, User: true, Project: true})
	if !strings.Contains(on.PromptSection(), "repo-note") {
		t.Error("-project-memory did not load the project note")
	}
	if len(on.Roots()) != 2 {
		t.Errorf("Roots() = %v, want both layers", on.Roots())
	}
}

func TestNoMemoryGrantsNothing(t *testing.T) {
	userAt(t, t.TempDir())
	s, _ := Load(Options{Cwd: t.TempDir()})
	if len(s.Roots()) != 0 {
		t.Errorf("Roots() = %v, want none with both layers disabled", s.Roots())
	}
	if got := s.PromptSection(); got != "" {
		t.Errorf("PromptSection() = %q, want empty", got)
	}
}

// A nil Store has to behave like an empty one, because that is what -no-memory hands
// the web manager and there is no branch there to check it.
func TestNilStoreIsUsable(t *testing.T) {
	var s *Store
	if !s.Empty() {
		t.Error("a nil Store is not empty")
	}
	if s.Roots() != nil {
		t.Error("a nil Store has roots")
	}
	if got := s.PromptSection(); got != "" {
		t.Errorf("a nil Store rendered %q", got)
	}
	if s.Count() != 0 {
		t.Error("a nil Store counted files")
	}
}

// Newest first, because the listing is truncated and the order decides what survives
// truncation. A note written today is likelier to matter than one from last year.
func TestListingIsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	userAt(t, dir)
	now := time.Now()
	write(t, filepath.Join(dir, "old.md"), "x", now.Add(-100*24*time.Hour))
	write(t, filepath.Join(dir, "new.md"), "x", now.Add(-time.Hour))
	write(t, filepath.Join(dir, "middle.md"), "x", now.Add(-10*24*time.Hour))

	s, _ := Load(Options{User: true})
	got := s.PromptSection()
	iNew, iMid, iOld := strings.Index(got, "new.md"), strings.Index(got, "middle.md"), strings.Index(got, "old.md")
	if !(iNew < iMid && iMid < iOld) {
		t.Errorf("listing order is new=%d middle=%d old=%d, want newest first\n%s", iNew, iMid, iOld, got)
	}
}

// Two levels deep, matching the documented behaviour of Anthropic's memory view, so a
// directory organised for one harness reads the same in the other. Deeper files are
// still reachable by read and ls — the listing is a summary, not an index.
func TestListingStopsAtTwoLevels(t *testing.T) {
	dir := t.TempDir()
	userAt(t, dir)
	write(t, filepath.Join(dir, "top.md"), "x", time.Time{})
	write(t, filepath.Join(dir, "sub", "second.md"), "x", time.Time{})
	write(t, filepath.Join(dir, "sub", "deeper", "third.md"), "x", time.Time{})

	s, _ := Load(Options{User: true})
	got := s.PromptSection()
	if !strings.Contains(got, "top.md") || !strings.Contains(got, "second.md") {
		t.Errorf("listing is missing the first two levels:\n%s", got)
	}
	if strings.Contains(got, "third.md") {
		t.Errorf("listing reached the third level:\n%s", got)
	}
}

func TestListingSkipsDotFilesAndDirectories(t *testing.T) {
	dir := t.TempDir()
	userAt(t, dir)
	write(t, filepath.Join(dir, "kept.md"), "x", time.Time{})
	write(t, filepath.Join(dir, ".hidden"), "x", time.Time{})
	write(t, filepath.Join(dir, ".git", "config"), "x", time.Time{})

	got, _ := Load(Options{User: true})
	section := got.PromptSection()
	for _, unwanted := range []string{".hidden", ".git", "config"} {
		if strings.Contains(section, unwanted) {
			t.Errorf("listing includes %q:\n%s", unwanted, section)
		}
	}
	if !strings.Contains(section, "kept.md") {
		t.Errorf("listing lost the real note:\n%s", section)
	}
}

// The listing is in every prompt, so it is capped by count and by size — two limits
// because they fail differently, and one of them is not caught by counting.
func TestListingIsCappedAndSaysSo(t *testing.T) {
	dir := t.TempDir()
	userAt(t, dir)
	for i := range maxListFiles + 15 {
		write(t, filepath.Join(dir, "note-"+string(rune('a'+i%26))+string(rune('a'+i/26))+".md"), "x", time.Time{})
	}

	s, _ := Load(Options{User: true})
	got := s.PromptSection()
	if n := strings.Count(got, "<note "); n > maxListFiles {
		t.Errorf("listed %d notes, cap is %d", n, maxListFiles)
	}
	if !strings.Contains(got, "<truncated") {
		t.Errorf("a truncated listing did not say it was truncated; that teaches the "+
			"model that what it cannot see does not exist\n%s", got)
	}
	if !strings.Contains(got, "ls") {
		t.Error("the truncation notice does not say how to see the rest")
	}
	if len(got) > maxListBytes*3 {
		t.Errorf("section is %d bytes, far past the %d cap", len(got), maxListBytes)
	}
}

// The section sits in the cached prompt prefix, so it has to be byte-identical across
// calls within a session. A timestamp or a minute-resolution age would invalidate the
// cache every turn — the same trap the context-clearing placeholders avoid by having
// no clock in them.
func TestListingIsStableAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	userAt(t, dir)
	write(t, filepath.Join(dir, "a.md"), "x", time.Now().Add(-3*time.Hour))
	write(t, filepath.Join(dir, "b.md"), "y", time.Now().Add(-40*24*time.Hour))

	s, _ := Load(Options{User: true})
	first := s.PromptSection()
	time.Sleep(5 * time.Millisecond)
	if second := s.PromptSection(); first != second {
		t.Errorf("the section changed between calls, which invalidates the prompt cache "+
			"every turn:\n%q\nvs\n%q", first, second)
	}
	// And explicitly: no clock finer than a day reaches the text.
	for _, unwanted := range []string{":", "ms", "µs"} {
		if strings.Contains(strings.SplitN(first, "<memory>", 2)[1], unwanted) {
			t.Errorf("the listing contains %q, which suggests a sub-day clock:\n%s", unwanted, first)
		}
	}
}

func TestAgeIsCoarseAndRoundsDown(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{time.Hour, "today"},
		{23 * time.Hour, "today"},
		{30 * time.Hour, "1d"},
		{5 * 24 * time.Hour, "5d"},
		{45 * 24 * time.Hour, "1mo"},
		{200 * 24 * time.Hour, "6mo"},
		{800 * 24 * time.Hour, "2y"},
	}
	for _, tc := range cases {
		if got := age(now.Add(-tc.ago), now); got != tc.want {
			t.Errorf("age(%s ago) = %q, want %q", tc.ago, got, tc.want)
		}
	}
}

func TestHumanSizeMatchesTheMemoryToolConvention(t *testing.T) {
	cases := map[int64]string{0: "0B", 512: "512B", 1536: "1.5K", 2 << 20: "2.0M"}
	for in, want := range cases {
		if got := humanSize(in); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", in, got, want)
		}
	}
}

// An unwritable location degrades to "no memory" with a diagnostic, never to a failed
// start. Same rule checkpointing follows: the failure mode is unavailable, not blocked.
func TestAnUnusableDirectoryDegradesRatherThanBlocking(t *testing.T) {
	// A file where the directory should be, so MkdirAll cannot succeed.
	base := t.TempDir()
	blocked := filepath.Join(base, "occupied")
	write(t, blocked, "not a directory", time.Time{})
	userAt(t, blocked)

	s, diags := Load(Options{User: true})
	if len(diags) == 0 {
		t.Error("an unusable memory directory produced no diagnostic")
	}
	if len(s.Roots()) != 0 {
		t.Errorf("Roots() = %v, want none when the directory is unusable", s.Roots())
	}
	if got := s.PromptSection(); got != "" {
		t.Errorf("PromptSection() = %q, want empty", got)
	}
}
