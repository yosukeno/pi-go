package web

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/yosukeno/pi-go/agent"
)

// DefaultGateTimeout bounds how long a call waits for a human.
const DefaultGateTimeout = 5 * time.Minute

// Verdict is a human's answer to one approval request.
type Verdict struct {
	Allow bool
	// Args, when set, replaces what the model asked for: the approve-after-editing
	// path.
	Args json.RawMessage
	// Reason is fed back to the model on a refusal.
	Reason string
	// Remember is "tool" or "command" to grant a session-scoped pass. Ignored when
	// the call matched a danger pattern.
	Remember string
}

// WebGate implements agent.ToolGate against a browser.
//
// Review blocks in the runner's goroutine while the Hub and every SSE connection
// carry on independently. That is the payoff of tying a run's lifetime to the
// session: the tab can be closed, reloaded, or replaced and the pending approval
// is still there, with a deadline the new page can render correctly.
type WebGate struct {
	hub     *Hub
	policy  *Policy
	timeout time.Duration

	// serial makes sure only one card is on screen at a time.
	//
	// The loop now also reviews a parallel batch one call at a time, which is
	// what gives the cards a deterministic order — a mutex alone hands the queue
	// to whichever goroutine reaches it first, so the cards used to appear in an
	// order nobody chose. This stays anyway: it is the guarantee for any caller
	// that is not the loop, and a gate that stacks cards on a reviewer is worse
	// than a redundant lock.
	//
	// Neither layer may be replaced by making the batch sequential — that would
	// give up the parallelism instead of just the concurrent prompting.
	serial sync.Mutex

	mu      sync.Mutex
	pending map[string]*pendingGate
	n       int
}

type pendingGate struct {
	id string
	// All three are buffered so a UI action never blocks on the reviewer
	// goroutine's scheduling.
	decide chan Verdict
	freeze chan struct{}
	thaw   chan struct{}
}

func NewWebGate(hub *Hub, policy *Policy, timeout time.Duration) *WebGate {
	if timeout <= 0 {
		timeout = DefaultGateTimeout
	}
	return &WebGate{
		hub: hub, policy: policy, timeout: timeout,
		pending: make(map[string]*pendingGate),
	}
}

// Review implements agent.ToolGate.
func (g *WebGate) Review(ctx context.Context, req agent.GateRequest) agent.GateDecision {
	if rule, auto := g.policy.Decide(req); auto {
		// An automatic pass still gets published: the timeline should be able to
		// show that a call went through under a rule, without a card.
		g.hub.Publish(Event{
			Type: EvGateAuto, CallID: req.CallID, Name: req.ToolName, Rule: rule,
		})
		return agent.Allow
	}

	g.serial.Lock()
	defer g.serial.Unlock()

	// The run may have been cancelled while this call waited its turn in the
	// queue behind another card.
	if err := ctx.Err(); err != nil {
		return agent.Deny("Run was cancelled")
	}

	p := g.open()
	defer g.close(p.id)

	danger := Danger(req)
	deadline := time.Now().Add(g.timeout)
	g.hub.Publish(Event{
		Type: EvGateRequest, GateID: p.id, CallID: req.CallID, Name: req.ToolName,
		Args: req.Args, Deadline: deadline.UnixMilli(), Danger: danger,
	})

	// One resettable timer for the whole wait. Deliberately not created inside
	// the loop with a deferred Stop: that accumulates defers until the function
	// returns.
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()

	var (
		frozen    bool
		remaining time.Duration
	)
	for {
		select {
		case v := <-p.decide:
			d := agent.GateDecision{Allow: v.Allow, Reason: v.Reason, Args: v.Args}
			if !v.Allow && d.Reason == "" {
				d.Reason = "The user rejected this call"
			}
			// A danger match cannot be turned into a standing grant: one fast
			// click should not open something for the rest of the session.
			//
			// Neither can a subagent's call. The grant is session-scoped, so
			// remembering one would let a delegated command widen the policy the
			// user's own calls are judged by — the invariant runs the other way,
			// and this is the one place it could be inverted.
			if v.Allow && v.Remember != "" && len(danger) == 0 && req.Origin == "" {
				g.remember(v.Remember, req)
			}
			g.hub.Publish(Event{
				Type: EvGateResolved, GateID: p.id, CallID: req.CallID,
				Allow: v.Allow, Reason: d.Reason, By: "user",
			})
			return d

		case <-p.freeze:
			// The user started editing the arguments. Rewriting a command takes
			// time and must not be judged as not answering.
			if !frozen {
				frozen, remaining = true, time.Until(deadline)
				timer.Stop()
			}

		case <-p.thaw:
			if frozen {
				frozen = false
				deadline = time.Now().Add(remaining)
				timer.Reset(remaining)
				g.hub.Publish(Event{
					Type: EvGateDeadline, GateID: p.id, Deadline: deadline.UnixMilli(),
				})
			}

		case <-timer.C:
			// Refusing on timeout, never allowing: an unattended gate must fail
			// closed. The refusal is information, so the loop keeps going and the
			// model can say what it wanted and why it could not.
			reason := fmt.Sprintf("The user did not approve this call within %s", g.timeout)
			g.hub.Publish(Event{
				Type: EvGateResolved, GateID: p.id, CallID: req.CallID,
				Allow: false, Reason: reason, By: "timeout",
			})
			return agent.Deny(reason)

		case <-ctx.Done():
			g.hub.Publish(Event{
				Type: EvGateResolved, GateID: p.id, CallID: req.CallID,
				Allow: false, Reason: "Run was cancelled", By: "cancel",
			})
			return agent.Deny("Run was cancelled")
		}
	}
}

