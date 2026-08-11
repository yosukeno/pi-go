package tools

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangy/pi-go/worktree"
)

// DefaultSubagentTimeout bounds one delegated task.
//
// Lower than it sounds: a subagent that has not finished in ten minutes is
// usually looping, and the parent is blocked on it. The bound is wall clock rather
// than turns because the turn limit is already passed to the child, and the two
// failure modes are different — a slow model and a stuck agent both need to end.
const DefaultSubagentTimeout = 10 * time.Minute

// DefaultSubagentMaxDepth is how many layers of delegation are allowed below the
// main conversation. One means a subagent cannot spawn its own.
//
// Deliberately shallow. Each layer multiplies the fixed per-turn cost (system
// prompt plus tool schemas) and makes the tree harder to reason about, while the
// cases that need depth are rare enough to ask for explicitly.
const DefaultSubagentMaxDepth = 1

// DefaultSubagentConcurrency is how many children one session runs at once.
//
// The number is fixed by web.CheckApprovalBudget rather than chosen: with a five
// minute gate timeout and a thirty minute run, two is what fits alongside the
// parent's own approval budget. Raising it means lowering one of those.
const DefaultSubagentConcurrency = 2

// Environment variables that describe a child's situation to itself. They are
// environment rather than flags because they must not be forgeable by the model:
// a model can propose flags through the tool it is calling, but it never writes
// the environment of a process the parent spawns.
const (
	EnvSubagentDepth    = "PI_GO_SUBAGENT_DEPTH"
	EnvMainCheckout     = "PI_GO_MAIN_CHECKOUT"
	EnvSubagentReadOnly = "PI_GO_SUBAGENT_READONLY"
)

// The two shapes a delegated task can take.
//
// The axis is the sharpest one available — whether the subagent can change
// anything — and everything else follows from it rather than being configured
// separately:
//
//	ModeExplore  no bash, no write, no edit  → nothing to isolate, so no worktree
//	ModeEdit     the full set minus git      → a worktree, and a commit at the end
//
// That bash implies a worktree is the load-bearing half. A child holding bash in
// the parent's own directory would have no isolation at all, whatever its other
// tools are, so there is no useful third mode between these two: "run the tests
// and tell me what fails" is ModeEdit even though it means to change nothing,
// because a test run writes build output, temporary files and caches.
//
// Splitting a tool by role is usually a mistake — the roles blur, and neither the
// model nor the person can say which one a task belongs to. This split survives
// that objection because it is not a role: read-only versus read-write is a
// property of the request that the caller already knows, and it changes what comes
// back (an answer, or an answer plus a commit).
const (
	ModeExplore = "explore"
	ModeEdit    = "edit"
)

// Subagent delegates a task to a separate pi-go process working in its own git
// worktree.
type Subagent struct {
	// Cwd is the parent's working directory, which must be inside a git
	// repository: the worktree is created from it.
	Cwd string
	// Exe is the pi-go binary to spawn. Injected rather than resolved on every
	// call so that tests can point it at a stub instead of requiring `go build`.
	Exe string
	// Model and MaxTurns are passed down. Inheriting them explicitly beats letting
	// the child fall back to its own defaults: a parent running an expensive model
	// silently delegating to the cheap default is the kind of drift nobody notices
	// until the bill.
	Model string
	// MaxTurns bounds the child's own run. Zero leaves it to the child's default,
	// which is the usual case.
	//
	// Deliberately *not* the parent's -max-turns, unlike Model. A turn limit bounds
	// one run, and a subagent is a different run: how many steps one delegated task
	// needs has nothing to do with how much conversation the parent has left. Passing
	// it down means `-max-turns 4`, which reads as "do not let this ramble", silently
	// becomes "and cripple everything you delegate" — observed live, where a child
	// spent its four turns reading files and returned nothing at all. Cost stays
	// bounded by Timeout and by the usage rolled up into the parent's token budget,
	// which are the instruments meant for it.
	MaxTurns int
	// ExploreModel overrides Model for read-only children. Empty means inherit,
	// which is the default and the existing behaviour.
	//
	// Only read-only delegation gets its own model, and the asymmetry is deliberate.
	// Finding where something is implemented is lighter work than implementing it,
	// and it is the case where latency shows: the parent's turn is blocked until the
	// answer arrives. An edit child is doing the same work the parent would have
	// done, so quietly downgrading it would change the result of the task rather
	// than the cost of a lookup.
	ExploreModel string
	// ExplorePool spreads read-only children across providers. Each fork runs on
	// the pool entry with the fewest in-flight children (ties break in pool
	// order), so a burst of parallel subagents lands one-per-provider instead of
	// all hitting the same endpoint's concurrency limit. ExploreModel, when set,
	// still wins over the pool; an empty pool means inherit Model.
	ExplorePool []ExploreTarget

	// exploreMu guards exploreInflight, the per-provider running counts the pool
	// balances on. Charged at fork, released when the child exits.
	exploreMu       sync.Mutex
	exploreInflight map[string]int
	// Timeout bounds one call. Zero means DefaultSubagentTimeout.
	Timeout time.Duration
	// Depth is this process's depth; a child gets Depth+1. MaxDepth zero means
	// DefaultSubagentMaxDepth.
	Depth    int
	MaxDepth int

	// Concurrency bounds how many children this session runs at once. Zero means
	// DefaultSubagentConcurrency.
	//
	// Deliberately not the loop's tool concurrency, which is eight. A subagent is
	// not a tool call: it is a whole context window with its own per-turn fixed cost,
	// its own checkout on disk, and — under an approval gate — its own claim on the
	// reviewer's attention. Eight of those at once breaks the timeout arithmetic in
	// web.CheckApprovalBudget before it breaks anything else.
	Concurrency int

	// sem is the limiter, built once per tool instance. The registry is per session,
	// so that is per session, which is the right scope: two browser tabs on the same
	// session share a budget, two sessions do not.
	semOnce sync.Once
	sem     chan struct{}

	// Review, when non-nil, decides whether a tool call the child wants to make may
	// run. Nil means the child gets no gate, which is the terminal's situation:
	// there is no human there to ask, and -p has to stay scriptable.
	//
	// The child asks about every call rather than filtering locally, and it does not
	// get a copy of the policy. That is the point: the parent applies its own
	// policy, so "a subagent's permissions never exceed its parent's" holds by
	// construction instead of by a snapshot that could go stale or be forged. The
	// read-only tools cost one pipe round-trip and no human attention, because the
	// parent's policy passes them without a prompt.
	Review ReviewFunc
}

