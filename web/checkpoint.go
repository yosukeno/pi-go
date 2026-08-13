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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pi-go/tools"
)

// errNoCheckpoint is Preview/Restore's not-found: the point predates
// checkpointing, or its snapshot never landed.
var errNoCheckpoint = errors.New("no checkpoint for that point")

// errRewindMode is a request that named no valid mode. A sentinel rather than a
// bare fmt.Errorf so the handler can map it to 400 by identity — matching on an
// error's text is how a reworded message becomes a 500.
var errRewindMode = errors.New("unknown rewind mode")

// RewindMode is what a rewind acts on. The two halves — the conversation and the
// work tree — are separately requestable because the two things a person wants to
// undo are not always the same thing.
type RewindMode string

const (
	// RewindChat forks the conversation and leaves the files alone.
	RewindChat RewindMode = "chat"
	// RewindFiles restores the files and leaves the conversation alone.
	RewindFiles RewindMode = "files"
	// RewindBoth does both, restore first, under one lock.
	RewindBoth RewindMode = "both"
)

// errTooLarge declines to snapshot a work tree that would cost more disk than a
// rewind is worth. It is a refusal, not a failure: the run continues without a
// checkpoint, and the next run tries again — so adding a .gitignore fixes it
// without a restart.
var errTooLarge = errors.New("work tree too large to checkpoint")

// The snapshot budget. A checkpoint is taken at every run start and kept until
// pruned, so an unbounded work tree is an unbounded disk cost — and the noise
// list below cannot catch what it has no name for: a vendored toolchain, a
// downloaded dataset, a database file. Measuring is the only honest guard.
//
// The numbers are sized so that a normal source tree never notices and a
// checkout carrying hundreds of megabytes of blobs always does.
const (
	defaultMaxCheckpointBytes = 512 << 20 // 512 MiB of new content
	defaultMaxCheckpointFiles = 20000
)

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

// checkpointSkip names directories whose contents are always reproducible from
// something else in the tree. Written into the shadow repo's info/exclude, so
// the work tree's own .gitignore still applies on top — and a workspace that is
// not a git repository at all gets at least this much.
var checkpointSkip = []string{
	"node_modules", "dist",
	".venv", "venv", "__pycache__", ".mypy_cache", ".pytest_cache", ".tox",
	".next", ".nuxt", ".gradle", ".terraform",
}

// ShadowRepo is the per-workspace checkpoint store: a bare git repository
// whose work tree is the workspace root. One git index means one writer at a
// time, so all mutating operations serialize on mu.
type ShadowRepo struct {
	dir  string // bare repository
	root string // workspace work tree
	mu   sync.Mutex

	// The snapshot budget, per repo so tests can shrink it. Zero means
	// unlimited, which no production path sets.
	maxBytes int64
	maxFiles int
}

