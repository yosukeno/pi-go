package agent

import "github.com/wangy/pi-go/llm"

// TurnPersister rebuilds a run's transcript from its event stream as it
// happens, so a process killed mid-run loses only the in-flight turn rather
// than everything since the last end-of-run flush.
//
// The unit is the turn, not the message. An assistant message that asked for
// tools is held until its results land: on disk it must never sit without
// them, because the API rejects a history whose last tool_use has no
// tool_result — which is what "unresumable" means. The wait is bounded by the
// loop's own pairing guarantee (every tool_use gets a result, even on cancel),
// and an aborted response carries no message at all (llm/openai.go returns
// StopAborted with an empty Message), so pending never holds one of those.
// Steering messages and the soft-cap notice are complete user messages and
// persist as they pass; the final answer persists when EventAgentEnd closes
// the run.
//
// The opening prompt is the caller's to append: the loop does not announce it
// (see EventSteer), so the persister cannot either.
//
// The first append error latches Failed and every later event is a no-op: a
// half-persisted run is worse than a cleanly partial one, because the caller's
// end-of-run flush reconciles from its own count of what actually landed.
type TurnPersister struct {
	appendFn func(llm.Message) error
	onError  func(error)

	pending *llm.Message // an assistant message waiting on its tool results
	failed  bool
	n       int
}

// NewTurnPersister wires the sink. onError may be nil; it fires once, on the
// first failure.
func NewTurnPersister(appendFn func(llm.Message) error, onError func(error)) *TurnPersister {
	return &TurnPersister{appendFn: appendFn, onError: onError}
}

// Add appends a message that never appears in the event stream — the run's
// opening prompt, which the loop deliberately does not announce.
func (p *TurnPersister) Add(m llm.Message) {
	p.flush()
	p.emit(m)
}

// OnEvent feeds one loop event. Anything the persister does not recognise is
// not a transcript message and passes through untouched.
func (p *TurnPersister) OnEvent(e Event) {
	switch e.Kind {
	case EventMessage:
		// Unreachable today — every path out of a turn flushes pending first —
		// but flushing here too keeps the rule uniform: order is preserved no
		// matter what the loop does next.
		p.flush()
		m := e.Message
		p.pending = &m
	case EventToolResults:
		p.flush()
		p.emit(e.Message)
	case EventSteer:
		p.flush()
		p.emit(llm.UserText(e.Text))
	case EventAgentEnd:
		p.flush()
	}
}

// Persisted reports how many messages landed. The caller adds it to its own
// baseline; anything short of the run's full history is the end flush's job.
func (p *TurnPersister) Persisted() int { return p.n }

// Failed reports that an append failed and the persister has gone quiet. The
// caller should expect its end flush to write the rest.
func (p *TurnPersister) Failed() bool { return p.failed }

func (p *TurnPersister) flush() {
	if p.pending == nil {
		return
	}
	m := *p.pending
	p.pending = nil
	p.emit(m)
}

func (p *TurnPersister) emit(m llm.Message) {
	if p.failed {
		return
	}
	if err := p.appendFn(m); err != nil {
		p.failed = true
		if p.onError != nil {
			p.onError(err)
		}
		return
	}
	p.n++
}
