package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// collector records fragments with the time each arrived, which is what the
// ordering assertions below need.
type collector struct {
	mu    sync.Mutex
	parts []string
	times []time.Time
}

func (c *collector) add(p Partial) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.parts = append(c.parts, p.Text)
	c.times = append(c.times, time.Now())
}

func (c *collector) joined() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.parts, "")
}

func (c *collector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.parts)
}

// The first write must be forwarded at once. Holding it back until a second write
// arrives would mean a command that prints one line and then works silently shows
// nothing — the case this whole feature exists for.
func TestFirstFragmentIsNotHeldBack(t *testing.T) {
	var c collector
	s := newPartialSink(c.add)

	if _, err := s.Write([]byte("first line\n")); err != nil {
		t.Fatal(err)
	}
	if c.count() != 1 {
		t.Fatalf("first write produced %d fragments, want 1", c.count())
	}
	if c.joined() != "first line\n" {
		t.Errorf("got %q", c.joined())
	}
}

// Output arriving faster than the interval is batched, so a chatty command does
// not turn every few bytes into an event that fans out to every client.
func TestRapidWritesAreCoalesced(t *testing.T) {
	var c collector
	s := newPartialSink(c.add)

	for i := 0; i < 50; i++ {
		if _, err := s.Write([]byte(fmt.Sprintf("line %d\n", i))); err != nil {
			t.Fatal(err)
		}
	}
	s.close()

	// One immediate fragment plus the flush at close; well short of 50 either way.
	if n := c.count(); n > 5 {
		t.Errorf("50 quick writes produced %d fragments, want them coalesced", n)
	}
	// Coalescing must not lose anything.
	for i := 0; i < 50; i++ {
		if !strings.Contains(c.joined(), fmt.Sprintf("line %d\n", i)) {
			t.Fatalf("line %d was lost", i)
		}
	}
	if c.joined() != s.String() {
		t.Error("the forwarded text and the recorded output disagree")
	}
}

// A slow trickle must not be batched: each line trips the interval and should
// appear on its own.
func TestSlowOutputIsForwardedPromptly(t *testing.T) {
	var c collector
	s := newPartialSink(c.add)

	for i := 0; i < 3; i++ {
		if _, err := s.Write([]byte(fmt.Sprintf("tick %d\n", i))); err != nil {
			t.Fatal(err)
		}
		time.Sleep(partialInterval + 20*time.Millisecond)
	}
	if n := c.count(); n != 3 {
		t.Errorf("got %d fragments for 3 slow lines, want 3", n)
	}
}

// close exists so the last lines are not held back by coalescing after the
// command has already exited.
func TestCloseFlushesTheRemainder(t *testing.T) {
	var c collector
	s := newPartialSink(c.add)

	// First write flushes; the second is inside the interval and is held.
	if _, err := s.Write([]byte("a\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("b\n")); err != nil {
		t.Fatal(err)
	}
	if got := c.joined(); got != "a\n" {
		t.Fatalf("before close: got %q, want just the first line", got)
	}
	s.close()
	if got := c.joined(); got != "a\nb\n" {
		t.Errorf("after close: got %q", got)
	}
}

// Streaming stops at a budget, because every fragment fans out to every client.
// The recorded output must be unaffected — it has its own, separate limit.
func TestStreamingStopsAtItsBudgetButRecordsEverything(t *testing.T) {
	var c collector
	s := newPartialSink(c.add)

	chunk := strings.Repeat("x", 8<<10) + "\n"
	total := 0
	for total < partialBudget*2 {
		if _, err := s.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
		total += len(chunk)
		time.Sleep(2 * time.Millisecond)
	}
	s.close()

	if got := len(c.joined()); got > partialBudget+512 {
		t.Errorf("forwarded %d bytes, want at most about %d", got, partialBudget)
	}
	// A stream that simply stops looks like a bug; the gap has to be explained.
	if !strings.Contains(c.joined(), "live output paused") {
		t.Error("the stream stopped without saying why")
	}
	if len(s.String()) < total {
		t.Errorf("recorded %d bytes of %d written; the budget must not affect the result",
			len(s.String()), total)
	}
}

// The point of the whole exercise: output has to be visible while the command is
// still running.
func TestBashStreamsBeforeItFinishes(t *testing.T) {
	b := &Bash{Cwd: t.TempDir()}
	var c collector
	firstSeen := make(chan time.Time, 1)

	started := time.Now()
	args := json.RawMessage(`{"command":"echo early; sleep 2; echo late"}`)
	res, err := b.ExecuteStreaming(context.Background(), args, func(p Partial) {
		select {
		case firstSeen <- time.Now():
		default:
		}
		c.add(p)
	})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)

	var first time.Time
	select {
	case first = <-firstSeen:
	default:
		t.Fatal("no output was streamed at all")
	}
	// The first line must arrive well before the command exits, not with it.
	if lead := elapsed - first.Sub(started); lead < time.Second {
		t.Errorf("first fragment arrived only %v before the command finished (total %v)", lead, elapsed)
	}
	if !strings.Contains(c.joined(), "early") || !strings.Contains(c.joined(), "late") {
		t.Errorf("streamed output is incomplete: %q", c.joined())
	}
	// The settled result is still whole, so a consumer that ignored every fragment
	// is not missing anything.
	for _, want := range []string{"early", "late"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("result missing %q: %q", want, res.Text)
		}
	}
}

// Execute must behave exactly as before, since six of the seven built-ins and
// every existing caller go through it.
func TestBashWithoutAnObserverIsUnchanged(t *testing.T) {
	b := &Bash{Cwd: t.TempDir()}
	res, err := b.Execute(context.Background(), json.RawMessage(`{"command":"echo hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "hi") {
		t.Errorf("got %q", res.Text)
	}
	if _, ok := res.Details.(BashDetails); !ok {
		t.Errorf("details = %T, want BashDetails", res.Details)
	}
}

// Only bash streams. The others produce their whole output at the end, and
// declaring otherwise would be a claim the loop acts on.
func TestOnlyBashDeclaresStreaming(t *testing.T) {
	for _, tool := range Default(t.TempDir()).All() {
		_, streams := tool.(StreamingTool)
		if want := tool.Name() == "bash"; streams != want {
			t.Errorf("%s streaming = %v, want %v", tool.Name(), streams, want)
		}
	}
}
