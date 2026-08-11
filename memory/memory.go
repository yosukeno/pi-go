// Package memory is the agent's own notes, carried across sessions.
//
// # What this is, and what it is not
//
// pi-go already reads AGENTS.md and CLAUDE.md into the system prompt: project
// conventions, written by a person, read-only. This package is the *writable*
// counterpart — the place the agent records what it worked out, for a future session
// that starts with no memory of this one. Anthropic's framing of the problem is the
// clearest one available: a project staffed by engineers working in shifts, where
// each new engineer arrives knowing nothing about the previous shift.
//
// It is deliberately not a new tool. Anthropic's memory tool is view / create /
// str_replace / insert / delete / rename, which is read, write and edit with
// different names — and their own harness-design guidance says memory, skills and
// programmatic tool calling are all compositions of the bash and text-editor
// primitives. So memory here is a *directory the existing tools can reach*, and it
// costs no extra schema tokens. Tool schemas are the largest fixed per-turn cost in
// this project (roughly 5.8x the system prompt), so a tool that duplicates three
// existing ones would be paid for on every single turn of every session.
//
// # It is the skills mechanism, inverted
//
// skills hands out a prompt string and a set of extra roots, and those roots go to
// read and ls only — a skill is instructions the model must not be able to rewrite.
// Memory is the same shape with the opposite grant: the roots go to read, ls, find,
// grep *and* write, edit. One mechanism, used twice, with the read/write split as
// the only difference. See tools.Options.WriteRoots.
//
// bash is not given the roots because bash never had a path restriction to widen.
// That is worth stating rather than leaving implicit: it means memory is not
// protected from bash, and nothing here pretends otherwise.
//
// # Two layers, and why the project one is off by default
//
// The user layer (~/.pi-go/memory) is on. The project layer (<cwd>/.pi-go/memory) is
// off unless asked for, and that is the same decision project skills already made,
// for the same reason: a file that changes how the agent behaves must not arrive with
// a `git clone`. Memory is worse than a skill in one specific way — it is presented
// to the model as *its own earlier conclusions*, which is a more credible voice than
// a document handed to it.
//
// The home directory is already as trusted as the binary (see config.FileName and
// CVE-2026-21852 for where that reasoning comes from), so the user layer needs no
// such flag.
//
// # The injection surface is real and it is measured
//
// Everything in these files is untrusted input. Tool output reaches memory, and tool
// output here is the contents of files in a repository — a README can contain a
// sentence addressed to whatever reads it. What memory changes is the *duration*: a
// prompt injection that lands in a session dies with it, and one that lands in a
// memory file is read again by every session afterwards.
//
// The literature on this is consistent and unflattering. Across 24 agent
// configurations, malicious memory persisted in 84.2% of cases and the full
// write-then-act chain succeeded in 50.3% (arXiv 2607.27080). Three injected records
// were enough for an 85.9% tool-hijacking rate, surviving three published defences
// (arXiv 2605.26154). And the failure does not need an attacker at all: routine
// conversation alone substantially corrupts long-term state over time, memory
// artefacts worst (arXiv 2605.06731).
//
// So three properties, each cheap because the mechanism for it already exists here:
//
//   - Contents are declared to be notes rather than instructions, inside a named
//     element, the same way compact.go declares <transcript> to be data. See
//     PromptSection.
//   - Every write is journalled, because write and edit already journal, so a memory
//     change shows up in the web workspace-diff view like any other file change. A
//     memory you can see change is a memory you can undo; the 84.2% figure above is
//     really a statement about how long a bad write survives unnoticed.
//   - The listing carries each file's age, so a note nobody has touched in months is
//     visible as such rather than reading exactly like one written this morning.
//
// What is deliberately *not* here: a size cap on writes. Capping would mean the write
// tool has to know which paths are memory, and that is the layering skills was careful
// to avoid — the loop and the path guard do not know skills exist. The exposure is
// bounded by mechanisms already in place instead: read truncates at 2000 lines / 50KB,
// and the listing below is capped, so neither a large file nor a large number of them
// can grow the prompt without bound. Stated as a known gap rather than a solved
// problem.
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DirEnv relocates the user-level memory directory, for tests and for anyone
// keeping it elsewhere. Same shape as PIGO_SESSION_DIR and PIGO_WORKTREE_DIR.
const DirEnv = "PIGO_MEMORY_DIR"

// DirName is the per-project subdirectory, under the same .pi-go prefix skills use.
const DirName = "memory"

// Options is what the flags resolved to.
type Options struct {
	// Cwd is the session's working directory, for the project layer.
	Cwd string
	// User enables ~/.pi-go/memory. On by default; -no-memory clears it.
	User bool
	// Project enables <cwd>/.pi-go/memory. Off by default; -project-memory sets it.
	//
	// Off because memory arrives with a checkout and speaks to the model in the most
	// credible voice it has: its own. See the package comment.
	Project bool
}

// Store is one session's memory: the roots the tools may write, and the listing
// that goes in the prompt.
type Store struct {
	// dirs are the enabled roots in precedence order, user first.
	dirs []dir
}

type dir struct {
	path string
	// project marks the layer, so the listing can say which is which. A note in the
	// repository and a note in the home directory carry different weight, and a model
	// deciding whether to trust one should be told which it is reading.
	project bool
	files   []file
}

type file struct {
	rel   string
	size  int64
	mtime time.Time
}

