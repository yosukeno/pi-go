// Package git reports the workspace's version control state. Read-only: it runs
// query commands and never writes a ref, an index or an object.
//
// This is the half of version control that pi-go was missing. The shadow repo
// (web/checkpoint.go) answers "undo the last few turns"; it deliberately never
// touches the user's own history. Nothing answered the other question — what
// branch is this, is any of it committed, is there version control here at all —
// and the consequence was not hypothetical: this project accumulated a
// 136-file uncommitted backlog in its own base repository, and no surface
// anywhere ever said so.
//
// Every comparable agent surfaces this. Claude Code injects git state into its
// system prompt, Codex's review pane makes the repository the primary object and
// offers to create one when a project has none. The two lessons taken from their
// issue trackers are in this file: report counts rather than content (their
// prompt injection grew to five figures of tokens on large repositories and
// could not be turned off), and never guess which branch is the trunk.
package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// probeTimeout bounds a status call. `git status` walks the work tree, so a
// pathological one (a network mount, a fuse filesystem) can hang; the caller is
// building a prompt or answering an HTTP request and neither may wait.
const probeTimeout = 5 * time.Second

// subjectLimit truncates the commit subject.
const subjectLimit = 72

// maxDirtyPaths bounds the one place this package names files instead of counting
// them. The rule it serves cannot be expressed in numbers — "do not stage what
// you did not change" needs to say which — but an unbounded list would be the
// mistake described on Status: a repository with a thousand dirty files must not
// make the prompt a thousand lines long. Past twenty the list has stopped being
// a list a model can act on precisely anyway, and the total is still reported.
const maxDirtyPaths = 20

// Status is a snapshot of the workspace's version control state.
//
// Counts, not paths. A list of changed files would be unbounded — and that is
// exactly how the same feature in Claude Code came to spend over ten thousand
// tokens per turn on a large repository (anthropics/claude-code#8245). The
// numbers answer the question a person or a model actually has here ("is this
// committed?"), and the file panel already lists the files themselves.
type Status struct {
	// Repo is false for a directory under no version control at all. That is a
	// fact worth stating rather than an error worth hiding: it means a rewind is
	// the only way back.
	Repo bool `json:"repo"`
	// Root is the repository's top level. Reported because it is not always the
	// workspace: a session started one directory too high silently reports the
	// state of a parent repository, which is a real and confusing failure
	// (anthropics/claude-code#5718, #81726).
	Root string `json:"root,omitempty"`

	Branch   string `json:"branch,omitempty"` // empty when detached
	Detached bool   `json:"detached,omitempty"`
	// Unborn is a repository whose branch has no commits yet — `git init` and
	// nothing else. Distinguished from a missing repository because the advice
	// differs: here the history exists and is empty, not absent.
	Unborn bool `json:"unborn,omitempty"`

	Head    string `json:"head,omitempty"`    // short commit id
	Subject string `json:"subject,omitempty"` // its subject, truncated

	Upstream string `json:"upstream,omitempty"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`

	Staged     int `json:"staged"`
	Unstaged   int `json:"unstaged"`
	Untracked  int `json:"untracked"`
	Conflicted int `json:"conflicted"`

	// DirtyPaths names up to maxDirtyPaths of the uncommitted files, and is the
	// deliberate exception to the counts-not-paths rule above.
	//
	// Probed at session start, it answers a question nothing else can: which
	// changes in this work tree are not the agent's. `git add -A` cannot tell,
	// and neither can a person approving it at the gate — the command shows what
	// it is, never what it will sweep in. Aider solves the same problem by
	// committing the human's dirty files first; knowing which they are is the
	// cheaper half of that, and it does not write to anyone's repository.
	DirtyPaths []string `json:"dirty_paths,omitempty"`

	// Unavailable says why there is no answer, when the reason is neither "no
	// repository" nor a real state. Same rule as checkpointing: the failure mode
	// is "unavailable", never "blocking".
	Unavailable string `json:"unavailable,omitempty"`
}

// Dirty reports whether the work tree has anything uncommitted. Untracked files
// count: the backlog this package exists because of was almost entirely
// untracked, and a "clean" that ignored them would have kept saying so.
func (s Status) Dirty() bool {
	return s.Staged+s.Unstaged+s.Untracked+s.Conflicted > 0
}

