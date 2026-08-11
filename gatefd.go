package main

import (
	"context"
	"sync"

	"github.com/wangy/pi-go/agent"
	"github.com/wangy/pi-go/tools"
)

// childGate is the approval gate of a subagent process. It forwards every call to
// the parent, which applies its own policy.
//
// This is what closes the hole the design notes recorded before subagents existed:
// if a parent's bash needs approval but the model can delegate to a child whose
// bash does not, the gate has stopped meaning anything. The child does not get a
// copy of the policy — it asks — so a subagent's permissions cannot exceed its
// parent's even in principle.
type childGate struct {
	// serial keeps one question outstanding at a time. The channel underneath is a
	// single pipe with one reply per request, so two concurrent asks would read
	// each other's answers; a parallel batch in the child has to queue here.
	serial sync.Mutex
	fd     *tools.FDGate
}

// newChildGate returns a gate when this process was given an approval channel, or
// nil. Nil is the ordinary case for `pi-go -p`, where the loop's own comment
// explains why: there is no human to ask.
func newChildGate() agent.ToolGate {
	fd := tools.OpenFDGate()
	if fd == nil {
		return nil
	}
	return &childGate{fd: fd}
}

// Review implements agent.ToolGate.
func (g *childGate) Review(ctx context.Context, req agent.GateRequest) agent.GateDecision {
	// A cancelled run should not queue behind another approval to then be told no.
	if err := ctx.Err(); err != nil {
		return agent.Deny("the run was cancelled")
	}
	g.serial.Lock()
	defer g.serial.Unlock()
	if err := ctx.Err(); err != nil {
		return agent.Deny("the run was cancelled")
	}
	allow, reason := g.fd.Ask(req.CallID, req.ToolName, req.Args)
	if allow {
		return agent.Allow
	}
	return agent.Deny(reason)
}