// Load resolves the enabled directories, creating them when absent, and lists what
// is in them.
//
// Creating rather than skipping is deliberate and has a concrete cause:
// tools.CanonicalRoots drops a root that does not exist, on the reasonable grounds
// that a missing directory cannot grant anything. Applied here that would mean the
// agent has nowhere to write on a fresh install and no way to make one, because the
// only place it could create it is the place it is not allowed to reach. The SDK
// reference implementations of Anthropic's memory tool create the root before the
// model's first call for the same reason.
//
// Diagnostics are returned rather than printed, like skills.Load: in -mode json
// nothing may reach stdout.
func Load(o Options) (*Store, []string) {
	var diags []string
	s := &Store{}

	add := func(path string, project bool) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			// Not fatal. A session with no memory is the behaviour that existed before
			// this package, so the honest degradation is to carry on without it and say
			// so — the same rule checkpointing follows: the failure mode is
			// "unavailable", never "blocked".
			diags = append(diags, fmt.Sprintf("memory: %s is unusable, continuing without it: %v", path, err))
			return
		}
		files, err := list(path)
		if err != nil {
			diags = append(diags, fmt.Sprintf("memory: cannot list %s: %v", path, err))
			return
		}
		s.dirs = append(s.dirs, dir{path: path, project: project, files: files})
	}

	// 0o700 on the user directory, not 0o755. Notes accumulate whatever the agent
	// found worth keeping — paths, hostnames, the shape of a private codebase — and
	// there is no reason for another account on the machine to read them.
	if o.User {
		if path, err := UserDir(); err != nil {
			diags = append(diags, fmt.Sprintf("memory: no home directory, skipping user memory: %v", err))
		} else {
			add(path, false)
		}
	}
	if o.Project && o.Cwd != "" {
		add(filepath.Join(o.Cwd, ".pi-go", DirName), true)
	}
	return s, diags
}

// UserDir is the user-level memory directory.
func UserDir() (string, error) {
	if p := strings.TrimSpace(os.Getenv(DirEnv)); p != "" {
		return filepath.Abs(p)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi-go", DirName), nil
}

// ProjectDir is where -project-memory looks.
func ProjectDir(cwd string) string { return filepath.Join(cwd, ".pi-go", DirName) }

// Roots are the directories the file tools may read *and write*.
//
// The same slice goes to tools.Options.WriteRoots, which grants it to write and edit
// as well as the read-only four. That is the one difference from skills.Roots, and it
// is the whole point of the package.
func (s *Store) Roots() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.dirs))
	for _, d := range s.dirs {
		out = append(out, d.path)
	}
	return out
}

// Dirs are the enabled directories, for -memory to report.
func (s *Store) Dirs() []string { return s.Roots() }

// Empty reports whether there is nothing recorded anywhere.
//
// Used to decide whether the prompt gets a section at all: an empty memory produces
// no section, so a user who never uses this feature pays nothing for it — not even
// the two sentences of protocol. Same rule skills follows.
func (s *Store) Empty() bool {
	if s == nil {
		return true
	}
	for _, d := range s.dirs {
		if len(d.files) > 0 {
			return false
		}
	}
	return true
}

// Count is how many files are recorded, for diagnostics.
func (s *Store) Count() int {
	n := 0
	if s == nil {
		return 0
	}
	for _, d := range s.dirs {
		n += len(d.files)
	}
	return n
}

// maxListDepth bounds how deep the listing walks.
//
// Two levels, matching the documented behaviour of Anthropic's memory view command,
// so a memory directory organised for one harness reads the same in the other. Deeper
// nesting is not forbidden on disk — read and ls reach it — it is only absent from
// the prompt listing, which is a summary and not an index.
const maxListDepth = 2

// maxListFiles and maxListBytes bound the listing, which is in *every* prompt.
//
// Two limits rather than one because they fail differently: a hundred tiny notes and
// three notes with enormous paths are both ways to make this section large, and only
// one of them is caught by counting. Past either limit the remainder is reported as a
// count — "and 40 more" is short, honest, and tells the model to ls if it cares.
const (
	maxListFiles = 40
	maxListBytes = 4096
)

// list walks a memory directory, newest first.
//
// Newest first because the listing is truncated, so the order decides what survives
// truncation, and a note written today is more likely to matter than one from six
// months ago. It is also the order that makes the age column worth having.
func list(root string) ([]file, error) {
	var out []file
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// A single unreadable entry must not lose the rest of the listing.
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		depth := len(strings.Split(rel, string(filepath.Separator)))
		if d.IsDir() {
			// Dotfile directories are skipped whole. Nothing writes one here, so its
			// presence means something else put it there.
			if strings.HasPrefix(d.Name(), ".") || depth >= maxListDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		out = append(out, file{rel: filepath.ToSlash(rel), size: info.Size(), mtime: info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].mtime.Equal(out[j].mtime) {
			return out[i].mtime.After(out[j].mtime)
		}
		// Ties broken by name so the listing is stable, which matters more than it
		// looks: the listing is part of the cached prompt prefix, and an unstable
		// order would invalidate the cache on every turn for no reason.
		return out[i].rel < out[j].rel
	})
	return out, nil
}

// humanSize formats a byte count the way Anthropic's memory view does, so the two
// listings are comparable at a glance.
func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// age formats how long ago a note was written.
//
// Coarse on purpose, and rounded down to a unit rather than given as a timestamp.
// Two reasons, and the second is the load-bearing one:
//
//   - "4mo" answers the question a reader has ("is this still true?") without them
//     having to subtract dates.
//   - A timestamp, or an age in minutes, would change on every turn, and this text
//     sits in the cached prompt prefix. A field that changes every turn invalidates
//     the cache every turn — the same trap the context-clearing placeholders avoid by
//     being a pure function of their input, with no clock in them. Days are the
//     finest unit that is stable across a session.
func age(mtime, now time.Time) string {
	d := now.Sub(mtime)
	switch {
	case d < 24*time.Hour:
		return "today"
	case d < 48*time.Hour:
		return "1d"
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
	default:
		return fmt.Sprintf("%dy", int(d.Hours()/24/365))
	}
}