// ReviewFunc is the parent's approval decision for a child's tool call.
type ReviewFunc func(ctx context.Context, req Approval) Decision

// ExploreTarget is one provider an explore child may run on. The pool balances
// per Provider, so two entries naming the same provider share one in-flight
// count; Model is what the child is actually launched with.
type ExploreTarget struct {
	Provider string
	Model    string
}

// pickExplore chooses what one explore child runs and charges the provider one
// in-flight slot. The explicit single-model mapping wins over the pool, and no
// pool means the child inherits its parent's model — the behaviour before pools
// existed. A "" provider means no slot was charged and releaseExplore is a no-op.
func (s *Subagent) pickExplore() (model, provider string) {
	if s.ExploreModel != "" {
		return s.ExploreModel, ""
	}
	if len(s.ExplorePool) == 0 {
		return "", ""
	}
	s.exploreMu.Lock()
	defer s.exploreMu.Unlock()
	// Least in flight wins; ties keep pool order, so the file's writing order is
	// the preference order when everything is equally busy.
	best := 0
	for i := 1; i < len(s.ExplorePool); i++ {
		if s.exploreInflight[s.ExplorePool[i].Provider] < s.exploreInflight[s.ExplorePool[best].Provider] {
			best = i
		}
	}
	t := s.ExplorePool[best]
	if s.exploreInflight == nil {
		s.exploreInflight = make(map[string]int, len(s.ExplorePool))
	}
	s.exploreInflight[t.Provider]++
	return t.Model, t.Provider
}

// releaseExplore returns the slot pickExplore charged. It must run when the
// child exits, or a finished subagent would keep scaring forks away from its
// provider forever.
func (s *Subagent) releaseExplore(provider string) {
	if provider == "" {
		return
	}
	s.exploreMu.Lock()
	s.exploreInflight[provider]--
	s.exploreMu.Unlock()
}

// Approval is one child's request to run one tool.
type Approval struct {
	// Subagent is the worktree id of the child asking, so a reviewer can tell which
	// delegated task a command belongs to.
	Subagent string
	CallID   string
	Tool     string
	Args     json.RawMessage
}

// Decision is the answer. A refusal carries a reason, which the child hands to its
// own model as the tool result: a blocked call is information, not a crash.
type Decision struct {
	Allow  bool
	Reason string
}

type subagentArgs struct {
	Task string `json:"task" required:"true" description:"The complete task for the subagent, written as if to someone who cannot see this conversation"`
	// Mode is required rather than defaulting, because the two modes return
	// different things and a wrong guess is expensive in one direction and silent
	// in the other. Defaulting to explore makes a subagent asked to fix something
	// come back with advice and no commit, which reads like success; defaulting to
	// edit builds a checkout and a commit for a question that only needed an
	// answer. Asking costs one enum and removes both.
	Mode string `json:"mode" required:"true" enum:"explore,edit" description:"explore for read-only work (searching, reading, explaining); edit when the subagent must change files or run commands"`
}

