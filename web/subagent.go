package web

import (
	"context"
	"fmt"
	"time"

	"github.com/yosukeno/pi-go/agent"
	"github.com/yosukeno/pi-go/tools"
)

// subagentReview lets a child's tool call be judged by this session's gate.
//
// It is the only place where the two packages meet, and the shape is dictated by
// the dependency graph: agent imports tools, so tools cannot import agent and the
// subagent tool cannot name a ToolGate. It takes a callback instead, and the
// adapter that knows about both lives here, in the package that already imports
// each of them.
//
// The call arrives with Origin set, which does two things. It labels the approval
// card, because a command the user never asked for is unreviewable without knowing
// which delegated task wants it. And it blocks "always allow" for that card: the
// user approved one subagent's command, not a standing opening in their own
// session. See WebGate.Review.
func subagentReview(gate *WebGate, hub *Hub) tools.ReviewFunc {
	return func(ctx context.Context, req tools.Approval) tools.Decision {
		d := gate.Review(ctx, agent.GateRequest{
			// Namespaced so the id cannot collide with a call id from the parent's
			// own conversation, which shares the timeline this is published to.
			CallID:   req.Subagent + ":" + req.CallID,
			ToolName: req.Tool,
			Args:     req.Args,
			Origin:   req.Subagent,
		})
		reason := d.Reason
		if !d.Allow && reason == "" {
			reason = "the parent agent refused this call"
		}
		return tools.Decision{Allow: d.Allow, Reason: reason}
	}
}

// CheckApprovalBudget reports whether a configuration's timeouts can all hold.
//
// The arithmetic is the part that is easy to get wrong. Approvals for the parent's
// own batch are bounded by ReviewBudget, but a subagent's are not: the child is
// spawned during execution, after the review phase is over, so its questions arrive
// outside any budget the loop knows about. Each can wait a full gate timeout, and N
// children can hold N of them between them.
//
// Stated as an inequality and checked at startup, because the failure mode
// otherwise is silent: raise -gate-timeout far enough and runs start dying of their
// own deadline with every call reported as aborted and nothing explaining why.
func CheckApprovalBudget(runTimeout, gateTimeout time.Duration, concurrency int) error {
	if runTimeout <= 0 || gateTimeout <= 0 {
		return nil // unbounded on one side; there is nothing to violate
	}
	reviewBudget := runTimeout / 2 // as set in newSession
	if reviewBudget+time.Duration(concurrency)*gateTimeout <= runTimeout {
		return nil
	}
	return fmt.Errorf("these timeouts cannot all hold at once: approvals for one batch may take "+
		"up to %s (half of the %s run timeout) and %d subagent(s) may each wait %s for "+
		"approval, for %s in total. Lower -gate-timeout below %s, or raise the run timeout",
		reviewBudget, runTimeout, concurrency, gateTimeout,
		reviewBudget+time.Duration(concurrency)*gateTimeout,
		(runTimeout-reviewBudget)/time.Duration(concurrency))
}