// NewShadowRepo opens (creating if needed) the shadow repo at dir for the
// workspace rooted at root. A failure — no git binary, an unwritable dir —
// is returned for the caller to degrade on, not to abort startup over.
func NewShadowRepo(dir, root string) (*ShadowRepo, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, err
	}
	r := &ShadowRepo{
		dir: dir, root: root,
		maxBytes: defaultMaxCheckpointBytes,
		maxFiles: defaultMaxCheckpointFiles,
	}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil {
		return r, nil // already a repository
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if _, err := r.gitPlain("init", "--bare", "-b", "main", dir); err != nil {
		return nil, err
	}
	// The noise list starts from the file index's (indexSkip in files.go):
	// generated directories are never what a rewind means, and they are what
	// would make `git add -A` slow enough to notice at every run start.
	//
	// It is a superset of that one because the two lists answer different
	// questions. The index's job is "would this drown the project's own files
	// in quick open"; this one's is "would snapshotting this cost real disk at
	// every run". So package managers and tool caches are added here.
	//
	// What is deliberately NOT here: build/ and target/. Both are conventional
	// output directories and both are hand-written source in some projects, and
	// the cost of guessing wrong is silent — a rewind that skips a file nobody
	// was told about. Size, not name, is what the budget above guards; a name
	// list only earns its place when the name is never source.
	exclude := ".git\n" + strings.Join(checkpointSkip, "\n") + "\n"
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
	if err := r.affordable(); err != nil {
		return err
	}
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

// affordable decides whether this snapshot fits the budget, before any object
// is written. Called with mu held.
//
// It asks git rather than walking the tree, because git is the only thing that
// knows what `add -A` would actually stage: info/exclude, the work tree's own
// .gitignore, and every nested .gitignore below it. A hand-rolled walk would
// disable checkpointing on a project whose 2GB build directory git was going to
// ignore anyway.
//
// Only untracked paths are measured. Already-tracked content is already paid
// for, and re-measuring it would turn a one-time refusal into a permanent one.
func (r *ShadowRepo) affordable() error {
	if r.maxBytes <= 0 && r.maxFiles <= 0 {
		return nil
	}
	out, err := r.git("-c", "core.quotepath=false", "ls-files", "--others", "--exclude-standard")
	if err != nil {
		// A failure to measure must not become a failure to checkpoint: the
		// budget is a guard against disk cost, not a precondition for safety.
		return nil
	}
	var total int64
	var files int
	// Blame is attributed to the top-level entry, because that is the unit a
	// person can act on — one .gitignore line, one moved directory.
	blame := map[string]int64{}
	for _, rel := range strings.Split(out, "\n") {
		if rel = strings.TrimSpace(rel); rel == "" {
			continue
		}
		info, err := os.Lstat(filepath.Join(r.root, filepath.FromSlash(rel)))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		files++
		total += info.Size()
		top := rel
		if i := strings.IndexByte(rel, '/'); i > 0 {
			top = rel[:i] + "/"
		}
		blame[top] += info.Size()
	}
	overBytes := r.maxBytes > 0 && total > r.maxBytes
	overFiles := r.maxFiles > 0 && files > r.maxFiles
	if !overBytes && !overFiles {
		return nil
	}
	return fmt.Errorf("%w: %s in %d new files — %s; add a .gitignore or move it out of the workspace",
		errTooLarge, humanBytes(total), files, topOffenders(blame, 3))
}

// topOffenders renders the largest few contributors, so the diagnostic names
// what to exclude instead of only reporting a number.
func topOffenders(blame map[string]int64, n int) string {
	type row struct {
		path string
		size int64
	}
	rows := make([]row, 0, len(blame))
	for p, s := range blame {
		rows = append(rows, row{p, s})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].size != rows[j].size {
			return rows[i].size > rows[j].size
		}
		return rows[i].path < rows[j].path
	})
	if len(rows) > n {
		rows = rows[:n]
	}
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf("%s %s", r.path, humanBytes(r.size)))
	}
	return "largest: " + strings.Join(parts, ", ")
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0fMiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fKiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
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

// RestorePaths restores only the named paths from a checkpoint, leaving the rest
// of the work tree alone. An empty list means the whole tree, which is Restore.
//
// This is a different operation from Restore, not a weaker one, and the
// difference is the branch: Restore moves the shadow branch back so later
// checkpoints fork from the restored state, mirroring the transcript's fork. A
// partial restore forks nothing — the work tree ends up in a state no checkpoint
// describes, which is exactly what "put this one file back" means. The next run's
// checkpoint snapshots whatever is there, so nothing downstream needs the branch
// to have moved.
//
// A path absent from the checkpoint was created after it, so restoring it means
// removing it. That asymmetry is the same one Preview reports as status "A".
func (r *ShadowRepo) RestorePaths(name string, rels []string) error {
	if len(rels) == 0 {
		return r.Restore(name)
	}
	commit, err := r.resolve(name)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, raw := range rels {
		// The project's own path guard, rather than a local check: one place
		// decides what "inside the workspace" means, and a rewind is not the
		// place to invent a second answer.
		abs, err := tools.Resolve(r.root, raw)
		if err != nil {
			return fmt.Errorf("restore %s: %w", raw, err)
		}
		rel, err := filepath.Rel(r.root, abs)
		if err != nil {
			return fmt.Errorf("restore %s: %w", raw, err)
		}
		rel = filepath.ToSlash(rel)
		// cat-file -e is the existence question asked of the commit, not of the
		// disk: what matters is whether the checkpoint had this path.
		if _, err := r.git("cat-file", "-e", commit+":"+rel); err != nil {
			if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("restore %s: %w", rel, err)
			}
			continue
		}
		if _, err := r.git("checkout", commit, "--", rel); err != nil {
			return err
		}
	}
	return nil
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

