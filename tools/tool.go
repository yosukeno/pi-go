// Package tools holds the four built-in tools. Each one is small, synchronous,
// and reports failures as text rather than aborting the agent loop.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Tool is the contract the agent loop depends on.
//
// A non-nil error is not fatal: the loop turns it into an is_error tool result
// and lets the model correct itself. This is the single most important behaviour
// inherited from pi.
//
// A tool may populate Result.Details even when it returns an error — a failed
// bash command still has an exit code and output worth showing.
type Tool interface {
	Name() string
	Description() string
	InputSchema() map[string]any
	// ExecutionMode reports whether this tool may run alongside its siblings in
	// the same batch. It is part of the interface rather than an optional method
	// so that adding a tool forces the question to be answered.
	ExecutionMode() ExecutionMode
	Execute(ctx context.Context, args json.RawMessage) (Result, error)
}

// SchemaProvider is an optional interface that tools can implement to provide
// their schema through reflection rather than hand-written map literals. It exists
// to remove the class of bug described on object below, where a tool with no
// mandatory arguments marshalled required as JSON null and took every request on
// one provider down with it.
type SchemaProvider interface {
	Schema() map[string]any
}

// ExecutionMode controls whether sibling tool calls in one assistant message may
// overlap.
type ExecutionMode int

const (
	// Parallel is safe for tools whose effects do not interfere.
	Parallel ExecutionMode = iota
	// Sequential means this call runs alone: nothing else in its batch overlaps
	// it, and it acts as a barrier between the calls before and after it.
	//
	// It no longer serializes the whole batch, which is what pi does and what this
	// did until the batch was split into segments — see agent.segments. Parallel
	// siblings still overlap each other; they just never overlap this.
	Sequential
)

// Registry looks tools up by name.
type Registry struct {
	byName map[string]Tool
	order  []Tool
}

func NewRegistry(ts ...Tool) *Registry {
	r := &Registry{byName: make(map[string]Tool, len(ts))}
	for _, t := range ts {
		r.byName[t.Name()] = t
		r.order = append(r.order, t)
	}
	return r
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.byName[name]
	return t, ok
}

func (r *Registry) All() []Tool { return r.order }

// Options describes one session's tool set.
//
// A struct rather than more positional parameters because the three call sites now
// want three different sets: the terminal, the web server, and a subagent child
// that must not have the same tools its parent does.
type Options struct {
	Cwd string
	// ReadRoots are additional directories the read-only tools may reach, for skill
	// bundles kept outside the project. Never granted to write or edit: a skill is
	// instructions, and a model that could rewrite its own instructions has no
	// instructions.
	ReadRoots []string
	// WriteRoots are additional directories the file tools may reach *and modify*,
	// for the agent's memory notes.
	//
	// The same mechanism as ReadRoots with the opposite grant, and the two are kept
	// as separate fields rather than one field plus a flag so that the asymmetry is
	// impossible to get wrong by accident: New hands ReadRoots to four tools and
	// WriteRoots to six, and no caller can widen a skill bundle into a writable one
	// by passing a different boolean.
	//
	// bash gets neither, because bash never had a path restriction to widen. Memory
	// is therefore not protected from bash — nothing here pretends otherwise.
	WriteRoots []string
	// Subagent, when non-nil, registers the subagent tool. Nil in a child, which is
	// how nesting is bounded structurally in addition to the depth counter.
	Subagent *Subagent
	// Guard, when non-nil, restricts what bash may run. Set in a subagent child;
	// nil everywhere else, so the terminal and web paths are byte-for-byte what
	// they were.
	Guard *Guard
	// Todo registers the task-list tool. Set for a top-level session and withheld
	// from a subagent child, which is what every comparable harness does: OpenCode
	// denies todowrite to a spawned agent by default in the same rule that denies
	// it further delegation, and neither Claude Code's nor Gemini CLI's read-only
	// children are given a task list at all.
	//
	// The reason is that a child's list would have no reader. A task list earns its
	// tokens two ways — showing a person the progress, and surviving a compaction
	// boundary so the agent still knows where it was. A child lives for one run
	// under a ten-minute timeout, so it never reaches a boundary; and the progress
	// is already visible, in more detail, through the frames it forwards to the
	// parent's subagent card. What is left is a second writer for "what is being
	// worked on now", a question that has to have one answer.
	Todo bool
	// ReadOnly withholds write, edit and bash. Set in an explore-mode subagent
	// child, and the reason that mode needs no worktree: a session that cannot
	// reach a mutating tool cannot change anything, whatever directory it runs in.
	//
	// Withheld rather than refused. A tool that is absent costs no schema tokens and
	// cannot be attempted; one that always says no invites the model to try again
	// with different arguments.
	ReadOnly bool
	// Journal, when non-nil, snapshots each file's pre-image the first time a
	// session changes it (the web workspace-changes view's data source). Nil in
	// the terminal, where no one is watching.
	Journal Journal
}

