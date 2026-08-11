package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/yosukeno/pi-go/llm"
	"github.com/yosukeno/pi-go/tools"
)

// alwaysTools scripts a client that asks for a tool call on every turn, so only
// a cap can stop the run.
func alwaysTools(n int) []llm.Response {
	responses := make([]llm.Response, n)
	for i := range responses {
		responses[i] = toolCalls("t")
	}
	return responses
}

func noticesIn(msgs []llm.Message) []string {
	var out []string
	for _, m := range msgs {
		if m.Role == llm.RoleUser && strings.Contains(m.Text(), "Turn checkpoint") {
			out = append(out, m.Text())
		}
	}
	return out
}

// The soft cap does not end the run: it injects one message per interval and the
// loop continues, so only the hard cap stops an always-tools model.
func TestSoftCapInjectsCheckpointsAndOnlyTheHardCapStops(t *testing.T) {
	tool := &fakeTool{name: "t"}
	c := &fakeClient{responses: alwaysTools(10)}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), MaxTurns: 5, SoftTurns: 2})

	events := drain(a.Run(context.Background(), "go"))

	if got := c.callCount(); got != 5 {
		t.Errorf("model called %d times, want 5: the soft cap must not end the run", got)
	}
	notices := noticesIn(a.Messages())
	if len(notices) != 2 {
		t.Fatalf("got %d checkpoints, want 2 (at 2 and 4 used turns): %v", len(notices), notices)
	}
	if !strings.Contains(notices[0], "used 2 turn(s)") || !strings.Contains(notices[1], "used 4 turn(s)") {
		t.Errorf("checkpoint texts = %v, want the used counts 2 and 4", notices)
	}
	var end *Event
	steerEvents := 0
	for i := range events {
		if events[i].Kind == EventSteer {
			steerEvents++
			if events[i].Text != notices[0] && events[i].Text != notices[1] {
				t.Errorf("steer event text %q is not a checkpoint that entered the transcript", events[i].Text)
			}
		}
		if events[i].Kind == EventAgentEnd {
			end = &events[i]
		}
	}
	if steerEvents != 2 {
		t.Errorf("steer events = %d, want 2: the notice rides the steering path, "+
			"so JSON-mode and web consumers see it as the user message it is", steerEvents)
	}
	if end == nil || end.EndReason != EndTurnLimit {
		t.Fatalf("end = %+v, want the hard cap's turn_limit", end)
	}
}

// The model must actually receive the notice: it is part of the history handed
// to the next call, not just the transcript.
func TestSoftCapNoticeReachesTheModel(t *testing.T) {
	tool := &fakeTool{name: "t"}
	c := &fakeClient{responses: alwaysTools(10)}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), MaxTurns: 4, SoftTurns: 2})

	drain(a.Run(context.Background(), "go"))

	// Call 3 is the first turn after the checkpoint at 2 used turns.
	seen := c.seen[2]
	last := seen[len(seen)-1]
	if last.Role != llm.RoleUser || !strings.Contains(last.Text(), "Turn checkpoint") {
		t.Errorf("last message before turn 3 = %v %q, want the checkpoint notice", last.Role, last.Text())
	}
}

// A plain answer in reply to the checkpoint ends the run as it always would have:
// the mechanism changes what the model knows, not how a run finishes.
func TestSoftCapNoticeThenModelFinishes(t *testing.T) {
	tool := &fakeTool{name: "t"}
	c := &fakeClient{responses: append(alwaysTools(2), llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "done, nothing left"}}},
		StopReason: llm.StopEndTurn,
	})}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), MaxTurns: 10, SoftTurns: 2})

	events := drain(a.Run(context.Background(), "go"))

	if got := c.callCount(); got != 3 {
		t.Errorf("model called %d times, want 3", got)
	}
	if notices := noticesIn(a.Messages()); len(notices) != 1 {
		t.Errorf("got %d checkpoints, want 1", len(notices))
	}
	var end *Event
	for i := range events {
		if events[i].Kind == EventAgentEnd {
			end = &events[i]
		}
	}
	if end == nil || end.EndReason != EndCompleted {
		t.Fatalf("end = %+v, want completed: answering after a checkpoint is a normal finish", end)
	}
}

// Zero keeps every existing caller byte-identical: no notice, no steer event.
func TestSoftCapZeroInjectsNothing(t *testing.T) {
	tool := &fakeTool{name: "t"}
	c := &fakeClient{responses: alwaysTools(10)}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), MaxTurns: 3})

	events := drain(a.Run(context.Background(), "go"))

	if notices := noticesIn(a.Messages()); len(notices) != 0 {
		t.Errorf("soft cap unset but got checkpoints: %v", notices)
	}
	for _, e := range events {
		if e.Kind == EventSteer {
			t.Errorf("soft cap unset but a steer event fired: %q", e.Text)
		}
	}
	if got := c.callCount(); got != 3 {
		t.Errorf("model called %d times, want 3", got)
	}
}

// A soft cap at or above the hard one can never fire: the hard check returns
// first. Documented as a no-op rather than rejected, so a script that sets both
// does not need to order them.
func TestSoftCapAtOrAboveHardCapNeverFires(t *testing.T) {
	for _, soft := range []int{3, 10} {
		tool := &fakeTool{name: "t"}
		c := &fakeClient{responses: alwaysTools(10)}
		a := New(Config{Client: c, Registry: tools.NewRegistry(tool), MaxTurns: 3, SoftTurns: soft})

		drain(a.Run(context.Background(), "go"))

		if notices := noticesIn(a.Messages()); len(notices) != 0 {
			t.Errorf("soft=%d hard=3: got checkpoints %v, want none", soft, notices)
		}
	}
}

// The notice text is the whole contract with the model: where the run stands,
// what the choice is, and that "what is left" is owed. Pin the shape so a
// wording change is a deliberate act.
func TestSoftCapNoticeStatesThePositionAndTheChoice(t *testing.T) {
	tool := &fakeTool{name: "t"}
	c := &fakeClient{responses: alwaysTools(10)}
	a := New(Config{Client: c, Registry: tools.NewRegistry(tool), MaxTurns: 8, SoftTurns: 2})

	drain(a.Run(context.Background(), "go"))

	notices := noticesIn(a.Messages())
	if len(notices) == 0 {
		t.Fatal("no checkpoint injected")
	}
	for _, want := range []string{"[pi-go]", "soft cap 2", "hard cap 8", "final answer", "still missing"} {
		if !strings.Contains(notices[0], want) {
			t.Errorf("checkpoint notice missing %q:\n%s", want, notices[0])
		}
	}
}
