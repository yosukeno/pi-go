package main

import (
	"strings"
	"testing"

	"github.com/yosukeno/pi-go/agent"
	"github.com/yosukeno/pi-go/tools"
)

// The chaining decision is the disposition table plus the run budget — and the
// table is where the judgement lives, so this pins the pairings that matter:
// allowances chain, lookalike failures do not.
func TestChainContinues(t *testing.T) {
	cases := []struct {
		reason        agent.EndReason
		runN, maxRuns int
		want          bool
	}{
		{agent.EndTurnLimit, 1, 3, true},
		{agent.EndTokenBudget, 1, 3, true},
		{agent.EndCostBudget, 2, 3, true},
		{agent.EndTimeBudget, 1, 2, true},
		{agent.EndMaxTokens, 1, 3, true},
		{agent.EndCompleted, 1, 3, false},  // done is done
		{agent.EndStagnation, 1, 3, false}, // reruns fail the same way
		{agent.EndContextOverflow, 1, 3, false},
		{agent.EndTransportError, 1, 3, false},
		{agent.EndAborted, 1, 3, false},
		{agent.EndTurnLimit, 3, 3, false}, // run budget spent
		{agent.EndTurnLimit, 1, 1, false}, // the default: single run
		{"", 1, 3, false},                 // no reason recorded: halt, the cautious default
	}
	for _, c := range cases {
		if got := chainContinues(c.reason, c.runN, c.maxRuns); got != c.want {
			t.Errorf("chainContinues(%q, %d, %d) = %v, want %v", c.reason, c.runN, c.maxRuns, got, c.want)
		}
	}
}

// The handoff prompt is the whole continuity contract, so its load-bearing
// parts are pinned: the original request verbatim, which run this is, and the
// two failure softenings (no notes → reconstruct from the workspace; verify
// before building).
func TestHandoffPrompt(t *testing.T) {
	p := handoffPrompt("build me a rocket\nwith fins", 3)
	for _, want := range []string{
		"run 3",
		"<original_request>\nbuild me a rocket\nwith fins\n</original_request>",
		".pi-go/handoff.md",
		"If it is missing",
		"do not build on an unverified state",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("handoff prompt missing %q:\n%s", want, p)
		}
	}
}

// The chain section is what run 1 knows before any limit is near: that its
// context will not carry over, and where to write things down.
func TestChainSectionTellsRunOneWhatItCannotDiscover(t *testing.T) {
	for _, want := range []string{".pi-go/handoff.md", "empty context", "turn checkpoint"} {
		if !strings.Contains(chainSection, want) {
			t.Errorf("chain section missing %q:\n%s", want, chainSection)
		}
	}
}

// The verdict protocol is the whole contract with the evaluator, so its edges
// are pinned: the exact two words, findings required on NEEDS_WORK, and
// anything else is an error that must degrade to the run's own result.
func TestParseVerdict(t *testing.T) {
	cases := []struct {
		answer      string
		wantPass    bool
		wantFinding string
		wantErr     bool
	}{
		{"PASS", true, "", false},
		{"PASS\n", true, "", false},
		{"  PASS  \n", true, "", false},
		{"NEEDS_WORK\nfruits.txt has 3 lines, want 5", false, "fruits.txt has 3 lines, want 5", false},
		{"NEEDS_WORK", false, "", true}, // a verdict without findings is unusable
		{"Looks done to me", false, "", true},
		{"", false, "", true},     // a turn-capped evaluator says nothing
		{"pass", false, "", true}, // case is the protocol; guessing is not parsing
	}
	for _, c := range cases {
		pass, findings, err := parseVerdict(c.answer)
		if pass != c.wantPass || (err != nil) != c.wantErr {
			t.Errorf("parseVerdict(%q) = pass %v, err %v; want pass %v, err %v",
				c.answer, pass, err, c.wantPass, c.wantErr)
		}
		if findings != c.wantFinding {
			t.Errorf("parseVerdict(%q) findings = %q, want %q", c.answer, findings, c.wantFinding)
		}
	}
}

// The evaluator prompt carries what the verdict depends on: the original
// request verbatim, where the notes live, and the protocol line.
func TestEvaluationTask(t *testing.T) {
	p := evaluationTask("build a rocket")
	for _, want := range []string{
		"<original_request>\nbuild a rocket\n</original_request>",
		".pi-go/handoff.md",
		"PASS or NEEDS_WORK",
		"verify against the workspace itself",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("evaluation task missing %q:\n%s", want, p)
		}
	}
}

// The findings prompt carries the disagreement and the pushback clause: a wrong
// finding is answered with evidence, not churn.
func TestFindingsPrompt(t *testing.T) {
	p := findingsPrompt("build a rocket", 2, "no fins found")
	for _, want := range []string{
		"run 2",
		"<original_request>\nbuild a rocket\n</original_request>",
		"<findings>\nno fins found\n</findings>",
		"point at the evidence instead of changing anything",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("findings prompt missing %q:\n%s", want, p)
		}
	}
}

// The evaluator must be unable to change anything: its registry is the explore
// child's read-only four, and nothing else.
func TestEvaluatorGetsOnlyReadTools(t *testing.T) {
	r := tools.New(tools.Options{Cwd: t.TempDir(), ReadOnly: true})
	for _, tool := range r.All() {
		switch tool.Name() {
		case "read", "ls", "find", "grep":
		default:
			t.Errorf("evaluator registry holds %q, want only the read-only four", tool.Name())
		}
	}
}