func (*Subagent) Name() string { return "subagent" }

// Description is prompt text, not documentation: it is loaded into the model's
// context verbatim, so it has to say when to delegate, what the subagent can and
// cannot see, and what comes back.
//
// The paragraph about writing the task is the one that earns its tokens. A
// subagent inherits nothing from this conversation — not the files already read,
// not the decisions already made — and the most common failure is a one-line task
// that was perfectly clear in context and meaningless out of it.
func (*Subagent) Description() string {
	return "Delegate a self-contained task to a subagent: a separate agent with its own " +
		"context window. Only its final answer comes back here, so use it when a task would " +
		"otherwise fill this conversation with output you will never refer to again. " +
		"Several calls in one message run in parallel.\n\n" +
		"mode \"explore\" is read-only and runs in your directory, so it sees your " +
		"uncommitted files. Use it for questions — where something is implemented, how a " +
		"subsystem fits together, which callers depend on a function. Prefer it whenever " +
		"you only need to know something.\n\n" +
		"mode \"edit\" also gets write, edit and shell commands, in an isolated git " +
		"worktree from HEAD carrying your uncommitted changes to tracked files (files you " +
		"created but never committed are not there). Use it for changing files or running " +
		"things — applying a fix, running a test suite, chasing a failing build. Its edits " +
		"never touch your working directory: they come back as a commit, to inspect with " +
		"`git show <ref>` and apply with `git cherry-pick <commit>`.\n\n" +
		"Write the task as if to a competent colleague who cannot see this conversation. " +
		"The subagent starts from an empty context: it does not know which files you have " +
		"read, what the user asked for, or what you have already ruled out. Include the " +
		"file paths, the exact error text, the constraints, and what a good answer looks " +
		"like. \"Fix the failing test\" will fail; \"TestFoo in pkg/foo/foo_test.go fails " +
		"with <error>; find the cause and fix it, then run go test ./pkg/foo/\" will not.\n\n" +
		"A subagent cannot run git and cannot delegate further."
}

// ExecutionMode is Parallel: the whole point of a worktree is that two subagents
// editing the same file cannot collide, so serialising them would throw away the
// isolation that was just paid for.
func (*Subagent) ExecutionMode() ExecutionMode { return Parallel }

func (*Subagent) InputSchema() map[string]any {
	mode := prop("string", "\"explore\" for read-only work: reading, searching, "+
		"explaining. \"edit\" when the subagent must change files or run commands.")
	mode["enum"] = []string{ModeExplore, ModeEdit}
	return object([]string{"task", "mode"}, map[string]any{
		"task": prop("string", "The complete task for the subagent, written as if to "+
			"someone who cannot see this conversation: include file paths, exact error "+
			"text, constraints, and what a good answer looks like."),
		"mode": mode,
	})
}

func (*Subagent) Schema() map[string]any {
	return GenerateSchema(reflect.TypeOf(subagentArgs{}))
}

// SubagentDetails is the structured record of one delegated run. It is for
// interfaces and the transcript; the model reads Result.Text.
type SubagentDetails struct {
	ID   string `json:"id"`
	Mode string `json:"mode"`
	// Model is what the child actually ran, which is not always the parent's: an
	// explore child may be configured onto a different one. Recorded because "why
	// was that answer worse than I expected" is otherwise unanswerable.
	Model string `json:"model,omitempty"`
	// Ref and Commit are empty when the subagent changed nothing, which is the
	// normal outcome for a task that only had to look something up, and always the
	// case in explore mode.
	Ref    string `json:"ref,omitempty"`
	Commit string `json:"commit,omitempty"`
	// Session is the child's own transcript, so a person can read the work that
	// deliberately did not enter the parent's context.
	Session string `json:"session,omitempty"`
	// Worktree is where it ran, empty in explore mode. Reported for a human, and
	// never given to the model: the parent has no reason to reach into it, and
	// saying so invites it.
	Worktree string `json:"worktree,omitempty"`
	Turns    int    `json:"turns"`
	InputTok int64  `json:"input_tokens"`
	OutTok   int64  `json:"output_tokens"`
	ExitCode int    `json:"exit_code"`
	// Tampered records that the worktree's git identity failed its check. It is
	// separate from a plain error because it means something actively tried to
	// reach the main checkout, which a person should see even if the task
	// "succeeded".
	Tampered bool `json:"tampered,omitempty"`
}

func (s *Subagent) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	return s.ExecuteStreaming(ctx, raw, nil)
}