// Default returns the file and shell built-ins rooted at cwd, with no subagent,
// no guard and no task list. Kept as the plain entry point because most callers
// want exactly this: the tools that do something to the workspace, without the
// two that describe a session.
func Default(cwd string, readRoots ...string) *Registry {
	return New(Options{Cwd: cwd, ReadRoots: readRoots})
}

// New builds a registry from an explicit description of the session.
func New(o Options) *Registry {
	// Writable roots are readable too — a note you cannot read is not a note — so the
	// read-only four get the union while write and edit get only the writable half.
	// Canonicalised once here rather than per call, like ReadRoots.
	writeRoots := CanonicalRoots(o.WriteRoots)
	readRoots := append(CanonicalRoots(o.ReadRoots), writeRoots...)
	ts := []Tool{
		&Read{Cwd: o.Cwd, Roots: readRoots},
		&Ls{Cwd: o.Cwd, Roots: readRoots},
		&Find{Cwd: o.Cwd, Roots: readRoots},
		&Grep{Cwd: o.Cwd, Roots: readRoots},
	}
	if !o.ReadOnly {
		ts = append(ts,
			&Write{Cwd: o.Cwd, Roots: writeRoots, Journal: o.Journal},
			&Edit{Cwd: o.Cwd, Roots: writeRoots, Journal: o.Journal},
			&Bash{Cwd: o.Cwd, Guard: o.Guard},
		)
	}
	// The two coordination tools go last, after the ones that touch files. Both are
	// a top-level session's business and neither is a child's; see Options.Todo and
	// the note below.
	if o.Todo {
		ts = append(ts, &Todo{})
	}
	// Enforced here rather than only at the call site: delegating is not itself a
	// write, but a read-only session that could spawn a read-write one would be a
	// way around the promise it was built on, and that promise is what lets explore
	// mode run in the parent's own directory.
	if o.Subagent != nil && !o.ReadOnly {
		ts = append(ts, o.Subagent)
	}
	return NewRegistry(ts...)
}

// Schemas used to render the registry as tool declarations and is deleted rather
// than kept for symmetry: it had no caller anywhere, not even a test, because the
// declarations the model actually receives are built in agent.New straight into
// llm.ToolSchema. It could not have acquired one either — the slice it returned held
// an unexported type, so no package outside this one could name the result.
//
// Left as a note because the shape is worth not rebuilding: rendering belongs where
// the wire type is known, and a second renderer here would be a place for the two to
// drift while both compiled.

// --- shared helpers ---

// object is a tiny helper for the hand-written JSON Schema literals.
//
// A nil required list becomes an empty array rather than JSON null. Go marshals a
// nil slice to null, and at least one provider (moonshot/kimi) rejects the whole
// request with "required must be an array" — so a tool with no mandatory
// arguments would take down every call, not just its own.
func object(required []string, props map[string]any) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

func prop(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}

// orderedProps is a JSON object that marshals its keys in declaration order,
// unlike encoding/json's alphabetical sort of maps. Property order is the one
// hint that decides which argument a model emits first, and a streaming UI
// wants path before content: the path is what names the in-progress preview,
// so it must arrive in the first fragments, not after the whole content has
// streamed. Verified against k3 (2026-08): an alphabetical — content-first —
// schema makes it emit the path last; a path-first schema flips it.
type orderedProps []propPair

type propPair struct {
	name string
	spec any
}

func (o orderedProps) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		name, _ := json.Marshal(p.name) // a plain string key never fails
		spec, err := json.Marshal(p.spec)
		if err != nil {
			return nil, err
		}
		b.Write(name)
		b.WriteByte(':')
		b.Write(spec)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// objectOrdered is object with a preserved property order; see orderedProps.
func objectOrdered(required []string, props orderedProps) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

