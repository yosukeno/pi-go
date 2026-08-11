package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/yosukeno/pi-go/llm"
	"github.com/yosukeno/pi-go/tools"
)

// collectPersister runs a fake session, feeding every event to a persister
// whose sink records instead of writing, and returns what it "persisted".
func collectPersister(t *testing.T, a *Agent, prompt string) []llm.Message {
	t.Helper()
	var disk []llm.Message
	p := NewTurnPersister(func(m llm.Message) error {
		disk = append(disk, m)
		return nil
	}, func(err error) { t.Errorf("append failed: %v", err) })
	p.Add(llm.UserText(prompt))
	drain(tap(a.Run(context.Background(), prompt), p.OnEvent))
	return disk
}

// tap is the test-local copy of the CLI's tapEvents: f sees each event first.
func tap(in <-chan Event, f func(Event)) <-chan Event {
	out := make(chan Event)
	go func() {
		defer close(out)
		for e := range in {
			f(e)
			out <- e
		}
	}()
	return out
}

// The whole point, stated as an invariant: prompt plus the events equals the
// transcript, message for message, in order — including a steered message and
// the final answer.
func TestTurnPersisterReproducesTheRun(t *testing.T) {
	tool := &fakeTool{name: "t"}
	c := &fakeClient{responses: []llm.Response{
		toolCalls("t"),
		toolCalls("t"),
		{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "done"}}},
			StopReason: llm.StopEndTurn},
	}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), MaxTurns: 10, SoftTurns: 1})

	disk := collectPersister(t, a, "go")

	if !reflect.DeepEqual(disk, a.Messages()) {
		t.Fatalf("persisted != transcript:\npersisted:  %v\ntranscript: %v", rolesTexts(disk), rolesTexts(a.Messages()))
	}
	// The soft cap at 1 fired, so the run contained a checkpoint notice — the
	// comparison above then proves notices persist too, in position.
	if len(disk) < 6 {
		t.Errorf("got %d messages, want prompt + 2 turns + notices + answer", len(disk))
	}
}

func rolesTexts(ms []llm.Message) []string {
	var out []string
	for _, m := range ms {
		out = append(out, string(m.Role)+":"+m.Text())
	}
	return out
}

// A kill can land between any two events. At every such point the persisted
// prefix must end at a clean boundary: never an assistant message still
// waiting on its tool results, because that history is unresumable.
func TestTurnPersisterNeverLeavesAnUnpairedToolUseOnDisk(t *testing.T) {
	tool := &fakeTool{name: "t"}
	c := &fakeClient{responses: append(alwaysTools(3), llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "done"}}},
		StopReason: llm.StopEndTurn,
	})}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), MaxTurns: 10})

	var events []Event
	drain(tap(a.Run(context.Background(), "go"), func(e Event) { events = append(events, e) }))

	for cut := 0; cut <= len(events); cut++ {
		var disk []llm.Message
		p := NewTurnPersister(func(m llm.Message) error { disk = append(disk, m); return nil }, nil)
		p.Add(llm.UserText("go"))
		for _, e := range events[:cut] {
			p.OnEvent(e)
		}
		if len(disk) == 0 {
			continue
		}
		last := disk[len(disk)-1]
		if last.Role == llm.RoleAssistant && len(last.ToolCalls()) > 0 {
			t.Errorf("killed after event %d: disk ends with tool calls awaiting results", cut)
		}
	}
}

// The first append error latches: nothing later is written — nothing later is
// even *attempted* — so the on-disk messages are always a prefix of the run and
// the end flush can reconcile by count.
func TestTurnPersisterLatchesTheFirstFailure(t *testing.T) {
	boom := errors.New("disk full")
	var disk []llm.Message
	calls, errs := 0, 0
	p := NewTurnPersister(func(m llm.Message) error {
		calls++
		if len(disk) >= 1 {
			return boom
		}
		disk = append(disk, m)
		return nil
	}, func(error) { errs++ })

	p.Add(llm.UserText("go"))
	p.OnEvent(Event{Kind: EventMessage, Message: llm.Message{Role: llm.RoleAssistant,
		Content: []llm.Block{{Type: llm.BlockText, Text: "answer"}}}})
	p.OnEvent(Event{Kind: EventAgentEnd})

	if p.Persisted() != 1 {
		t.Errorf("persisted = %d, want 1 (the prompt)", p.Persisted())
	}
	if !p.Failed() {
		t.Error("Failed should be latched after the first append error")
	}
	// Later events are no-ops — and that has to be observable: one more append
	// attempt would be one more error, so count calls, not just successes.
	p.OnEvent(Event{Kind: EventSteer, Text: "late"})
	if calls != 2 {
		t.Errorf("appendFn called %d times, want 2: after the latch no append may be attempted", calls)
	}
	if errs != 1 {
		t.Errorf("onError fired %d times, want exactly 1", errs)
	}
	if p.Persisted() != 1 {
		t.Errorf("persisted = %d after a latched failure, want 1", p.Persisted())
	}
}

// A text-only answer kept open by steering: the answer must hit disk before
// the steering message, which is the order they hold in the history.
func TestTurnPersisterFlushesAnAnswerBeforeLandingSteering(t *testing.T) {
	tool := &fakeTool{name: "t"}
	c := &fakeClient{responses: []llm.Response{
		toolCalls("t"),
		{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "first answer"}}},
			StopReason: llm.StopEndTurn},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "second answer"}}},
			StopReason: llm.StopEndTurn},
	}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), MaxTurns: 10})
	c.onStream = func(n int) {
		if n == 2 && !a.Steer("one more thing") {
			t.Error("steering was refused mid-run")
		}
	}

	disk := collectPersister(t, a, "go")

	if !reflect.DeepEqual(disk, a.Messages()) {
		t.Fatalf("persisted != transcript:\npersisted:  %v\ntranscript: %v", rolesTexts(disk), rolesTexts(a.Messages()))
	}
	// Spot the order directly: first answer, then the steering, then the second.
	var seq []string
	for _, m := range disk {
		seq = append(seq, string(m.Role)+":"+m.Text())
	}
	want := []string{"user:go", "assistant:", "user:", "assistant:first answer", "user:one more thing", "assistant:second answer"}
	if !reflect.DeepEqual(seq, want) {
		t.Errorf("sequence = %v, want %v", seq, want)
	}
}