func (s *Subagent) ExecuteStreaming(ctx context.Context, raw json.RawMessage, onPartial func(Partial)) (Result, error) {
	var a subagentArgs
	if err := unmarshal(raw, &a); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(a.Task) == "" {
		return Result{}, errors.New("task must not be empty")
	}
	switch a.Mode {
	case ModeExplore, ModeEdit:
	case "":
		return Result{}, fmt.Errorf("mode is required: %q to read, search and explain "+
			"without changing anything, %q to let the subagent edit files and run commands",
			ModeExplore, ModeEdit)
	default:
		return Result{}, fmt.Errorf("unknown mode %q: use %q or %q", a.Mode, ModeExplore, ModeEdit)
	}
	maxDepth := s.MaxDepth
	if maxDepth <= 0 {
		maxDepth = DefaultSubagentMaxDepth
	}
	if s.Depth >= maxDepth {
		// An error the model can act on, not a bare refusal: the useful next move
		// is to do the work itself.
		return Result{}, fmt.Errorf("subagents cannot be nested more than %d level(s) deep, "+
			"and this agent is already at depth %d: do this task yourself, or split it "+
			"differently", maxDepth, s.Depth)
	}
	exe := s.Exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return Result{}, fmt.Errorf("cannot locate the pi-go binary to run a subagent: %w", err)
		}
	}

	// Queued before the worktree is created, so waiting for a slot does not leave a
	// checkout on disk doing nothing. A cancelled wait creates nothing at all. Both
	// modes queue: the slot limit is about context windows and reviewer attention,
	// neither of which a read-only child consumes any less of.
	if err := s.acquire(ctx); err != nil {
		return Result{}, err
	}
	defer s.release()

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultSubagentTimeout
	}
	// The clock is deliberately started by each mode, around the child run alone.
	// Setting it here instead would charge worktree creation to the agent's time
	// budget, and that is not a rounding error: checking out a large repository and
	// copying whatever .worktreeinclude names can take seconds, which the child then
	// does not have.
	if a.Mode == ModeExplore {
		return s.explore(ctx, exe, a.Task, timeout, onPartial)
	}
	return s.edit(ctx, exe, a.Task, timeout, onPartial)
}

// explore runs a read-only child in the parent's own directory.
//
// No worktree, and that is the point rather than an optimisation. A child with no
// bash, no write and no edit cannot change anything, so there is nothing for an
// isolated checkout to protect — and running in place fixes something a worktree
// gets wrong for this kind of task. A worktree starts from HEAD plus a diff of
// tracked files, so files the parent has created but not committed are simply not
// there; an agent asked to explain how something works would report on a codebase
// that is missing the newest part of it. Here it reads what the parent reads.
//
// It also means explore works outside a git repository, which the worktree path
// cannot.
func (s *Subagent) explore(ctx context.Context, exe, task string, timeout time.Duration,
	onPartial func(Partial)) (Result, error) {

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	model, provider := s.pickExplore()
	defer s.releaseExplore(provider)
	spec := childSpec{id: newID(), dir: s.Cwd, readOnly: true, model: model}
	run, _ := s.spawn(runCtx, exe, spec, exploreNote()+task, onPartial)
	d := SubagentDetails{
		ID: spec.id, Mode: ModeExplore, Model: spec.modelOr(s.Model), Session: run.session,
		Turns: run.turns, InputTok: run.input, OutTok: run.output, ExitCode: run.exit,
	}
	text, err := run.report(&d, timeout)
	return Result{Text: text, Details: d}, err
}

// edit runs a read-write child in an isolated worktree and commits what it did.
func (s *Subagent) edit(ctx context.Context, exe, task string, timeout time.Duration,
	onPartial func(Partial)) (Result, error) {

	repo, err := worktree.Open(s.Cwd)
	if err != nil {
		// worktree.Open already explains that this is not a repository and what to
		// do about it. No fallback to the shared directory: that is how two agents
		// come to overwrite each other, which is the failure this tool exists to
		// avoid.
		return Result{}, err
	}
	tree, err := repo.Add(newID())
	if err != nil {
		return Result{}, err
	}
	if err := tree.Lock(os.Getpid()); err != nil {
		_ = tree.Remove(true)
		return Result{}, err
	}

	// The wait error is deliberately dropped: everything worth reporting about how
	// the child ended is already in run (exit code, its own run_end error, stderr,
	// whether the context expired), and each of those produces a message the model
	// can act on. "exit status 1" adds nothing to that.
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	spec := childSpec{id: tree.ID, dir: tree.Dir, mainCheckout: tree.Repo.Root}
	run, _ := s.spawn(runCtx, exe, spec, situation(tree)+task, onPartial)

	// The worktree is unlocked before anything else so that a failure below cannot
	// leave it locked forever; -worktrees-prune can then reclaim it.
	_ = tree.Unlock()

	// Committing happens here, in the parent, and only after Verify passes. The
	// child never runs git, so this is the one git invocation in the worktree and
	// it is on the trusted side of the boundary.
	var (
		sha      string
		ok       bool
		tampered bool
	)
	sha, ok, err = tree.Commit(commitMessage(tree.ID, task, run))
	if err != nil {
		tampered = strings.Contains(err.Error(), "rewritten") ||
			strings.Contains(err.Error(), "common dir") ||
			strings.Contains(err.Error(), "working tree to the main")
		run.commitErr = err
	}
	// The worktree is disposable; the commit is the artifact. Once the work is
	// pinned to a ref it lives in the shared object store, so keeping a full
	// checkout per call would grow the disk without holding anything that is not
	// already safe. A read-only run has even less to keep.
	//
	// The exception is a failure we could not account for: if the commit did not
	// happen because the identity check failed or git refused, the directory may
	// hold real work and a person has to be able to look at it. Those are left for
	// -worktrees to show and -worktrees-prune to decide about.
	if run.commitErr == nil {
		// Not forced: a clean worktree comes away without argument, and anything
		// git objects to is by definition something we did not account for.
		_ = tree.Remove(false)
	}

	d := SubagentDetails{
		ID: tree.ID, Mode: ModeEdit, Model: s.Model, Session: run.session, Worktree: tree.Dir,
		Turns: run.turns, InputTok: run.input, OutTok: run.output,
		ExitCode: run.exit, Tampered: tampered,
	}
	if ok {
		d.Ref, d.Commit = tree.Ref(), sha
	}

	text, err := run.report(&d, timeout)
	return Result{Text: text, Details: d}, err
}