// Probe collects the state of the repository containing cwd. It never returns an
// error: every failure is a Status the caller can render.
func Probe(cwd string) Status {
	if _, err := exec.LookPath("git"); err != nil {
		return Status{Unavailable: "git is not installed"}
	}
	root, err := run(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		// Telling "not a repository" apart from a broken repository matters: the
		// first is a normal way to work, the second is something to fix.
		if strings.Contains(err.Error(), "not a git repository") {
			return Status{}
		}
		return Status{Unavailable: firstLine(err.Error())}
	}
	s := Status{Repo: true, Root: strings.TrimSpace(root)}

	// One call for branch, upstream divergence and every changed entry.
	// --porcelain=v2 is the machine format with a stability guarantee, which the
	// short format explicitly does not have.
	//
	// Untracked files are left at git's default collapsing (a wholly untracked
	// directory counts once) rather than --untracked-files=all: it is what
	// `git status` itself reports, so the number here matches the number the
	// user sees in their own terminal.
	//
	// core.quotepath=false because this reads paths now: git's default
	// octal-escapes every non-ASCII one, which is how a CJK filename becomes
	// "\346\265\213…" in a prompt. The checkpoint store learned this the same way.
	out, err := run(cwd, "-c", "core.quotepath=false", "status", "--porcelain=v2", "--branch")
	if err != nil {
		s.Unavailable = firstLine(err.Error())
		return s
	}
	parseStatus(&s, out)

	if !s.Unborn {
		// Separate call because porcelain v2 carries the commit id but not its
		// subject. Worth one subprocess: "which commit am I on" is answered by
		// the message far more usefully than by seven hex digits.
		if line, err := run(cwd, "log", "-1", "--format=%h%x09%s"); err == nil {
			short, subject, _ := strings.Cut(strings.TrimSpace(line), "\t")
			s.Head = short
			s.Subject = truncate(subject, subjectLimit)
		}
	}
	return s
}

// parseStatus reads `git status --porcelain=v2 --branch` output.
//
// Header lines are "# branch.<field> <value>". Entry lines start with a kind:
// "1" ordinary change, "2" rename or copy, "u" unmerged, "?" untracked. For 1
// and 2 the second field is a two-letter XY state where X is the index and Y the
// work tree, and "." means unmodified — so a file can be counted in both Staged
// and Unstaged, which is the truth about a partially staged file.
func parseStatus(s *Status, out string) {
	var dirty []string
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		kind, rest, _ := strings.Cut(line, " ")
		switch kind {
		case "#":
			field, value, _ := strings.Cut(rest, " ")
			switch field {
			case "branch.oid":
				s.Unborn = value == "(initial)"
			case "branch.head":
				if value == "(detached)" {
					s.Detached = true
				} else {
					s.Branch = value
				}
			case "branch.upstream":
				s.Upstream = value
			case "branch.ab":
				// "+2 -1"; a missing or malformed pair leaves both at zero
				// rather than inventing a divergence.
				ahead, behind, ok := strings.Cut(value, " ")
				if !ok {
					continue
				}
				s.Ahead, _ = strconv.Atoi(strings.TrimPrefix(ahead, "+"))
				s.Behind, _ = strconv.Atoi(strings.TrimPrefix(behind, "-"))
			}
		case "1", "2":
			xy, _, _ := strings.Cut(rest, " ")
			if len(xy) != 2 {
				continue
			}
			if xy[0] != '.' {
				s.Staged++
			}
			if xy[1] != '.' {
				s.Unstaged++
			}
			// Field counts before the path: 7 for an ordinary change, 8 for a
			// rename or copy (it carries a similarity score as well).
			n := 7
			if kind == "2" {
				n = 8
			}
			dirty = appendPath(dirty, entryPath(rest, n))
		case "u":
			s.Conflicted++
			dirty = appendPath(dirty, entryPath(rest, 9))
		case "?":
			s.Untracked++
			dirty = appendPath(dirty, rest)
		}
	}
	// Sorted for determinism: git's order is stable in practice but not promised,
	// and a prompt that reshuffles between sessions invalidates the cached prefix
	// for no reason.
	sort.Strings(dirty)
	if len(dirty) > maxDirtyPaths {
		dirty = dirty[:maxDirtyPaths]
	}
	s.DirtyPaths = dirty
}

// entryPath returns the path from a porcelain v2 entry line, given how many
// space-separated fields precede it.
//
// The path is the remainder of the line, not the next field: porcelain v2 does
// not quote or escape paths (with core.quotepath off), so a filename containing
// spaces arrives as-is and splitting on every space would truncate it. A rename
// entry puts the original path after a tab, and only the new name is wanted.
func entryPath(rest string, fields int) string {
	for i := 0; i < fields; i++ {
		_, after, ok := strings.Cut(rest, " ")
		if !ok {
			return ""
		}
		rest = after
	}
	path, _, _ := strings.Cut(rest, "\t")
	return path
}