func (g *WebGate) remember(scope string, req agent.GateRequest) {
	var state PolicyState
	switch scope {
	case "tool":
		state = g.policy.AllowTool(req.ToolName)
	case "command":
		// The key, not the bare command: the grant covers the call the user actually
		// looked at, workdir included. See grantKeyOf.
		key := grantKeyOf(req.Args)
		if key == "" {
			return
		}
		state = g.policy.AllowCommand(key)
	default:
		return
	}
	g.hub.Publish(Event{Type: EvPolicyChanged, Policy: &state, By: "user"})
}

func (g *WebGate) open() *pendingGate {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	p := &pendingGate{
		id:     fmt.Sprintf("g%d", g.n),
		decide: make(chan Verdict, 1),
		freeze: make(chan struct{}, 1),
		thaw:   make(chan struct{}, 1),
	}
	g.pending[p.id] = p
	return p
}

func (g *WebGate) close(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.pending, id)
}

func (g *WebGate) get(id string) (*pendingGate, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p, ok := g.pending[id]
	return p, ok
}

// Decide delivers a verdict from the UI.
func (g *WebGate) Decide(id string, v Verdict) error {
	p, ok := g.get(id)
	if !ok {
		return fmt.Errorf("no pending approval %q", id)
	}
	select {
	case p.decide <- v:
		return nil
	default:
		// Already answered by another tab, or resolved by timeout in between.
		return fmt.Errorf("approval %q is already resolved", id)
	}
}

// Freeze pauses the countdown while the user edits the arguments.
func (g *WebGate) Freeze(id string) error {
	return g.signal(id, func(p *pendingGate) chan struct{} { return p.freeze })
}

// Thaw resumes a paused countdown from where it stopped.
func (g *WebGate) Thaw(id string) error {
	return g.signal(id, func(p *pendingGate) chan struct{} { return p.thaw })
}

func (g *WebGate) signal(id string, pick func(*pendingGate) chan struct{}) error {
	p, ok := g.get(id)
	if !ok {
		return fmt.Errorf("no pending approval %q", id)
	}
	select {
	case pick(p) <- struct{}{}:
	default:
		// A duplicate click is not an error; the reviewer is already in that state.
	}
	return nil
}