// commitMessage describes one delegated change.
//
// The subject was the task's first line alone, and a live run showed why that is
// not enough: the parent delegated two changes to the same function, and because
// both tasks opened with the same framing sentence, `git log --oneline` came back
// with two commits carrying identical, uninformative messages. The parent noticed
// and offered to rewrite them.
//
// pi-go cannot summarise a diff without another model call, so this does not pretend
// to. What it can do is make each commit distinguishable and explicable: the id in
// the subject, and in the body the task as given, what the subagent reported, and
// the transcript that produced it. `git log --oneline` stays scannable, `git show`
// answers "why is this line like this", and the answer reaches the transcript rather
// than stopping at a hash.
func commitMessage(id, task string, run *childRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "subagent %s: %s\n", id, firstLine(task))

	// The task as given, so the commit records what was asked and not only what
	// arrived. Capped because a task can be long and a commit body is not the place
	// for all of it — the transcript below has the rest.
	if t := strings.TrimSpace(task); t != "" {
		fmt.Fprintf(&b, "\nTask as delegated:\n%s\n", indent(clip(t, 1200)))
	}
	if answer := strings.TrimSpace(run.answer.String()); answer != "" {
		fmt.Fprintf(&b, "\nReported by the subagent:\n%s\n", indent(clip(answer, 1200)))
	}
	if run.session != "" {
		fmt.Fprintf(&b, "\nTranscript: %s\n", run.session)
	}
	return b.String()
}

// clip shortens text on a line boundary where it can, so a truncated body does not
// end mid-word.
func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndexByte(cut, '\n'); i > max/2 {
		cut = cut[:i]
	}
	return cut + "\n[…truncated]"
}

// indent keeps the body from being mistaken for message structure — a task
// containing its own blank lines would otherwise read as several paragraphs of
// commit message.
func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}

// childRun is what one child process produced.
type childRun struct {
	session   string
	answer    strings.Builder
	turns     int
	input     int64
	output    int64
	exit      int
	runErr    string // the child's own run_end error
	stderr    string
	spawnErr  error
	aborted   bool
	timedOut  bool
	commitErr error
}

// maxSituationItems caps each list in the preamble. A checkout can have hundreds
// of untracked files, and a preamble longer than the task stops being context and
// starts being noise.
const maxSituationItems = 8

// exploreNote is the preamble prepended to a read-only child's task.
//
// Written after watching one fail. A live run gave an explore child a question it
// had effectively answered by its second turn; it then decided to confirm by running
// the tests — announcing that this was read-only, which it is not — and spent its
// three remaining turns hunting for a way to do it: grep for go.mod, list the
// directory, read go.mod. It hit its turn limit having thrown the answer away.
//
// The cause is that nothing told it what it was. It has no shell in its tool list,
// but an absent tool is a silence, and a model reads silence as "look harder". So
// the note says the capability is absent rather than merely unlisted, and — the part
// that actually changes behaviour — says what to do instead of searching for it.
//
// Constant, unlike the edit preamble: there is no per-run state here, because
// running in the parent's own directory is exactly the case where nothing is
// missing.
func exploreNote() string {
	return "<your-role>\nYou are a read-only subagent. You can read, list, find and " +
		"grep, in the directory the parent agent is working in — so you see its files as " +
		"they are now, including uncommitted ones.\n" +
		"You have no shell and cannot edit anything: running tests, builds or any command " +
		"is not available to you, and there is no way around it worth looking for. If the " +
		"task cannot be finished without one, say exactly what you would have run and why, " +
		"and stop — the parent can run it. Answer from the source you can read.\n" +
		"</your-role>\n\n"
}