// PruneResult is what a prune did, in the terms a person asked it in: how many
// restore points went away, how many are still there, and whether the disk
// actually came back.
type PruneResult struct {
	Removed   int
	Kept      int
	BeforeKiB int64
	AfterKiB  int64
}

// DefaultCheckpointKeep and DefaultCheckpointMaxAge are the retention policy.
// The count matches what comparable agents keep per session; the age exists
// because a project you have not opened in months should not still be paying for
// the afternoon you spent in it.
const (
	DefaultCheckpointKeep   = 100
	DefaultCheckpointMaxAge = 30 * 24 * time.Hour
)

// Prune drops checkpoint refs beyond the retention policy and reclaims their
// objects. keep <= 0 keeps every count; maxAge <= 0 disables the age rule.
//
// Reclaiming needs the second half to be real work, and this is the part that is
// easy to get wrong: every checkpoint commit is created with the previous one as
// its parent, so the shadow branch chains the entire history together. Deleting
// a ref frees nothing while some newer commit still lists it as an ancestor.
// Prune therefore rewrites the surviving checkpoints as a fresh chain over the
// same trees — same ref names, new commit ids — which is what actually lets gc
// drop the objects only the discarded points referenced.
//
// Rewriting ids is safe because a ref name, not a commit id, is the join key
// with the transcript: Preview and Restore both resolve the name every time.
func (r *ShadowRepo) Prune(keep int, maxAge time.Duration) (PruneResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var res PruneResult
	res.BeforeKiB, _ = r.sizeKiB()

	out, err := r.git("for-each-ref",
		"--format=%(refname)%09%(committerdate:unix)%09%(objectname)", "refs/checkpoints/")
	if err != nil {
		return res, err
	}
	type ref struct {
		name string // full refname
		when int64
		sha  string
		age  int // position in the commit chain, 0 = newest
	}
	var refs []ref
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			continue
		}
		ts, _ := strconv.ParseInt(strings.TrimSpace(f[1]), 10, 64)
		refs = append(refs, ref{name: f[0], when: ts, sha: strings.TrimSpace(f[2])})
	}

	// Ordering cannot come from the commit dates, and this is the trap: git
	// timestamps have one-second resolution, so a handful of quick runs all
	// carry the same date and --sort=-committerdate silently falls back to
	// sorting by ref name. That keeps rec0 and rec1 while discarding rec4 and
	// rec5 — the exact opposite of a retention policy.
	//
	// The commit chain is the real order, since each checkpoint parents the one
	// before it, and --topo-order guarantees a child is listed before its
	// parent. One call gives a total order over every checkpoint, including
	// those on branches a restore abandoned.
	order := map[string]int{}
	if chain, err := r.git("rev-list", "--all", "--topo-order"); err == nil {
		for i, sha := range strings.Fields(chain) {
			order[sha] = i
		}
	}
	for i := range refs {
		pos, ok := order[refs[i].sha]
		if !ok {
			pos = len(order) // unplaceable: treat as oldest rather than newest
		}
		refs[i].age = pos
	}
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].age < refs[j].age })

	var cutoff int64
	if maxAge > 0 {
		cutoff = time.Now().Add(-maxAge).Unix()
	}
	var doomed, survivors []ref
	for i, rf := range refs {
		tooMany := keep > 0 && i >= keep
		// <= rather than <, for the same one-second resolution reason: a point
		// stamped exactly at the cutoff is at the age limit, and "30 days or
		// older" is the policy people mean.
		tooOld := cutoff > 0 && rf.when > 0 && rf.when <= cutoff
		if tooMany || tooOld {
			doomed = append(doomed, rf)
			continue
		}
		survivors = append(survivors, rf)
	}
	res.Removed, res.Kept = len(doomed), len(survivors)
	if len(doomed) == 0 {
		res.AfterKiB = res.BeforeKiB
		return res, nil
	}

	for _, rf := range doomed {
		if _, err := r.git("update-ref", "-d", rf.name); err != nil {
			return res, err
		}
	}

	// Oldest first, so each rewritten commit can point at the one before it —
	// by chain position, not by date, for the reason above.
	sort.SliceStable(survivors, func(i, j int) bool { return survivors[i].age > survivors[j].age })
	parent := ""
	for _, rf := range survivors {
		tree, err := r.git("rev-parse", rf.name+"^{tree}")
		if err != nil {
			return res, err
		}
		name := strings.TrimPrefix(rf.name, "refs/checkpoints/")
		args := []string{"-c", "user.name=pi-go", "-c", "user.email=pi-go@local",
			"commit-tree", strings.TrimSpace(tree), "-m", "checkpoint " + name}
		if parent != "" {
			args = append(args, "-p", parent)
		}
		created, err := r.git(args...)
		if err != nil {
			return res, err
		}
		parent = strings.TrimSpace(created)
		if _, err := r.git("update-ref", rf.name, parent); err != nil {
			return res, err
		}
	}
	// The branch has to follow, or the discarded chain stays reachable through
	// it and the whole rewrite frees nothing. With no survivors the branch is
	// deleted outright: the next run starts a fresh history, and the work tree
	// it snapshots is the same one either way.
	if parent != "" {
		if _, err := r.git("update-ref", "refs/heads/main", parent); err != nil {
			return res, err
		}
	} else if _, err := r.git("update-ref", "-d", "refs/heads/main"); err != nil {
		return res, err
	}

	// Reflogs hold the pre-rewrite ids, so they must go before gc can.
	_, _ = r.git("reflog", "expire", "--expire=now", "--all")
	if _, err := r.git("gc", "--prune=now", "--quiet"); err != nil {
		return res, err
	}
	res.AfterKiB, _ = r.sizeKiB()
	return res, nil
}

