package web

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pi-go/agent"
	"github.com/yosukeno/pi-go/tools"
)

// A subagent's approval must not become a standing grant. The user approved one
// delegated command; turning that into a session rule would let a child widen the
// policy the user's own calls are judged by, which inverts the invariant.
func TestSubagentApprovalNeverBecomesASessionGrant(t *testing.T) {
	hub := NewHub()
	policy := NewPolicy()
	gate := NewWebGate(hub, policy, time.Second)

	// The user approves the child's command and ticks "always allow this command".
	go answerFirstGate(t, gate, Verdict{Allow: true, Remember: "command"})
	d := gate.Review(context.Background(), agent.GateRequest{
		CallID: "sub1:c1", ToolName: "bash", Origin: "sub1",
		Args: json.RawMessage(`{"command":"go test ./..."}`),
	})
	if !d.Allow {
		t.Fatalf("the approval did not go through: %+v", d)
	}
	// The parent's own identical command must still be reviewed.
	rule, auto := policy.Decide(agent.GateRequest{
		ToolName: "bash", Args: json.RawMessage(`{"command":"go test ./..."}`),
	})
	if auto {
		t.Errorf("the parent's own bash now passes automatically under %q; a subagent's "+
			"approval widened the session policy", rule)
	}
}

// The same click from the parent's own conversation does create a grant: that is the
// feature, and this is the control that shows the difference is Origin and nothing
// else.
func TestOwnApprovalStillBecomesASessionGrant(t *testing.T) {
	hub := NewHub()
	policy := NewPolicy()
	gate := NewWebGate(hub, policy, time.Second)

	go answerFirstGate(t, gate, Verdict{Allow: true, Remember: "command"})
	if d := gate.Review(context.Background(), agent.GateRequest{
		CallID: "c1", ToolName: "bash",
		Args: json.RawMessage(`{"command":"go test ./..."}`),
	}); !d.Allow {
		t.Fatalf("the approval did not go through: %+v", d)
	}
	if _, auto := policy.Decide(agent.GateRequest{
		ToolName: "bash", Args: json.RawMessage(`{"command":"go test ./..."}`),
	}); !auto {
		t.Error("the user's own always-allow did not take effect")
	}
}

// The adapter is the only place tools and agent meet, and it has to carry the
// subagent's identity through: an approval card for a command the user never asked
// for is unreviewable without it.
func TestSubagentReviewCarriesOriginAndReason(t *testing.T) {
	hub := NewHub()
	policy := NewPolicy()
	policy.Set(ModeStrict, 0)
	gate := NewWebGate(hub, policy, time.Second)

	go answerFirstGate(t, gate, Verdict{Allow: false, Reason: "not now"})
	review := subagentReview(gate, hub)
	d := review(context.Background(), tools.Approval{
		Subagent: "subabc", CallID: "c9", Tool: "bash",
		Args: json.RawMessage(`{"command":"rm -rf /"}`),
	})
	if d.Allow {
		t.Error("a refused call came back allowed")
	}
	if d.Reason != "not now" {
		t.Errorf("Reason = %q, want the reviewer's own words to reach the child", d.Reason)
	}
}

// A refusal with no reason still has to say something: the child hands this text to
// its model as the tool result, and an empty result teaches it nothing.
func TestRefusalAlwaysCarriesAReason(t *testing.T) {
	hub := NewHub()
	policy := NewPolicy()
	policy.Set(ModeStrict, 0)
	gate := NewWebGate(hub, policy, time.Second)

	go answerFirstGate(t, gate, Verdict{Allow: false})
	review := subagentReview(gate, hub)
	d := review(context.Background(), tools.Approval{
		Subagent: "subabc", CallID: "c9", Tool: "bash",
		Args: json.RawMessage(`{"command":"ls"}`),
	})
	if d.Allow || d.Reason == "" {
		t.Errorf("Decision = %+v, want a refusal with a reason", d)
	}
}

// The timeouts have to be able to coexist, and the check exists because the failure
// is otherwise silent: runs start dying of their own deadline with every call
// reported as aborted and nothing saying why.
func TestApprovalBudgetArithmetic(t *testing.T) {
	// The defaults: 15m of batch approvals plus 2 x 5m of subagent approvals fits in
	// a 30m run.
	if err := CheckApprovalBudget(30*time.Minute, 5*time.Minute, tools.DefaultSubagentConcurrency); err != nil {
		t.Errorf("the shipped defaults are rejected: %v", err)
	}
	// Eight concurrent subagents is what the loop's tool concurrency would give, and
	// it does not fit. This is why subagents have their own, smaller limit.
	if err := CheckApprovalBudget(30*time.Minute, 5*time.Minute, 8); err == nil {
		t.Error("8 concurrent subagents was accepted; 15m + 8*5m exceeds a 30m run")
	}
	// A gate timeout raised without raising the run timeout is the realistic way to
	// walk into this, so the message has to name the numbers and the fix.
	err := CheckApprovalBudget(30*time.Minute, 10*time.Minute, 2)
	if err == nil {
		t.Fatal("a 10m gate timeout was accepted alongside a 30m run")
	}
	for _, want := range []string{"30m0s", "10m0s", "Lower -gate-timeout"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, missing %q", err, want)
		}
	}
	// An unbounded side has nothing to violate.
	if err := CheckApprovalBudget(0, 5*time.Minute, 2); err != nil {
		t.Errorf("an unbounded run timeout was rejected: %v", err)
	}
}

// answerFirstGate waits for a pending approval to appear and answers it.
func answerFirstGate(t *testing.T, gate *WebGate, v Verdict) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		gate.mu.Lock()
		var id string
		for k := range gate.pending {
			id = k
		}
		gate.mu.Unlock()
		if id != "" {
			if err := gate.Decide(id, v); err != nil {
				t.Errorf("Decide: %v", err)
			}
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Error("no approval appeared to answer")
}