// situation is the preamble prepended to an edit child's task: what its checkout
// is and, more usefully, what is not in it.
//
// A fresh worktree is not the directory the parent is working in, and the
// differences are exactly the ones that break the first command an agent runs.
// There is no node_modules, no virtualenv, no build output, because all of that is
// gitignored and a checkout does not carry it. Uncommitted new files are absent
// too, since the worktree starts at HEAD. Left unsaid, each of these produces an
// error that points somewhere else entirely — a missing-module failure reads like a
// broken dependency, not like a directory that was never populated — and the agent
// spends its turns debugging the environment instead of doing the task.
//
// Everything here is stated as a fact about the surroundings, not as an
// instruction. What to do about a missing dependency depends on the task: install
// it, work around it, or stop and say it is needed. The agent is better placed to
// decide that than this function is, and it can only decide if it knows.
//
// The role half is always present; the "what is missing" half only appears when
// something is. Those are different kinds of statement: what a subagent is does not
// vary between runs, while a warning that fires every time stops being read by the
// time it matters.
func situation(tree *worktree.Tree) string {
	var notes []string
	if !tree.DirtyApplied {
		// Required to be said rather than swallowed: the agent would otherwise be
		// reading a version of the code the user is not looking at, and would report
		// on work that appears not to exist.
		why := ""
		if tree.DirtyErr != nil {
			why = ": " + firstLine(tree.DirtyErr.Error())
		}
		notes = append(notes, "The parent has uncommitted changes to tracked files that "+
			"could not be carried into this checkout"+why+", so you are looking at HEAD. "+
			"Say so in your answer if it affects what you find.")
	}
	if len(tree.MissingIgnored) > 0 {
		notes = append(notes, "These are gitignored in the project, so they exist where the "+
			"parent is working but not here: "+list(tree.MissingIgnored)+". Dependencies and "+
			"build output are the usual reason a command fails here for no apparent reason. "+
			"Install what you need, or stop and report that you need it.")
	}
	if len(tree.Untracked) > 0 {
		notes = append(notes, "The parent also has these files, which are new and not "+
			"committed, so they are not in this checkout: "+list(tree.Untracked)+".")
	}
	// Fenced, because the task below is the parent's words and this is not. A model
	// that cannot tell them apart will treat our description of the filesystem as
	// part of what it was asked to do.
	var b strings.Builder
	b.WriteString("<your-checkout>\nYou are working in an isolated git worktree checked " +
		"out from HEAD, not in the directory the parent agent is working in.\n")
	b.WriteString(editRole)
	for _, n := range notes {
		b.WriteString(n)
		b.WriteByte('\n')
	}
	b.WriteString("</your-checkout>\n\n")
	return b.String()
}

// editRole is what an edit child needs to know about itself, and it is stated
// unconditionally for the same reason exploreNote is.
//
// Observed live: an edit child finished its work, tried to read back the commit hash,
// found it had no git, and reported to the parent that it could not obtain one — a
// non-problem the parent then had to explain away. The information exists, but only
// in two places the child cannot see: the tool description the *parent* reads, and
// the guard's refusal message, which arrives only after the child has already spent
// a turn trying. Same shape as the explore case: the child was learning the rules by
// walking into them.
const editRole = "You have no git here: do not try to commit, diff, or read a hash. " +
	"Leave your changes in the working tree and describe them in your answer — the parent " +
	"commits them for you once you finish, and it is the one that decides whether to apply " +
	"them. To see what you have changed, read the files.\n"

// list renders a capped, comma-separated list.
func list(items []string) string {
	if len(items) <= maxSituationItems {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more",
		strings.Join(items[:maxSituationItems], ", "), len(items)-maxSituationItems)
}

// childSpec is everything that differs between the two modes at spawn time. A
// struct rather than more parameters so that adding a mode later cannot quietly
// leave one of these at its zero value.
type childSpec struct {
	// id names the run: the gate origin, the details record, and in edit mode the
	// worktree and its ref.
	id string
	// dir is where the child works — the parent's own directory in explore mode, an
	// isolated worktree in edit mode.
	dir string
	// readOnly withholds bash, write and edit from the child.
	readOnly bool
	// mainCheckout is the repository an edit child's worktree came from, so its
	// bash guard can refuse commands that name it. Empty in explore mode, where
	// there is no bash to guard and no separate checkout to name.
	mainCheckout string
	// model overrides the inherited model for this child. Empty means inherit.
	model string
}

// modelOr resolves which model this child actually runs.
func (c childSpec) modelOr(inherited string) string {
	if c.model != "" {
		return c.model
	}
	return inherited
}