// resolve turns a possibly relative tool path into an absolute one and refuses
// to escape the working root. Sandboxing here is deliberately coarse: it stops
// accidental writes outside the project, not a determined adversary.
//
// extraRoots widens the check for read-only tools only. It exists so that skill
// bundles, which live outside the project by design, can be read without
// becoming writable: write and edit never pass any, so the widened set is
// readable and nothing more. The roots must already be canonical — see
// CanonicalRoots — because this runs on every tool call.
func resolve(cwd, path string, extraRoots ...string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, abs)
	}
	abs = filepath.Clean(abs)

	real := canonical(abs)
	if within(real, canonical(cwd)) {
		return abs, nil
	}
	for _, root := range extraRoots {
		if within(real, root) {
			return abs, nil
		}
	}
	if len(extraRoots) > 0 {
		return "", fmt.Errorf("path %s is outside the working directory %s and the readable roots %s",
			path, cwd, strings.Join(extraRoots, ", "))
	}
	return "", fmt.Errorf("path %s is outside the working directory %s", path, cwd)
}

// Resolve is resolve exported for callers outside the package — the web file
// browser lists and reads workspace files over HTTP and needs the exact same
// canonicalisation and escape refusal the tools get. Same coarse-sandbox
// caveat: this stops accidents, not adversaries.
func Resolve(cwd, path string, extraRoots ...string) (string, error) {
	return resolve(cwd, path, extraRoots...)
}

// CanonicalRoots resolves extra read-only roots once, at construction, so that
// the per-call path check stays free of filesystem work. Empty entries and paths
// that do not resolve are dropped: a root that is not there cannot grant
// anything, and keeping it would only produce confusing error messages.
func CanonicalRoots(paths []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		root := canonical(abs)
		if _, err := os.Stat(root); err != nil || seen[root] {
			continue
		}
		seen[root] = true
		out = append(out, root)
	}
	return out
}

// canonical resolves symlinks as far down the path as it exists, keeping the
// non-existent tail intact.
//
// Resolving symlinks matters because otherwise a link inside the root pointing
// outside it would slip through. Tolerating a non-existent tail matters because
// write and edit legitimately target files — and, via MkdirAll, whole
// directories — that do not exist yet.
func canonical(path string) string {
	remainder := ""
	for cur := path; ; {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, remainder)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return path // reached the filesystem root with nothing resolvable
		}
		remainder = filepath.Join(filepath.Base(cur), remainder)
		cur = parent
	}
}

// within reports whether path is root or sits inside it. Both are expected to have
// been through canonical already.
//
// The byte comparison is the entire answer on a case-sensitive filesystem, and it
// stays the fast path everywhere: no syscall, which is what keeps a per-call check
// cheap enough to sit in front of every read and write.
//
// The fallback exists because macOS and Windows default to case-insensitive
// volumes, where /Users/x and /users/x are one directory with two spellings.
// canonical does not normalise case — EvalSymlinks resolves links and hands back
// the components as spelled — so a model that lowercases a path gets a refusal for
// a directory it is plainly inside. Fail-closed, so nothing ever escaped; write
// simply stopped working, with an error naming two paths that look the same.
//
// Folding the comparison outright would be the wrong repair. On a case-sensitive
// filesystem /tmp/Foo and /tmp/foo are different directories, and treating one as
// the other would put a real hole in the only check that keeps the tools inside the
// workspace. So instead of guessing the volume's collation, the fallback asks the
// filesystem: same device and inode means one directory whatever the spelling, and
// on a case-sensitive volume two spellings cannot be the same file. Two stats, paid
// only on a path that was about to be refused anyway.
//
// Only the root-length prefix is stat'ed, never the full path: write and edit
// legitimately target files that do not exist yet, and the question here is about
// the ancestor, not the leaf.
func within(path, root string) bool {
	if path == root || strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return true
	}
	if !foldWithin(path, root) {
		return false
	}
	return sameDir(path[:len(root)], root)
}

// foldWithin is the same containment test with case ignored. It decides only
// whether asking the filesystem is worth the syscalls; it never grants anything on
// its own.
func foldWithin(path, root string) bool {
	if len(path) < len(root) {
		return false
	}
	if !strings.EqualFold(path[:len(root)], root) {
		return false
	}
	return len(path) == len(root) || path[len(root)] == os.PathSeparator
}

// sameDir reports whether two spellings name one directory. A path that does not
// resolve is not the same as anything, which keeps the failure direction the same
// as the rest of the guard.
func sameDir(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

func unmarshal(args json.RawMessage, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(args)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		// Retry leniently: models routinely add harmless extra keys, and a hard
		// failure there wastes a turn.
		return json.Unmarshal(args, v)
	}
	return nil
}
