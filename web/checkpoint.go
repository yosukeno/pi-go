package web

// Checkpointing snapshots the workspace into a shadow git repository before
// each run, so a rewind can restore the files the abandoned turns changed.
// This is the design the market converged on — Codex's ghost commits, Gemini
// CLI's ~/.gemini/history, Cline's per-task shadow repo — because a whole-tree
// snapshot catches what a per-file hook cannot: bash writes, deletions,
// renames. The shadow repo lives under the session dir, never inside the
// workspace: the workspace does not need to be a git repository of its own,
// and the user's git history — if they have one — is never touched.
//
// A checkpoint is named by the transcript record that was the head when the
// run started (refs/checkpoints/<recordID>). A rewind's fork point is exactly
// such a record id, which makes it the join key between the two trees: the
// conversation forks in the JSONL, the shadow branch forks here.
//
// What the snapshot cannot tell apart is an agent's edit from a user's edit
// after the checkpoint, so restore previews and asks rather than guessing.
// The failure mode of a checkpoint is "unavailable", never "blocked": a run
// starts whether or not its snapshot landed.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// errNoCheckpoint is Preview/Restore's not-found: the point predates
// checkpointing, or its snapshot never landed.
var errNoCheckpoint = errors.New("no checkpoint for that point")

// FileChange is one path a restore would touch. Status is git's name-status
// letter: "M" restored, "D" brought back, "A" deleted (created afterwards).
// Added/Removed count the lines the restore itself would add and remove —
// the rewind's impact, matching the consequence the Status describes. Both
// are -1 for binary files, where a line count would be a lie.
type FileChange struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// ShadowRepo is the per-workspace checkpoint store: a bare git repository
// whose work tree is the workspace root. One git index means one writer at a
// time, so all mutating operations serialize on mu.
type ShadowRepo struct {
	dir  string // bare repository
	root string // workspace work tree
	mu   sync.Mutex
}

// NewShadowRepo opens (creating if needed) the shadow repo at dir for the
// workspace rooted at root. A failure — no git binary, an unwritable dir —
// is returned for the caller to degrade on, not to abort startup over.
func NewShadowRepo(dir, root string) (*ShadowRepo, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, err
	}
	r := &ShadowRepo{dir: dir, root: root}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil {
		return r, nil // already a repository
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if _, err := r.gitPlain("init", "--bare", "-b", "main", dir); err != nil {
		return nil, err
	}
	// The noise list mirrors the file index's (indexSkip in files.go):
	// generated directories are never what a rewind means, and they are what
	// would make `git add -A` slow enough to notice at every run start.
	exclude := ".git\nnode_modules\ndist\n"
	// A session dir inside the workspace would make the shadow repo snapshot
	// its own database — and every transcript alongside it.
	if rel, err := filepath.Rel(root, dir); err == nil && !strings.HasPrefix(rel, "..") {
		exclude += filepath.ToSlash(rel) + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "info", "exclude"), []byte(exclude), 0o644); err != nil {
		return nil, err
	}
	return r, nil
}

// git runs a command against the shadow repo with the workspace as its work
// tree. The timeout keeps a pathological tree (a fuse mount, a hung fs) from
// parking a run start forever.
func (r *ShadowRepo) git(args ...string) (string, error) {
	full := []string{"--git-dir=" + r.dir, "--work-tree=" + r.root}
	return r.gitPlain(append(full, args...)...)
}

