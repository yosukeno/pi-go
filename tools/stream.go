package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Partial is a fragment of a tool's output, handed over while the tool is still
// running.
type Partial struct {
	// Text is newly produced output. It is a delta, appended to whatever came
	// before, not a replacement for it.
	Text string
	// Frame is one structured event, for a tool whose progress has structure rather
	// than being a stream of bytes. Only the subagent tool has any: a delegated run
	// produces turns, tool calls and an answer, and flattening that to a line of
	// text throws away everything an interface could show.
	//
	// Alongside Text rather than instead of it, because the two consumers want
	// different things and neither should have to parse the other's. A terminal wants
	// a line; a browser wants the event. A tool that sets Frame should set Text too.
	Frame json.RawMessage
}

// StreamingTool is the optional interface for a tool whose output is worth
// watching before it finishes.
//
// Optional rather than part of Tool on purpose. ExecutionMode is in the interface
// because getting it wrong causes data races, so every tool must answer it.
// Streaming is not like that: a tool that does not stream is merely less pleasant
// to watch, and six of the seven built-ins produce their whole output at the end
// anyway. Putting it in Tool would make all of them carry a parameter they ignore.
//
// A tool that implements this should make ExecuteStreaming the real
// implementation and have Execute delegate with a nil observer, so there is only
// one code path to be correct.
type StreamingTool interface {
	Tool
	// ExecuteStreaming runs the tool, calling onPartial as output appears.
	// onPartial may be nil, and may be called from another goroutine, but never
	// after ExecuteStreaming has returned.
	ExecuteStreaming(ctx context.Context, args json.RawMessage, onPartial func(Partial)) (Result, error)
}

const (
	// partialInterval is the shortest gap between forwarded fragments. Output
	// arriving faster than this is coalesced.
	partialInterval = 100 * time.Millisecond
	// partialChunk forces a flush once this much has piled up, so a command
	// producing output quickly is still seen promptly rather than in one lump.
	partialChunk = 4 << 10
	// partialBudget caps how much output is streamed in total.
	//
	// The final result is capped separately and keeps the tail (see truncate.go).
	// This is a different limit for a different reason: every fragment becomes an
	// event that fans out to every connected client, so a command printing
	// hundreds of megabytes would flood the transport rather than the context.
	partialBudget = 256 << 10
)

// partialSink accumulates a command's output and forwards it in coalesced
// fragments.
//
// It is the authoritative buffer as well as the forwarder, so the streamed
// fragments and the final result come from the same bytes and cannot disagree.
type partialSink struct {
	onPartial func(Partial)

	mu  sync.Mutex
	buf bytes.Buffer
	// pending is output not yet forwarded, held back to coalesce.
	pending   bytes.Buffer
	lastFlush time.Time
	forwarded int
	capped    bool
}

// newPartialSink starts a sink whose first write flushes immediately.
//
// lastFlush is deliberately left at the zero time. Seeding it with time.Now
// would hold the first fragment back until a second write arrived, so a command
// that prints one line and then works silently for a minute would show nothing at
// all — the exact case this feature exists for.
func newPartialSink(onPartial func(Partial)) *partialSink {
	return &partialSink{onPartial: onPartial}
}

// Write records output and forwards it when the coalescing rules allow.
//
// Both cmd.Stdout and cmd.Stderr point here. os/exec gives them a single pipe
// when they are the same value, so writes arrive serialized, but the lock is kept
// because relying on that would make an unrelated change to bash.go able to
// introduce a race silently.
func (s *partialSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.buf.Write(p)
	if s.onPartial == nil {
		return len(p), nil
	}
	s.pending.Write(p)

	// Flushing on elapsed time as well as size is what makes slow output prompt:
	// a command printing one line every few seconds trips the interval every
	// time, while one spewing output trips the size limit and gets batched.
	if s.pending.Len() >= partialChunk || time.Since(s.lastFlush) >= partialInterval {
		s.flushLocked()
	}
	return len(p), nil
}

// String is the complete output, for the caller to render and truncate.
func (s *partialSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// close flushes whatever is left. Called once the command has exited, so the last
// few lines are not held back waiting for a fragment that will never come.
func (s *partialSink) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushLocked()
}

func (s *partialSink) flushLocked() {
	if s.pending.Len() == 0 || s.capped {
		return
	}
	text := s.pending.String()
	s.pending.Reset()
	s.lastFlush = time.Now()

	if remaining := partialBudget - s.forwarded; len(text) > remaining {
		// Stop streaming, but say so. Freezing mid-output and then jumping to the
		// final result looks like a bug; a note explains the gap.
		text = text[:max(remaining, 0)] + fmt.Sprintf(
			"\n[live output paused after %s; the full result follows when the command finishes]\n",
			formatSize(partialBudget))
		s.capped = true
	}
	s.forwarded += len(text)
	s.onPartial(Partial{Text: text})
}