// spawn runs the child and reads its event stream.
//
// The child speaks the same JSONL contract as `pi-go -mode json`, which is the
// whole reason Phase 0 came first: there is no private parent-child format to
// maintain, and anything a script could read from a subagent, the parent reads
// the same way.
func (s *Subagent) spawn(ctx context.Context, exe string, spec childSpec,
	task string, onPartial func(Partial)) (*childRun, error) {

	args := []string{"-mode", "json", "-C", spec.dir, "-p", task}
	if m := spec.modelOr(s.Model); m != "" {
		args = append(args, "-model", m)
	}
	if s.MaxTurns > 0 {
		args = append(args, "-max-turns", strconv.Itoa(s.MaxTurns))
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = spec.dir
	// Passed as environment, not flags: see the note on EnvSubagentDepth. The
	// read-only marker belongs in the same class — a child that could talk its way
	// out of being read-only would make the mode decorative.
	cmd.Env = append(os.Environ(),
		EnvSubagentDepth+"="+strconv.Itoa(s.Depth+1),
		EnvMainCheckout+"="+spec.mainCheckout,
	)
	if spec.readOnly {
		cmd.Env = append(cmd.Env, EnvSubagentReadOnly+"=1")
	}
	closeGate, err := s.wireGate(cmd, spec.id)
	if err != nil {
		return &childRun{spawnErr: err}, err
	}
	defer closeGate()
	// Same process-group treatment as bash: a child that spawns `go test` must not
	// leave it running after the parent gives up on the turn.
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return &childRun{spawnErr: err}, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	run := &childRun{}
	if err := cmd.Start(); err != nil {
		run.spawnErr = err
		return run, err
	}
	run.read(stdout, onPartial)
	waitErr := cmd.Wait()
	run.stderr = strings.TrimSpace(stderr.String())
	run.exit = cmd.ProcessState.ExitCode()
	run.aborted = ctx.Err() != nil
	run.timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	return run, waitErr
}

// read consumes the child's JSONL stream.
//
// Unparseable lines are ignored rather than fatal. The stream is a contract, but a
// child that dies mid-write leaves a half line, and losing a turn because of the
// last 30 bytes of a crash would be its own bug.
func (r *childRun) read(stdout io.Reader, onPartial func(Partial)) {
	sc := bufio.NewScanner(stdout)
	// Tool output can be long; the default 64KB line limit is not enough.
	sc.Buffer(make([]byte, 0, 64*1024), MaxBytes+64*1024)
	for sc.Scan() {
		var e struct {
			Type    string `json:"type"`
			Session string `json:"session"`
			Turn    int    `json:"turn"`
			Name    string `json:"name"`
			Text    string `json:"text"`
			IsError bool   `json:"is_error"`
			Error   string `json:"error"`
			Usage   *struct {
				Input  int64 `json:"input"`
				Output int64 `json:"output"`
			} `json:"usage"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		// Forwarded whole, before being interpreted. The child speaks the same event
		// contract the parent's own consumers speak, so an interface that can render a
		// run can render a delegated one with the same code — and the alternative,
		// flattening it to a line of text, throws away everything but the tool name.
		//
		// Copied because the scanner reuses its buffer, and the frame outlives this
		// iteration.
		if frameWorthForwarding(e.Type) {
			emitFrame(onPartial, append(json.RawMessage(nil), sc.Bytes()...))
		}
		switch e.Type {
		case "session":
			r.session = e.Session
		case "turn_start":
			r.turns = e.Turn
		case "token":
			// The answer is assembled from the deltas, which is also what the
			// parent shows live: the point of a subagent is that only this part
			// comes back.
			r.answer.WriteString(e.Text)
		case "tool_start":
			// One line per tool call, not the arguments: the parent is watching
			// progress, and the details are in the child's own transcript.
			emit(onPartial, "· "+e.Name+"\n")
		case "run_end":
			r.runErr = e.Error
			if e.Usage != nil {
				r.input, r.output = e.Usage.Input, e.Usage.Output
			}
		}
	}
}

func emit(onPartial func(Partial), s string) {
	if onPartial != nil {
		onPartial(Partial{Text: s})
	}
}

func emitFrame(onPartial func(Partial), frame json.RawMessage) {
	if onPartial != nil {
		onPartial(Partial{Frame: frame})
	}
}

// frameWorthForwarding filters the child's stream down to what an interface shows.
//
// An allowlist, not a denylist, and measurement is why. The first version excluded
// only the per-token deltas; a live run then spent 40% of its frames on `thinking` —
// another delta stream, arriving twenty times across four turns to say something no
// interface displays. A denylist has to be revisited every time the event contract
// grows, and the failure mode when it is not is a silent flood.
//
// What is left is the shape of a delegated run: where its transcript is, its turn
// boundaries, its tool calls and how each went, and how it ended. The answer is not
// here because it arrives whole with the result.
func frameWorthForwarding(kind string) bool {
	switch kind {
	case "session", "turn_start", "tool_start", "tool_end", "run_end":
		return true
	}
	return false
}

// report turns a finished run into what the model reads.
//
// Failures are described in terms of what to do next, because the model is the
// one who has to react: a bare "exit status 1" gives it nothing to work with,
// while "the subagent produced no answer, its stderr says X" lets it retry with a
// better task or do the work itself.
func (r *childRun) report(d *SubagentDetails, timeout time.Duration) (string, error) {
	answer := strings.TrimSpace(r.answer.String())
	if tr := TruncateTail(answer); tr.Truncated {
		answer = tr.Content + fmt.Sprintf(
			"\n\n[the subagent's answer was truncated to the last %d lines; its full "+
				"transcript is at %s]", tr.OutputLines, d.Session)
	}

	switch {
	case d.Tampered:
		return "", fmt.Errorf("the subagent's worktree no longer passes its git identity "+
			"check, so its work was not committed and nothing was merged: %v. Treat its "+
			"answer as unverified and do not re-run the same task", r.commitErr)
	case r.spawnErr != nil:
		return "", fmt.Errorf("could not start the subagent: %w", r.spawnErr)
	case r.timedOut:
		return "", fmt.Errorf("the subagent did not finish within %s and was stopped. "+
			"Partial answer: %s%s", timeout, quoteOrNone(answer), partial(d))
	case r.aborted:
		return "", fmt.Errorf("the subagent was aborted%s", partial(d))
	case r.runErr != "":
		return "", fmt.Errorf("the subagent stopped with an error: %s. Partial answer: %s%s",
			r.runErr, quoteOrNone(answer), partial(d))
	case r.exit != 0:
		return "", fmt.Errorf("the subagent exited with code %d: %s. Partial answer: %s%s",
			r.exit, firstLine(r.stderr), quoteOrNone(answer), partial(d))
	case r.commitErr != nil:
		return "", fmt.Errorf("the subagent finished but its work could not be recorded: %w", r.commitErr)
	case answer == "":
		// stderr is included because this is the case where it is the only clue: the
		// child ran, said nothing, and exited zero. Without it the message says
		// "nothing happened" and stops there.
		why := ""
		if r.stderr != "" {
			why = " It wrote this to stderr: " + firstLine(r.stderr) + "."
		}
		return "", fmt.Errorf("the subagent finished without producing an answer after %d turn(s).%s "+
			"Its transcript is at %s. Re-issue the task with the context it was missing",
			d.Turns, why, d.Session)
	}

	// Success. The commit is named in terms the model can act on rather than as a
	// bare identifier, and the worktree path is deliberately absent.
	var b strings.Builder
	b.WriteString(answer)
	switch {
	case d.Commit != "":
		fmt.Fprintf(&b, "\n\n[the subagent changed files; its commit is %s, reachable as %s. "+
			"Inspect it with `git show %s`, apply it with `git cherry-pick %s`]",
			shortSHA(d.Commit), d.Ref, d.Ref, shortSHA(d.Commit))
	case d.Mode == ModeEdit:
		// Worth saying in edit mode, where the model asked for changes and got
		// none: that is a result, not an absence of one. Saying it after an explore
		// run would only be noise, since nothing could have changed.
		b.WriteString("\n\n[the subagent changed no files]")
	}
	return b.String(), nil
}

// partial names the commit a failed run still produced, or nothing.
//
// A subagent that crashed or ran out of time may have done real work first, and
// that work is committed rather than thrown away. Saying so is the difference
// between a recoverable failure and a commit nobody knows exists.
func partial(d *SubagentDetails) string {
	if d.Ref == "" {
		return ""
	}
	return fmt.Sprintf(". The work it had already done was kept as %s; inspect it with "+
		"`git show %s` before deciding whether to retry", d.Ref, d.Ref)
}

func quoteOrNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return strconv.Quote(s)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	if s == "" {
		return "(no output)"
	}
	return s
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// acquire waits for a subagent slot.
func (s *Subagent) acquire(ctx context.Context) error {
	s.semOnce.Do(func() {
		n := s.Concurrency
		if n <= 0 {
			n = DefaultSubagentConcurrency
		}
		s.sem = make(chan struct{}, n)
	})
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("gave up waiting for a subagent slot: %w", ctx.Err())
	}
}

func (s *Subagent) release() { <-s.sem }

// newID names a worktree, its ref, and its lock. Random rather than sequential
// because two parents in the same repository must not pick the same name, and
// there is no shared counter to ask.
func newID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "sub" + hex.EncodeToString(b[:])
}