// sizeKiB reports the repository's object store size the way git measures it:
// loose objects plus packs, in KiB.
func (r *ShadowRepo) sizeKiB() (int64, error) {
	out, err := r.git("count-objects", "-v")
	if err != nil {
		return 0, err
	}
	var total int64
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok || (k != "size" && k != "size-pack") {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err == nil {
			total += n
		}
	}
	return total, nil
}

// CheckpointDir is where a workspace's shadow repo lives. Exported so the prune
// command can find it without standing up a Manager — nothing else in that path
// (no model, no session, no server) is needed to delete old refs.
func CheckpointDir(sessionDir, cwd string) string {
	return filepath.Join(sessionDir, "checkpoints", journalDirKey(cwd))
}

// PruneCheckpoints runs a prune against a workspace's shadow repo and reports
// what happened. A workspace that was never checkpointed is not an error: it is
// the answer.
func PruneCheckpoints(out io.Writer, sessionDir, cwd string, keep int, maxAge time.Duration) error {
	dir := CheckpointDir(sessionDir, cwd)
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		fmt.Fprintf(out, "no checkpoints for this workspace\n%s\n", dir)
		return nil
	}
	r, err := NewShadowRepo(dir, cwd)
	if err != nil {
		return err
	}
	res, err := r.Prune(keep, maxAge)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "removed %d restore point(s), kept %d\n", res.Removed, res.Kept)
	// Printed even when nothing was freed: "pruned but the disk did not move"
	// is the answer to the next question, and it is a real outcome — the kept
	// points can reference every object the removed ones did.
	fmt.Fprintf(out, "store %s -> %s\n",
		humanBytes(res.BeforeKiB*1024), humanBytes(res.AfterKiB*1024))
	fmt.Fprintf(out, "%s\n", dir)
	return nil
}