func (r *ShadowRepo) gitPlain(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Checkpoint snapshots the whole work tree and names the commit after a
// transcript record id. It runs at every run start, serialized: the index is
// one writer deep, and concurrent sessions queue rather than corrupt it.
func (r *ShadowRepo) Checkpoint(name string) error {
	if name == "" {
		return errors.New("checkpoint name is empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.git("add", "-A"); err != nil {
		return err
	}
	// --allow-empty keeps the naming rule unconditional: the ref always moves
	// forward with the run, even when the previous run left nothing behind.
	if _, err := r.git("-c", "user.name=pi-go", "-c", "user.email=pi-go@local",
		"commit", "--quiet", "--allow-empty", "-m", "checkpoint "+name); err != nil {
		return err
	}
	_, err := r.git("update-ref", "refs/checkpoints/"+name, "HEAD")
	return err
}

// Preview reports what Restore would do, without touching anything. ok is
// false when the point has no checkpoint — an older session, or a run whose
// snapshot failed to land.
func (r *ShadowRepo) Preview(name string) (changes []FileChange, ok bool) {
	commit, err := r.resolve(name)
	if err != nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// core.quotepath=false: git's default octal-escapes non-ASCII paths in
	// every diff listing, which is what turned CJK filenames into
	// "\346\265\213…" in the rewind dialog.
	statusOut, err := r.git("-c", "core.quotepath=false", "diff", "--name-status", commit, "--")
	if err != nil {
		return nil, false
	}
	// numstat walks the same diff pairs in the same order as name-status, so
	// the two listings zip by index. Its counts describe what happened SINCE
	// the checkpoint; the restore undoes exactly that, so the numbers swap to
	// describe what the restore will add and remove.
	statOut, err := r.git("-c", "core.quotepath=false", "diff", "--numstat", commit, "--")
	if err != nil {
		return nil, false
	}
	statLines := strings.Split(strings.TrimSpace(statOut), "\n")
	for i, line := range strings.Split(strings.TrimSpace(statusOut), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		status, path := f[0], f[len(f)-1]
		// A rename reads as "modified at its new name": the restore rewrites
		// the content and removes the old path, which is what matters here.
		if strings.HasPrefix(status, "R") {
			status = "M"
		}
		fc := FileChange{Path: path, Status: status, Added: -1, Removed: -1}
		if i < len(statLines) {
			added, removed := parseNumstat(statLines[i])
			if added >= 0 {
				fc.Added, fc.Removed = removed, added
			}
		}
		changes = append(changes, fc)
	}
	// Untracked files are what the cleanup half of Restore deletes — "created
	// after the checkpoint". --exclude-standard keeps the noise list out of
	// both the preview and the deletion. numstat never sees them, so their
	// line count is measured directly: everything goes, nothing comes back.
	untracked, err := r.git("-c", "core.quotepath=false", "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, false
	}
	for _, line := range strings.Split(strings.TrimSpace(untracked), "\n") {
		if line == "" {
			continue
		}
		changes = append(changes, FileChange{
			Path:    line,
			Status:  "A",
			Added:   0,
			Removed: countLines(filepath.Join(r.root, filepath.FromSlash(line))),
		})
	}
	return changes, true
}

// parseNumstat reads one "added\tdeleted\tpath" row. Binary files read
// "-\t-", reported as -1/-1 because a line count would be invented.
func parseNumstat(line string) (added, removed int) {
	f := strings.SplitN(line, "\t", 3)
	if len(f) < 2 {
		return -1, -1
	}
	var a, d int
	if _, err := fmt.Sscanf(f[0], "%d", &a); err != nil {
		return -1, -1
	}
	if _, err := fmt.Sscanf(f[1], "%d", &d); err != nil {
		return -1, -1
	}
	return a, d
}

// countLines measures an untracked file for the preview. Binary or very
// large files report -1: the dialog says "binary" rather than show a number
// nobody verified.
func countLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return -1
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return -1
	}
	buf := make([]byte, 32*1024)
	lines, size, last := 0, 0, byte('\n')
	for {
		n, err := f.Read(buf)
		chunk := buf[:n]
		if size == 0 && bytes.IndexByte(chunk, 0) >= 0 {
			return -1 // a NUL in the first chunk: binary
		}
		for _, b := range chunk {
			if b == '\n' {
				lines++
			}
		}
		if n > 0 {
			last = chunk[n-1]
		}
		size += n
		if err != nil {
			break
		}
	}
	// A trailing partial line still counts as a line.
	if size > 0 && last != '\n' {
		lines++
	}
	return lines
}

// Restore resets the work tree to the named checkpoint: modified files go
// back, deleted files return, files created afterwards are removed. The
// shadow branch moves with it, so later checkpoints fork from the restored
// state — matching the transcript's fork.
func (r *ShadowRepo) Restore(name string) error {
	commit, err := r.resolve(name)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.git("reset", "--hard", commit); err != nil {
		return err
	}
	// reset leaves untracked files alone, but the files the abandoned run
	// created are untracked — a rewind that spares them is half a rewind.
	// Ignored paths (the noise list) are spared, which is the point of
	// ignoring them rather than deleting them outright.
	_, err = r.git("clean", "-fd")
	return err
}

// resolve maps a checkpoint name to its commit. Read-only, so it stays
// outside the mutex; callers take the lock for the operation that follows.
func (r *ShadowRepo) resolve(name string) (string, error) {
	out, err := r.git("rev-parse", "--verify", "refs/checkpoints/"+name)
	if err != nil {
		return "", errNoCheckpoint
	}
	return strings.TrimSpace(out), nil
}
