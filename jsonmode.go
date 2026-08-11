package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/wangy/pi-go/agent"
	"github.com/wangy/pi-go/wire"
)

// consumer turns the loop's event stream into output.
//
// Two implementations: the terminal renderer (tui.Renderer) and the JSON
// emitter below. The
// interface is deliberately this narrow — one method, no configuration — because
// it is the whole reason the loop emits events instead of printing. Adding a
// third front end should not require touching anything above this line.
type consumer interface {
	Consume(events <-chan agent.Event) error
}

// jsonEmitter writes one JSON object per line to out.
//
// Every line is a complete object and nothing else is ever written to the same
// stream, which is the entire contract: `pi-go -mode json -p "..." | jq` must
// work without a consumer having to skip a banner or tolerate a stray progress
// line. Anything a human would read goes to stderr instead — see diagOut.
type jsonEmitter struct {
	out io.Writer
	// enc is kept rather than created per event so the newline handling is the
	// encoder's job, not a Fprintln at every call site.
	enc *json.Encoder
}

func newJSONEmitter(out io.Writer) *jsonEmitter {
	enc := json.NewEncoder(out)
	// No indentation: one object per line is the format, and an indented object
	// spans lines, which would break every line-oriented reader.
	return &jsonEmitter{out: out, enc: enc}
}

// header writes the first line. It is separate from Consume because it describes
// the session rather than the run: a consumer needs the transcript path before
// any events arrive, and a resumed session emits it exactly once even though
// consume runs per turn.
func (j *jsonEmitter) header(h wire.Header) error {
	h.Type, h.TS = wire.Session, wire.NowMS()
	return j.enc.Encode(h)
}

// Consume drains the loop's events, writing each as one line.
//
// It returns the run's error, matching the terminal renderer: the exit status of
// `-p` must not depend on which front end printed it. Events the contract does
// not name are skipped rather than guessed at — see wire.FromAgent.
func (j *jsonEmitter) Consume(events <-chan agent.Event) error {
	// The channel has to be drained even after a write failure: the loop blocks
	// on its terminating send, and abandoning it here would leak the run
	// goroutine and lose the session flush that follows.
	var runErr, writeErr error
	for e := range events {
		if e.Kind == agent.EventAgentEnd && e.Err != nil {
			runErr = e.Err
		}
		ev, ok := wire.FromAgent(e)
		if !ok || writeErr != nil {
			continue
		}
		if err := j.enc.Encode(ev); err != nil {
			// A broken pipe is the common case (`| head`), and it is not the
			// run's fault. Recorded and reported once, rather than per event.
			writeErr = fmt.Errorf("writing json event: %w", err)
		}
	}
	if runErr != nil {
		return runErr
	}
	return writeErr
}
