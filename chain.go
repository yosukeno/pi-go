package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/yosukeno/pi-go/agent"
	"github.com/yosukeno/pi-go/llm"
	"github.com/yosukeno/pi-go/tools"
)

// Chained runs: reset, not compaction.
//
// When a run ends because an allowance ran out (a "continue" disposition), the
// task does not have to die with the context window: the session forks, and a
// fresh run starts with an empty history, the original request, and whatever
// its predecessor wrote down. That is the reset Anthropic's long-running
// harness work points at, and it sidesteps the evidence against automatic
// summarisation (harness-design §9.6): nothing is rewritten, the old transcript
// stays in the file as an abandoned branch, and the state that carries over is
// a file the agent itself maintains — auditable, diffable, deletable.
//
// The handoff file is one Markdown file, .pi-go/handoff.md, maintained by the
// model. Not JSON: the feature-list contract with machine-checked fields is a
// later step (the evaluator), and prescribing the shape before there is a
// consumer that checks it is ceremony. What the harness guarantees is only the
// location and the protocol around it.

// chainSection is the system-prompt addition when chaining is on. It tells the
// model the one thing it cannot discover from the conversation: that its
// context will not carry over, so anything not written down is lost.
const chainSection = `## Chained task

This task may span more than one run. When this run ends on a limit, a fresh
run starts with an empty context, and the only state it has is what you wrote
down. Maintain .pi-go/handoff.md in the working directory as you go: the task
in one line, what is done and how to verify it, what is left — short, and in
that order. It is the only memory the next run has, so write it for a successor
who has seen nothing. Update it whenever you finish a piece, and always when a
turn checkpoint arrives.`

// handoffPrompt opens every run after the first. The original request is
// repeated verbatim rather than left implicit in the notes: the notes are the
// model's own words about the task, and when the two disagree the request is
// the one a person wrote.
func handoffPrompt(original string, run int) string {
	return fmt.Sprintf(`This is run %d of a task that spans multiple runs. The original request was:

<original_request>
%s
</original_request>

Your predecessor's notes are in .pi-go/handoff.md in the working directory. Read it first. If it is missing, your predecessor left no notes — reconstruct the state from the workspace itself (git status, recently changed files) before continuing. If the notes name a way to verify what is done, run that check before any new work: do not build on an unverified state. Then continue from where the notes say the work stopped, and keep .pi-go/handoff.md current as you go.`, run, original)
}

// chainContinues decides whether runN's end should start another run. The
// disposition table carries the judgement: continue means an allowance ran out
// and a fresh run is progress, while stagnation or an overflow would rerun the
// same failure, and a transport error or an abort is nobody's cue to spend
// more. The table, not this call site, is where that distinction lives.
func chainContinues(reason agent.EndReason, runN, maxRuns int) bool {
	return runN < maxRuns && reason.Disposition() == agent.DispositionContinue
}

// --- the evaluator ---
//
// A chain driver otherwise takes the run's own word for everything: "done"
// means the model said so, and "not done" means a cap interrupted it. Both are
// claims, and Anthropic's long-running work names the failure mode precisely:
// an agent grading its own output praises it confidently. The evaluator is the
// other pair of eyes — a fresh agent with a fresh context and a read-only
// registry, which is exactly the explore child's shape: isolation by absent
// tools rather than by inspection, and no bash means its evidence is what the
// files contain, not what a command printed.
//
// It speaks at two decision points: a claimed completion (NEEDS_WORK chains
// another run with the findings), and a chain that ran out of runs on an
// allowance (a verified workspace exits 0 even though no run produced a final
// answer — the report is the part that can be squeezed out, not the work).

// evaluatorMaxTurns caps the evaluator's reading. Verdicts are cheap — a
// handoff and a handful of files — and a runaway reader burns budget for
// nothing, which is the same argument DefaultMaxTurns makes for workers.
const evaluatorMaxTurns = 15

// evaluatorSystemPrompt is the whole briefing. The pi-go preamble would tell
// it to read files before changing them; this one changes nothing, and the
// distrust instruction is the point of the role.
func evaluatorSystemPrompt(cwd string) string {
	return `You are an evaluator deciding whether a task is actually done.

You did not do the work and did not watch it happen. You have read-only tools: you can look at anything under the working directory and change nothing. Judge from what the files contain, never from what the worker's notes claim they contain — the notes are the one thing here to distrust by default. Say PASS only for work you verified yourself.

Working directory: ` + cwd
}

// evaluationTask is the per-verdict prompt: the original request verbatim
// (claims are checked against what was asked, not against what the notes say
// was asked) and the verdict protocol.
func evaluationTask(original string) string {
	return fmt.Sprintf(`The task given to the worker was:

<original_request>
%s
</original_request>

The worker's handoff notes, if any, are at .pi-go/handoff.md in the working directory.

Decide whether the task is actually done. Read the notes if they exist, then verify against the workspace itself. First line of your answer: exactly PASS or NEEDS_WORK. For NEEDS_WORK, one concrete finding per line after it — what is missing or wrong, and where you looked.`, original)
}

// findingsPrompt opens the run that a NEEDS_WORK verdict chains. It carries
// the findings and the pushback clause: an evaluator can be wrong, and the
// right answer to a wrong finding is evidence, not churn.
func findingsPrompt(original string, run int, findings string) string {
	return fmt.Sprintf(`This is run %d of a task that spans multiple runs. The original request was:

<original_request>
%s
</original_request>

The previous run claimed the task was done. An independent evaluator checked the workspace and disagreed:

<findings>
%s
</findings>

Fix what the findings name, then update .pi-go/handoff.md. If a finding is wrong — the work is verifiably there — say so and point at the evidence instead of changing anything.`, run, original, findings)
}

// parseVerdict reads the protocol line. An unreadable verdict is an error, and
// the caller falls back to the run's own result: a failed evaluator must never
// make the outcome worse than having no evaluator.
func parseVerdict(answer string) (pass bool, findings string, err error) {
	line, rest, _ := strings.Cut(strings.TrimSpace(answer), "\n")
	switch strings.TrimSpace(line) {
	case "PASS":
		return true, "", nil
	case "NEEDS_WORK":
		findings = strings.TrimSpace(rest)
		if findings == "" {
			return false, "", fmt.Errorf("NEEDS_WORK without findings")
		}
		return false, findings, nil
	default:
		return false, "", fmt.Errorf("no verdict in the answer's first line")
	}
}

// evaluateRun asks a fresh read-only agent whether the task is done. It shares
// the worker's client (the verdict is a judgement about the workspace, not a
// creative act) but nothing else: no history, no budgets, no store — the
// verdict is not part of the transcript, it is reported on stderr.
func evaluateRun(ctx context.Context, client llm.Client, cwd, original string) (bool, string, error) {
	a := agent.New(agent.Config{
		Client:       client,
		Registry:     tools.New(tools.Options{Cwd: cwd, ReadOnly: true}),
		SystemPrompt: evaluatorSystemPrompt(cwd),
		MaxTurns:     evaluatorMaxTurns,
	})
	var answer string
	for e := range a.Run(ctx, evaluationTask(original)) {
		if e.Kind == agent.EventMessage {
			answer = e.Message.Text()
		}
	}
	return parseVerdict(answer)
}
