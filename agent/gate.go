package agent

import (
	"context"
	"encoding/json"
)

// ToolGate decides whether a tool call may run. It is the pi-go equivalent of
// pi's AgentLoopConfig.beforeToolCall.
//
// Implementations must be safe for concurrent use: a parallel batch enters
// Review from several goroutines at once. An implementation that wants to show
// the user one prompt at a time should serialize internally rather than force
// the whole batch to run sequentially.
//
// Review may block for as long as it needs, including waiting on a human. It
// must respect ctx: a cancelled run has to unwind promptly rather than hold the
// loop open until a timeout fires.
type ToolGate interface {
	Review(ctx context.Context, req GateRequest) GateDecision
}

type GateRequest struct {
	CallID   string
	ToolName string
	Args     json.RawMessage
	// Turn is the 1-based loop iteration that produced this call.
	Turn int
	// Origin names the subagent that raised this call, or is empty for the main
	// conversation.
	//
	// It exists because a reviewer needs to know: a command they never asked for is
	// unreviewable without knowing who wants to run it. It also carries a rule —
	// a decision about a subagent's call must not become a standing grant, since
	// the person approved one delegated command, not a permanent opening in their
	// own session. See WebGate.Review.
	Origin string
}

type GateDecision struct {
	Allow bool
	// Reason explains a refusal. It is fed back as the tool result text, so the
	// model reads it and can adapt.
	Reason string
	// Args, when non-nil, replaces the arguments the model supplied. This is the
	// "approve after editing" path.
	Args json.RawMessage
}

// Allow is the decision a gate returns to let a call through unchanged.
var Allow = GateDecision{Allow: true}

// Deny builds a refusal carrying an explanation for the model.
func Deny(reason string) GateDecision {
	return GateDecision{Allow: false, Reason: reason}
}
