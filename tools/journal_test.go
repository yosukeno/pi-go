package tools

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestDirJournalKeepsFirstPreImage(t *testing.T) {
	root := t.TempDir()
	j := NewDirJournal(filepath.Join(t.TempDir(), "j"), root)
	p := filepath.Join(root, "a.txt")

	j.BeforeChange(p, []byte("v1"), true)
	j.BeforeChange(p, []byte("v2"), true)

	base, ok := j.Base("a.txt")
	if !ok || string(base) != "v1" {
		t.Fatalf("Base = %q, %v; want the first pre-image v1", base, ok)
	}
	entries := j.List()
	if len(entries) != 1 {
		t.Fatalf("List = %d entries, want 1", len(entries))
	}
	if entries[0].Created {
		t.Error("existed=true must not mark the entry created")
	}
	if entries[0].LastMS < entries[0].FirstMS {
		t.Error("LastMS should track the latest touch")
	}
}

func TestDirJournalCreatedFileHasEmptyBase(t *testing.T) {
	j := NewDirJournal(filepath.Join(t.TempDir(), "j"), t.TempDir())
	p := filepath.Join(j.root, "new.txt")

	j.BeforeChange(p, nil, false)

	e, _ := j.Entry("new.txt")
	if !e.Created {
		t.Error("existed=false must mark the entry created")
	}
	base, ok := j.Base("new.txt")
	if !ok || len(base) != 0 {
		t.Errorf("created file's base = %q, %v; want empty and available", base, ok)
	}
}

func TestDirJournalSkipsHugePreImage(t *testing.T) {
	old := journalMaxBase
	journalMaxBase = 16
	defer func() { journalMaxBase = old }()

	j := NewDirJournal(filepath.Join(t.TempDir(), "j"), t.TempDir())
	j.BeforeChange(filepath.Join(j.root, "big.bin"), make([]byte, 64), true)

	e, _ := j.Entry("big.bin")
	if !e.NoBase {
		t.Error("over-cap pre-image must be marked NoBase")
	}
	if _, ok := j.Base("big.bin"); ok {
		t.Error("NoBase entry must not serve a base")
	}
}

func TestDirJournalSurvivesReload(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "j")
	root := t.TempDir()
	NewDirJournal(dir, root).BeforeChange(filepath.Join(root, "a.txt"), []byte("v1"), true)

	j2 := NewDirJournal(dir, root)
	base, ok := j2.Base("a.txt")
	if !ok || string(base) != "v1" {
		t.Fatalf("after reload: Base = %q, %v", base, ok)
	}
}

func TestDirJournalClear(t *testing.T) {
	j := NewDirJournal(filepath.Join(t.TempDir(), "j"), t.TempDir())
	j.BeforeChange(filepath.Join(j.root, "a.txt"), []byte("v1"), true)
	if err := j.Clear(); err != nil {
		t.Fatal(err)
	}
	if len(j.List()) != 0 {
		t.Fatal("Clear must empty the index")
	}
	if _, ok := j.Base("a.txt"); ok {
		t.Fatal("Clear must drop pre-images")
	}
}

func TestDirJournalEvictsLeastRecentlyTouched(t *testing.T) {
	oldBase, oldTotal := journalMaxBase, journalMaxTotal
	journalMaxBase, journalMaxTotal = 64, 100
	defer func() { journalMaxBase, journalMaxTotal = oldBase, oldTotal }()

	j := NewDirJournal(filepath.Join(t.TempDir(), "j"), t.TempDir())
	j.BeforeChange(filepath.Join(j.root, "old.txt"), make([]byte, 60), true)
	j.record("", filepath.Join(j.root, "mid.txt"), make([]byte, 60), true)
	// Touch old.txt so mid.txt becomes the eviction candidate... except old.txt
	// is already the oldest by first write; re-touching only moves LastMS.
	j.BeforeChange(filepath.Join(j.root, "new.txt"), make([]byte, 60), true)

	if _, ok := j.Base("new.txt"); !ok {
		t.Error("the incoming entry must always fit")
	}
	kept := 0
	for _, e := range j.List() {
		if !e.NoBase {
			kept++
		}
	}
	if kept > 1 {
		t.Errorf("total %d over cap 100 with 60-byte entries: kept %d bases, want <= 1", 180, kept)
	}
}

func TestDirJournalConcurrentFirstTouch(t *testing.T) {
	j := NewDirJournal(filepath.Join(t.TempDir(), "j"), t.TempDir())
	p := filepath.Join(j.root, "race.txt")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j.BeforeChange(p, []byte("v"), true)
		}()
	}
	wg.Wait()

	if n := len(j.List()); n != 1 {
		t.Fatalf("concurrent first touches produced %d entries, want 1", n)
	}
	if _, ok := j.Base("race.txt"); !ok {
		t.Fatal("the winning touch must have stored its pre-image")
	}
}

func TestDirJournalIgnoresPathsOutsideTheRoot(t *testing.T) {
	j := NewDirJournal(filepath.Join(t.TempDir(), "j"), t.TempDir())
	j.BeforeChange(filepath.Join(t.TempDir(), "elsewhere.txt"), []byte("x"), true)
	if len(j.List()) != 0 {
		t.Fatal("paths outside the root must not be journaled")
	}
	if _, err := os.Stat(filepath.Join(j.dir)); !os.IsNotExist(err) {
		t.Error("nothing was recorded, so nothing should have been created")
	}
}
