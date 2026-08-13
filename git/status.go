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
	"strconv"
	"strings"
	"time"
)

// probeTimeout bounds a status call. `git status` walks the work tree, so a
// pathological one (a network mount, a fuse filesystem) can hang; the caller is
// building a prompt or answering an HTTP request and neither may wait.
const probeTimeout = 5 * time.Second

// subjectLimit truncates the commit subject. This is the only free-form text in
// the whole structure, so it is the only thing that could make the prompt
// section grow without bound.
const subjectLimit = 72

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
	out, err := run(cwd, "status", "--porcelain=v2", "--branch")
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
		case "u":
			s.Conflicted++
		case "?":
			s.Untracked++
		}
	}
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
	}
	b.WriteString("</git>")
	return b.String()
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
			return "", fmt.Errorf("git %s: %s", args[0], msg)
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return string(out), nil
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