// appendPath collects a path, ignoring the empty result of a malformed line. The
// slice is capped by the caller after sorting, so that the twenty kept are the
// first twenty by name rather than the first twenty git happened to print.
func appendPath(paths []string, p string) []string {
	if p = strings.TrimSpace(p); p == "" {
		return paths
	}
	return append(paths, p)
}

// PromptSection renders the state for the system prompt, or "" when there is
// nothing worth spending tokens on.
//
// The size is bounded by construction — counts plus one truncated subject — so
// there is no token budget to enforce and no large repository that can make this
// expensive. That is the whole reason it reports numbers instead of a diff.
func (s Status) PromptSection() string {
	var b strings.Builder
	b.WriteString("<git>\n")
	switch {
	case s.Unavailable != "":
		// Worth saying: it stops the model proposing `git` for recovery, or
		// reading a failed git call as something it should work around.
		fmt.Fprintf(&b, "unavailable: %s. Do not rely on git commands here.\n", s.Unavailable)
	case !s.Repo:
		b.WriteString("This workspace is not under version control (no git repository).\n" +
			"There is no committed history to compare against or fall back on, so do not\n" +
			"suggest recovering earlier state with git. If the user's work would benefit\n" +
			"from version control, say so once; do not initialise a repository unasked.\n")
	case s.Unborn:
		fmt.Fprintf(&b, "branch: %s (no commits yet)\n", orDefault(s.Branch, "unknown"))
		writeDirt(&b, s)
	default:
		if s.Detached {
			b.WriteString("HEAD: detached\n")
		} else {
			fmt.Fprintf(&b, "branch: %s\n", s.Branch)
		}
		if s.Upstream != "" && (s.Ahead > 0 || s.Behind > 0) {
			fmt.Fprintf(&b, "vs %s: %d ahead, %d behind\n", s.Upstream, s.Ahead, s.Behind)
		}
		if s.Head != "" {
			fmt.Fprintf(&b, "head: %s %s\n", s.Head, s.Subject)
		}
		writeDirt(&b, s)
		writePreexisting(&b, s)
	}
	b.WriteString("</git>")
	return b.String()
}

// writePreexisting names what was already uncommitted when the session started,
// and says what that means.
//
// This is the one rule in this package that needs paths rather than counts. The
// hazard it addresses is specific: `git add -A` sweeps the user's own unfinished
// work into a commit the agent composed, and the approval gate cannot catch it
// because the command it shows — `git add -A` — is not a list of what it will
// stage. Naming them at session start is enough, because by definition nothing
// the agent does later can add to this set.
func writePreexisting(b *strings.Builder, s Status) {
	if len(s.DirtyPaths) == 0 {
		return
	}
	total := s.Staged + s.Unstaged + s.Untracked + s.Conflicted
	b.WriteString("already modified before this session started, so not yours:\n")
	for _, p := range s.DirtyPaths {
		fmt.Fprintf(b, "  %s\n", p)
	}
	if total > len(s.DirtyPaths) {
		fmt.Fprintf(b, "  ... and %d more\n", total-len(s.DirtyPaths))
	}
	b.WriteString("When committing, stage the paths you changed; do not stage these.\n")
}

// writeDirt states the uncommitted totals, and says so plainly when there are
// none — "clean" is information, and its absence would read as "not measured".
func writeDirt(b *strings.Builder, s Status) {
	if !s.Dirty() {
		b.WriteString("uncommitted: none\n")
		return
	}
	parts := make([]string, 0, 4)
	for _, p := range []struct {
		n    int
		name string
	}{
		{s.Staged, "staged"},
		{s.Unstaged, "unstaged"},
		{s.Untracked, "untracked"},
		{s.Conflicted, "conflicted"},
	} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.name))
		}
	}
	fmt.Fprintf(b, "uncommitted: %s\n", strings.Join(parts, ", "))
}

// run executes a git query in cwd.
//
// GIT_OPTIONAL_LOCKS=0 is not an optimisation: `git status` normally refreshes
// the index and takes its lock, and this runs while the user may be running git
// in the same checkout. A status display that can make someone's own commit fail
// is worse than no status display.
func run(cwd string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("git %s: %s", subcommand(args), msg)
		}
		return "", fmt.Errorf("git %s: %w", subcommand(args), err)
	}
	return string(out), nil
}

// subcommand names the command for an error message, skipping the leading
// `-c key=value` pairs — otherwise every failure of the status call would report
// itself as "git -c".
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		return args[i]
	}
	return "git"
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// truncate limits the subject to n runes, not n bytes: cutting a CJK commit
// message mid-rune renders as a replacement character in every consumer.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
