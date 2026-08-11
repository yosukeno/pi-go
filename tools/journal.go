package tools

// A Journal snapshots a file's pre-image the first time anyone changes it, so
// a "what has this workspace become" view can diff the present against the
// past without git — the same idea as Claude Code's checkpointing
// (~/.claude/file-history) and Codex's TurnDiffTracker baseline: content
// snapshots, not version control.
//
// The hook lives in the edit and write tools, so bash writes and outside
// editors are NOT seen. That limitation is deliberate, and the same one both
// competitors document.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Caps are variables so tests can shrink them; production keeps the defaults.
var (
	// journalMaxBase skips pre-images above this size: a huge generated file's
	// history is not worth keeping, and the changes view degrades to stats.
	journalMaxBase = 1 << 20
	// journalMaxTotal bounds the whole journal directory; the least recently
	// touched entries are evicted first.
	journalMaxTotal = 256 << 20
)

// Journal is the edit/write hook: one call per change, with the file's
// content as it was just before. existed distinguishes "created" from "was
// empty" — both arrive as zero bytes.
type Journal interface {
	BeforeChange(absPath string, original []byte, existed bool)
}

// JournalEntry is one journaled path: where its pre-image lives and who
// touched it first.
type JournalEntry struct {
	Path    string `json:"path"`     // workspace-relative, slash-separated
	Base    string `json:"base"`     // pre-image file name inside the journal dir
	Size    int    `json:"size"`     // pre-image size in bytes
	Created bool   `json:"created"`  // the file did not exist at first touch
	NoBase  bool   `json:"no_base"`  // pre-image was too big to keep
	Sid     string `json:"sid"`      // session that first touched it ("" when unattributed)
	FirstMS int64  `json:"first_ms"` // first touch, epoch ms
	LastMS  int64  `json:"last_ms"`  // latest touch, epoch ms
}

// DirJournal is a Journal persisted as a directory of pre-images plus an
// index.json, keyed by workspace. It is safe for concurrent use, and every
// write is atomic (tmp + rename) so a crash mid-change cannot tear the index.
type DirJournal struct {
	dir   string // journal directory
	root  string // workspace root; paths are stored relative to it
	mu    sync.Mutex
	index map[string]JournalEntry
	total int // sum of kept pre-image sizes
}

// NewDirJournal opens (creating if needed) the journal at dir for the
// workspace rooted at root.
func NewDirJournal(dir, root string) *DirJournal {
	j := &DirJournal{dir: dir, root: root, index: map[string]JournalEntry{}}
	data, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err == nil {
		var entries []JournalEntry
		if json.Unmarshal(data, &entries) == nil {
			for _, e := range entries {
				j.index[e.Path] = e
				if !e.NoBase {
					j.total += e.Size
				}
			}
		}
		// A corrupt index is not fatal: the journal is a convenience, not a
		// ledger. Starting empty loses old diffs and nothing else.
	}
	return j
}

// ForSession stamps every record with the calling session's id, so the
// workspace-wide journal can still say which session touched a file first.
func (j *DirJournal) ForSession(sid string) Journal {
	return &sessionJournal{j: j, sid: sid}
}

type sessionJournal struct {
	j   *DirJournal
	sid string
}

func (s *sessionJournal) BeforeChange(absPath string, original []byte, existed bool) {
	s.j.record(s.sid, absPath, original, existed)
}

// BeforeChange records without session attribution (tests, direct use).
func (j *DirJournal) BeforeChange(absPath string, original []byte, existed bool) {
	j.record("", absPath, original, existed)
}

func (j *DirJournal) record(sid, absPath string, original []byte, existed bool) {
	rel, err := filepath.Rel(j.root, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return // outside the workspace: the journal only mirrors the root
	}
	rel = filepath.ToSlash(rel)

	j.mu.Lock()
	defer j.mu.Unlock()
	if e, ok := j.index[rel]; ok {
		// The pre-image exists by definition; only the touch time moves.
		e.LastMS = time.Now().UnixMilli()
		j.index[rel] = e
		j.persistLocked()
		return
	}

	e := JournalEntry{
		Path:    rel,
		Created: !existed,
		Sid:     sid,
		FirstMS: time.Now().UnixMilli(),
		LastMS:  time.Now().UnixMilli(),
	}
	sum := sha256.Sum256([]byte(rel))
	e.Base = hex.EncodeToString(sum[:])
	e.Size = len(original)
	if len(original) > journalMaxBase {
		e.NoBase = true
		e.Size = 0
	} else {
		j.evictLocked(len(original))
		if err := j.writeFileLocked(e.Base, original); err != nil {
			return // a journal we cannot write records nothing rather than lying
		}
	}
	j.index[rel] = e
	j.total += e.Size
	j.persistLocked()
}

// Base returns the recorded pre-image for rel. ok is false when the path was
// never journaled or its pre-image was too big to keep.
func (j *DirJournal) Base(rel string) (content []byte, ok bool) {
	j.mu.Lock()
	e, found := j.index[filepath.ToSlash(rel)]
	j.mu.Unlock()
	if !found || e.NoBase {
		return nil, false
	}
	data, err := os.ReadFile(filepath.Join(j.dir, e.Base))
	if err != nil {
		return nil, false
	}
	return data, true
}

// Entry reports the journal's record for rel, if any.
func (j *DirJournal) Entry(rel string) (JournalEntry, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	e, ok := j.index[filepath.ToSlash(rel)]
	return e, ok
}

// List returns every journaled path, sorted for stable output.
func (j *DirJournal) List() []JournalEntry {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]JournalEntry, 0, len(j.index))
	for _, e := range j.index {
		out = append(out, e)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Path < out[k].Path })
	return out
}

// Clear drops all pre-images: diffs accumulate from this moment on.
func (j *DirJournal) Clear() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := os.RemoveAll(j.dir); err != nil {
		return err
	}
	j.index = map[string]JournalEntry{}
	j.total = 0
	return nil
}

// evictLocked drops least-recently-touched pre-images until the incoming size
// fits the cap. The index entry stays with NoBase set — losing a pre-image
// must not pretend the change never happened.
func (j *DirJournal) evictLocked(incoming int) {
	if j.total+incoming <= journalMaxTotal {
		return
	}
	oldest := make([]JournalEntry, 0, len(j.index))
	for _, e := range j.index {
		if !e.NoBase {
			oldest = append(oldest, e)
		}
	}
	sort.Slice(oldest, func(i, k int) bool { return oldest[i].LastMS < oldest[k].LastMS })
	for _, e := range oldest {
		if j.total+incoming <= journalMaxTotal {
			break
		}
		_ = os.Remove(filepath.Join(j.dir, e.Base))
		j.total -= e.Size
		e.NoBase = true
		e.Size = 0
		j.index[e.Path] = e
	}
}

func (j *DirJournal) writeFileLocked(name string, content []byte) error {
	if err := os.MkdirAll(j.dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(j.dir, name+".tmp")
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(j.dir, name))
}

func (j *DirJournal) persistLocked() {
	entries := make([]JournalEntry, 0, len(j.index))
	for _, e := range j.index {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, k int) bool { return entries[i].Path < entries[k].Path })
	data, err := json.Marshal(entries)
	if err != nil {
		return
	}
	_ = j.writeFileLocked("index.json", data)
}
