package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pi-go/agent"
	"github.com/yosukeno/pi-go/analyze"
	"github.com/yosukeno/pi-go/config"
	"github.com/yosukeno/pi-go/llm"
	"github.com/yosukeno/pi-go/session"
	"github.com/yosukeno/pi-go/skills"
)

// --- Hub ---------------------------------------------------------------------

func TestSnapshotCarriesAccumulatedTextWithoutReplayingDeltas(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvRunStart, RunID: "r1", Model: "k3"})
	h.Publish(Event{Type: EvUserMessage, Text: "hello"})
	h.Publish(Event{Type: EvTurnStart, Turn: 1})
	for _, s := range []string{"Hel", "lo ", "there"} {
		h.Publish(Event{Type: EvToken, Text: s})
	}

	backlog, _, cancel := h.Subscribe(-1)
	defer cancel()

	if len(backlog) != 1 || backlog[0].Type != EvSnapshot {
		t.Fatalf("expected exactly one snapshot frame, got %d frames", len(backlog))
	}
	snap := backlog[0].Snapshot
	if snap.Live.Text != "Hello there" {
		t.Errorf("live text = %q, want the full accumulation", snap.Live.Text)
	}
	if !snap.Live.Active || snap.Live.Turn != 1 {
		t.Errorf("live state lost the run: %+v", snap.Live)
	}
	if len(snap.Messages) != 1 || snap.Messages[0].Role != "user" {
		t.Errorf("messages = %+v, want the user prompt", snap.Messages)
	}

	// The point of the snapshot is that the log never has to hold deltas.
	for _, e := range h.log {
		if e.Type == EvToken || e.Type == EvThinking {
			t.Fatalf("delta event %s entered the replay log", e.Type)
		}
	}
}

func TestMessageIDLetsClientsPlaceDeltas(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvRunStart, RunID: "r1"})
	h.Publish(Event{Type: EvTurnStart, Turn: 1})

	_, ch, cancel := h.Subscribe(0)
	defer cancel()
	h.Publish(Event{Type: EvToken, Text: "x"})

	got := <-ch
	if got.MessageID == "" {
		t.Fatal("token event carried no message_id")
	}
	if want := h.Snapshot().Live.MessageID; got.MessageID != want {
		t.Errorf("message_id = %q, want %q", got.MessageID, want)
	}
}

func TestIncrementalReplayReturnsOnlyLaterEvents(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvRunStart, RunID: "r1"})
	h.Publish(Event{Type: EvTurnStart, Turn: 1})
	mark := h.Seq()
	h.Publish(Event{Type: EvToolStart, CallID: "c1", Name: "read"})
	h.Publish(Event{Type: EvToolEnd, CallID: "c1", Name: "read", Text: "ok"})

	backlog, _, cancel := h.Subscribe(mark)
	defer cancel()

	if len(backlog) != 2 {
		t.Fatalf("replayed %d events, want 2", len(backlog))
	}
	if backlog[0].Type != EvToolStart || backlog[1].Type != EvToolEnd {
		t.Errorf("replay out of order: %s, %s", backlog[0].Type, backlog[1].Type)
	}
	for _, e := range backlog {
		if e.Seq <= mark {
			t.Errorf("replayed already-seen seq %d", e.Seq)
		}
	}
}

func TestSlowSubscriberIsDroppedAndRecoversByReplay(t *testing.T) {
	h := NewHub()
	_, ch, cancel := h.Subscribe(0)
	defer cancel()

	for i := 0; i < subBuffer+10; i++ {
		h.Publish(Event{Type: EvToolStart, CallID: fmt.Sprintf("c%d", i), Name: "read"})
	}

	// Draining until the channel closes: falling behind costs the subscription,
	// never the data.
	drained := 0
	for range ch {
		drained++
	}
	if drained == 0 {
		t.Fatal("subscriber received nothing before being dropped")
	}
	if h.Subscribers() != 0 {
		t.Error("dropped subscriber is still registered")
	}

	backlog, _, cancel2 := h.Subscribe(int64(drained))
	defer cancel2()
	if len(backlog) == 0 {
		t.Fatal("reconnect with ?from= returned nothing to catch up on")
	}
	if backlog[0].Type == EvSnapshot {
		t.Fatal("expected incremental replay, got a snapshot")
	}
}

// A long session eventually overflows the replay log. Reconnecting from a
// sequence number the log no longer covers has to degrade to a snapshot rather
// than silently hand back a partial history.
//
// This only triggers past maxLog events, which is exactly the case nobody
// reaches by hand.
func TestTruncatedLogFallsBackToASnapshot(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvRunStart, RunID: "r1"})
	for i := 0; i < maxLog+50; i++ {
		h.Publish(Event{Type: EvToolStart, CallID: fmt.Sprintf("c%d", i), Name: "read"})
	}
	if len(h.log) > maxLog {
		t.Fatalf("log holds %d events, want it capped at %d", len(h.log), maxLog)
	}

	// Sequence 1 has long since been dropped.
	backlog, _, cancel := h.Subscribe(1)
	defer cancel()
	if len(backlog) != 1 || backlog[0].Type != EvSnapshot {
		t.Fatalf("got %d frames (first %s), want a single snapshot", len(backlog), backlog[0].Type)
	}
	if backlog[0].Snapshot == nil || backlog[0].Snapshot.Seq != h.Seq() {
		t.Error("the snapshot does not carry the current sequence number")
	}

	// A client that is still inside the retained window keeps the cheap path.
	recent, _, cancel2 := h.Subscribe(h.Seq() - 5)
	defer cancel2()
	if len(recent) != 5 {
		t.Fatalf("replayed %d events, want 5", len(recent))
	}
	if recent[0].Type == EvSnapshot {
		t.Error("a client inside the window should get a replay, not a snapshot")
	}
}

// Context occupancy is the latest turn's prompt size, not the session total.
//
// Every turn resends the whole conversation, so the total grows roughly
// quadratically with turn count — a meter built on it would read several times
// the window on a perfectly healthy session. This is the one place the two
// numbers must not be confused.
func TestContextTokensTrackTheLatestTurnNotTheTotal(t *testing.T) {
	h := NewHub()
	h.SetRunInfo("k3", "kimi", 1_048_576)
	h.Publish(Event{Type: EvRunStart, RunID: "r1"})

	// Three turns of a growing conversation.
	for _, prompt := range []int64{800, 1500, 2600} {
		h.Publish(Event{Type: EvTurnStart, Turn: 1})
		h.Publish(Event{
			Type: EvMessage, Role: "assistant",
			Usage: &llm.Usage{Input: prompt, CacheRead: prompt / 2, Output: 50},
		})
	}
	// The run total, as the loop accumulates it: 4900, well past any single prompt.
	h.Publish(Event{Type: EvRunEnd, Usage: &llm.Usage{Input: 4900, Output: 150}})

	snap := h.Snapshot()
	if snap.ContextTokens != 2600 {
		t.Errorf("context_tokens = %d, want the last turn's 2600", snap.ContextTokens)
	}
	if snap.Usage.Input != 4900 {
		t.Errorf("usage.input = %d, want the running total 4900", snap.Usage.Input)
	}
	if snap.Run.ContextWindow != 1_048_576 {
		t.Errorf("context_window = %d, want the catalog value", snap.Run.ContextWindow)
	}
}

func TestSeedSplitsToolResultsOutOfHistory(t *testing.T) {
	h := NewHub()
	h.Seed([]session.Timed{
		{Message: llm.UserText("fix it")},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockText, Text: "reading"},
			{Type: llm.BlockToolUse, ID: "c1", Name: "read"},
		}}},
		{Message: llm.Message{Role: llm.RoleUser, Content: []llm.Block{
			{Type: llm.BlockToolResult, ToolUseID: "c1", Text: "file body"},
		}}},
	})

	snap := h.Snapshot()
	if len(snap.Messages) != 2 {
		t.Fatalf("messages = %d, want 2 (the result message is keyed out)", len(snap.Messages))
	}
	res, ok := snap.Results["c1"]
	if !ok {
		t.Fatal("tool result did not reach the results table")
	}
	if res.Text != "file body" || res.Name != "read" {
		t.Errorf("result = %+v, want the text and the tool name recovered", res)
	}
}

// --- Policy ------------------------------------------------------------------

func TestPolicyModeMatrix(t *testing.T) {
	cases := []struct {
		mode   Mode
		tool   string
		review bool
	}{
		{ModeStandard, "read", false},
		{ModeStandard, "edit", false},
		{ModeStandard, "bash", true},
		{ModeStrict, "edit", true},
		{ModeStrict, "read", false},
		{ModeAuto, "bash", false},
	}
	for _, c := range cases {
		p := NewPolicy()
		p.Set(c.mode, 0)
		_, auto := p.Decide(agent.GateRequest{ToolName: c.tool, Args: json.RawMessage(`{}`)})
		if auto == c.review {
			t.Errorf("%s/%s: review=%v, want %v", c.mode, c.tool, !auto, c.review)
		}
	}
}

func TestAutoTurnBudgetCoversExactlyNTurns(t *testing.T) {
	p := NewPolicy()
	p.Set(ModeAuto, 3)

	req := agent.GateRequest{ToolName: "bash", Args: json.RawMessage(`{"command":"ls"}`)}
	for turn := 1; turn <= 3; turn++ {
		if _, _, reverted := p.TurnStarted(); reverted {
			t.Fatalf("reverted at turn %d, before the budget was spent", turn)
		}
		if _, auto := p.Decide(req); !auto {
			t.Fatalf("turn %d was gated while auto was still in effect", turn)
		}
	}
	from, state, reverted := p.TurnStarted()
	if !reverted || from != ModeAuto || state.Mode != string(ModeStandard) {
		t.Fatalf("turn 4: reverted=%v from=%s to=%s, want a revert to standard", reverted, from, state.Mode)
	}
	if _, auto := p.Decide(req); auto {
		t.Error("bash still auto-allowed after the budget ran out")
	}
}

func TestExactCommandGrantDoesNotGeneralise(t *testing.T) {
	p := NewPolicy()
	p.AllowCommand("go build ./...")

	same := agent.GateRequest{ToolName: "bash", Args: json.RawMessage(`{"command":"go build ./..."}`)}
	if _, auto := p.Decide(same); !auto {
		t.Error("the exact command was not allowed")
	}
	// A prefix grant would let this through, which is the whole reason there is
	// no prefix matching.
	sneaky := agent.GateRequest{ToolName: "bash", Args: json.RawMessage(`{"command":"go build ./... ; rm -rf /"}`)}
	if _, auto := p.Decide(sneaky); auto {
		t.Error("a command that merely starts with the granted text was allowed")
	}
}

func TestDangerPatternsAreDetected(t *testing.T) {
	req := agent.GateRequest{ToolName: "bash", Args: json.RawMessage(`{"command":"rm -rf ./build"}`)}
	if hits := Danger(req); len(hits) == 0 {
		t.Fatal("rm -rf was not flagged")
	}
	safe := agent.GateRequest{ToolName: "bash", Args: json.RawMessage(`{"command":"go test ./..."}`)}
	if hits := Danger(safe); len(hits) != 0 {
		t.Errorf("go test flagged as dangerous: %v", hits)
	}
}

func TestDangerousApprovalCannotBecomeAStandingGrant(t *testing.T) {
	h := NewHub()
	p := NewPolicy()
	g := NewWebGate(h, p, time.Second)

	req := agent.GateRequest{CallID: "c1", ToolName: "bash", Args: json.RawMessage(`{"command":"rm -rf ./x"}`)}
	go func() {
		_ = g.Decide(waitForGate(t, h), Verdict{Allow: true, Remember: "command"})
	}()
	if d := g.Review(context.Background(), req); !d.Allow {
		t.Fatal("approval was not honoured")
	}
	if _, auto := p.Decide(req); auto {
		t.Error("a danger-matching approval was remembered")
	}
}

// --- Gate --------------------------------------------------------------------

func TestGateTimeoutDeniesWithAbsoluteDeadline(t *testing.T) {
	h := NewHub()
	g := NewWebGate(h, NewPolicy(), 60*time.Millisecond)

	_, ch, cancel := h.Subscribe(0)
	defer cancel()

	start := time.Now()
	d := g.Review(context.Background(), agent.GateRequest{
		CallID: "c1", ToolName: "bash", Args: json.RawMessage(`{"command":"ls"}`),
	})
	if d.Allow {
		t.Fatal("an unanswered gate must fail closed")
	}
	if !strings.Contains(d.Reason, "did not approve") {
		t.Errorf("reason = %q, want something the model can act on", d.Reason)
	}

	req := waitEvent(t, ch, EvGateRequest)
	if req.Deadline <= start.UnixMilli() {
		t.Errorf("deadline %d is not in the future; it must be absolute epoch ms", req.Deadline)
	}
	if got := waitEvent(t, ch, EvGateResolved); got.By != "timeout" {
		t.Errorf("resolved by %q, want timeout", got.By)
	}
}

func TestFreezeStopsTheClockAndThawRestoresIt(t *testing.T) {
	h := NewHub()
	g := NewWebGate(h, NewPolicy(), 120*time.Millisecond)

	_, ch, cancel := h.Subscribe(0)
	defer cancel()

	done := make(chan agent.GateDecision, 1)
	go func() {
		done <- g.Review(context.Background(), agent.GateRequest{
			CallID: "c1", ToolName: "bash", Args: json.RawMessage(`{"command":"ls"}`),
		})
	}()

	gateID := waitForGate(t, h)
	if err := g.Freeze(gateID); err != nil {
		t.Fatal(err)
	}
	// Well past the original deadline. Frozen means the clock is not running, so
	// nothing may be decided here.
	time.Sleep(250 * time.Millisecond)
	select {
	case d := <-done:
		t.Fatalf("gate resolved while frozen: %+v", d)
	default:
	}

	if err := g.Thaw(gateID); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, ch, EvGateRequest)
	if ev := waitEvent(t, ch, EvGateDeadline); ev.Deadline <= time.Now().UnixMilli() {
		t.Errorf("resumed deadline %d is already past", ev.Deadline)
	}
	select {
	case d := <-done:
		if d.Allow {
			t.Error("expected the resumed countdown to expire into a refusal")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gate never resolved after thaw")
	}
}

func TestGateSerialisesConcurrentReviews(t *testing.T) {
	h := NewHub()
	g := NewWebGate(h, NewPolicy(), 5*time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			g.Review(context.Background(), agent.GateRequest{
				CallID: fmt.Sprintf("c%d", i), ToolName: "bash",
				Args: json.RawMessage(`{"command":"ls"}`),
			})
		}(i)
	}

	// A parallel batch queues at the gate: one card at a time, so the UI never
	// has to stack them.
	answered := ""
	for i := 0; i < 4; i++ {
		id := waitForNextGate(t, h, answered)
		if n := len(h.Snapshot().Live.PendingGates); n != 1 {
			t.Fatalf("%d cards pending at once, want 1", n)
		}
		if err := g.Decide(id, Verdict{Allow: true}); err != nil {
			t.Fatal(err)
		}
		answered = id
	}
	wg.Wait()
}

// --- End to end over HTTP ----------------------------------------------------

func TestRunSurvivesTheConnectionThatStartedIt(t *testing.T) {
	h := newHarness(t, scriptedTurns(
		toolTurn("bash", `{"command":"sleep 0.5; echo finished-anyway"}`),
		textTurn("done"),
	))
	sid := h.createSession()
	h.setPolicy(sid, ModeAuto, 0)

	s1 := h.start(sid, "run it")

	// Hang up while the command is still running.
	s1.wait(t, EvToolStart)
	s1.close()

	// The dropped connection must not have taken the run with it.
	s2 := h.stream(sid, "")
	defer s2.close()
	snap := s2.next(t)
	if snap.Type != EvSnapshot {
		t.Fatalf("first frame = %s, want snapshot", snap.Type)
	}
	if snap.Snapshot.Run.Active {
		// Still going, as expected for a half-second command. Wait it out.
		if end := s2.wait(t, EvRunEnd); end.Error != "" {
			t.Fatalf("run ended with an error: %s", end.Error)
		}
	}

	final := h.session(sid).Hub().Snapshot()
	if !strings.Contains(fmt.Sprint(final.Results), "finished-anyway") {
		t.Errorf("the command did not complete; results = %+v", final.Results)
	}
	if final.Run.Active {
		t.Error("run still marked active after it ended")
	}
}

func TestPendingGateSurvivesReconnectAndLoopContinuesAfterTimeout(t *testing.T) {
	h := newHarness(t, scriptedTurns(
		toolTurn("bash", `{"command":"echo nope"}`),
		textTurn("I could not run that"),
	), func(c *Config) { c.GateTimeout = 900 * time.Millisecond })

	sid := h.createSession()
	s1 := h.start(sid, "run it")
	gate := s1.wait(t, EvGateRequest)
	if gate.Deadline <= time.Now().UnixMilli() {
		t.Fatalf("deadline %d must be absolute and in the future", gate.Deadline)
	}
	s1.close()

	// Reconnecting has to restore the card, deadline included, or a reload
	// silently loses the thing the run is blocked on.
	s2 := h.stream(sid, "")
	defer s2.close()
	snap := s2.next(t)
	if snap.Type != EvSnapshot {
		t.Fatalf("first frame = %s, want snapshot", snap.Type)
	}
	if len(snap.Snapshot.Live.PendingGates) != 1 {
		t.Fatalf("pending_gates = %+v, want the undecided card", snap.Snapshot.Live.PendingGates)
	}
	pg := snap.Snapshot.Live.PendingGates[0]
	if pg.GateID != gate.GateID || pg.Deadline != gate.Deadline {
		t.Errorf("restored card %+v does not match the original", pg)
	}
	if remaining := time.Until(time.UnixMilli(pg.Deadline)); remaining <= 0 || remaining > time.Second {
		t.Errorf("remaining time computes to %s, which is not plausible", remaining)
	}

	// Timing out refuses the call, and the loop carries on: a refusal is a tool
	// result, not the end of the run.
	if resolved := s2.wait(t, EvGateResolved); resolved.Allow || resolved.By != "timeout" {
		t.Errorf("resolved = %+v, want a timeout refusal", resolved)
	}
	if toolEnd := s2.wait(t, EvToolEnd); !toolEnd.IsError {
		t.Error("a refused call must come back as an error result")
	}
	if end := s2.wait(t, EvRunEnd); end.Error != "" {
		t.Fatalf("run ended in failure instead of continuing: %s", end.Error)
	}
	if calls := h.calls(); calls < 2 {
		t.Errorf("model was called %d times; the loop stopped instead of continuing", calls)
	}
}

func TestApprovalLetsTheCallThrough(t *testing.T) {
	h := newHarness(t, scriptedTurns(
		toolTurn("bash", `{"command":"echo approved"}`),
		textTurn("done"),
	))
	sid := h.createSession()
	s := h.start(sid, "run it")
	defer s.close()
	gate := s.wait(t, EvGateRequest)

	h.post("/api/sessions/"+sid+"/control",
		fmt.Sprintf(`{"action":"gate_decide","gate_id":%q,"allow":true}`, gate.GateID), http.StatusOK)

	toolEnd := s.wait(t, EvToolEnd)
	if toolEnd.IsError || !strings.Contains(toolEnd.Text, "approved") {
		t.Fatalf("tool result = %+v, want the command output", toolEnd)
	}
	s.wait(t, EvRunEnd)
}

func TestRewrittenArgumentsAreWhatRuns(t *testing.T) {
	h := newHarness(t, scriptedTurns(
		toolTurn("bash", `{"command":"echo original"}`),
		textTurn("done"),
	))
	sid := h.createSession()
	s := h.start(sid, "run it")
	defer s.close()
	gate := s.wait(t, EvGateRequest)

	h.post("/api/sessions/"+sid+"/control",
		fmt.Sprintf(`{"action":"gate_freeze","gate_id":%q}`, gate.GateID), http.StatusOK)
	h.post("/api/sessions/"+sid+"/control",
		fmt.Sprintf(`{"action":"gate_decide","gate_id":%q,"allow":true,"args":{"command":"echo rewritten"}}`,
			gate.GateID), http.StatusOK)

	// tool_start is emitted after review, so it must show the edited arguments
	// rather than what the model asked for.
	if start := s.wait(t, EvToolStart); !strings.Contains(string(start.Args), "rewritten") {
		t.Errorf("tool_start args = %s, want the rewrite", start.Args)
	}
	if toolEnd := s.wait(t, EvToolEnd); !strings.Contains(toolEnd.Text, "rewritten") {
		t.Errorf("output = %q, want the rewritten command's output", toolEnd.Text)
	}
}

func TestCancelReachesTheChildProcess(t *testing.T) {
	h := newHarness(t, scriptedTurns(
		toolTurn("bash", `{"command":"sleep 30"}`),
		textTurn("unreachable"),
	))
	sid := h.createSession()
	h.setPolicy(sid, ModeAuto, 0)
	s := h.start(sid, "run it")
	defer s.close()
	s.wait(t, EvToolStart)

	start := time.Now()
	h.post("/api/sessions/"+sid+"/control", `{"action":"cancel"}`, http.StatusOK)
	end := s.wait(t, EvRunEnd)

	// Cancelling has to kill the child, not just detach from it: a 30s sleep that
	// was merely abandoned would still be holding the run open here.
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("cancel took %s; the child process outlived it", elapsed)
	}
	if end.StopReason != string(llm.StopAborted) {
		t.Errorf("stop_reason = %q, want aborted", end.StopReason)
	}
	if h.session(sid).Active() {
		t.Error("session still reports an active run")
	}
}

func TestSecondRunIsRejected(t *testing.T) {
	h := newHarness(t, scriptedTurns(
		toolTurn("bash", `{"command":"sleep 0.5"}`),
		textTurn("done"),
	))
	sid := h.createSession()
	h.setPolicy(sid, ModeAuto, 0)
	h.post("/api/sessions/"+sid+"/messages", `{"prompt":"first"}`, http.StatusAccepted)
	h.post("/api/sessions/"+sid+"/messages", `{"prompt":"second"}`, http.StatusConflict)
}

func TestRewindForksTheBranchAndRebuildsEveryView(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("answer one"), textTurn("answer two")))
	sid := h.createSession()

	s1 := h.start(sid, "first question")
	s1.wait(t, EvRunEnd)
	s2 := h.start(sid, "second question")
	s2.wait(t, EvRunEnd)
	s1.close()
	s2.close()

	before := h.session(sid).Hub().Snapshot()
	if len(before.Messages) != 4 {
		t.Fatalf("before: %d messages, want u1 m1 u2 m2 = 4", len(before.Messages))
	}
	u2 := before.Messages[2]
	if u2.Role != "user" {
		t.Fatalf("messages[2].Role = %s, want the second user message", u2.Role)
	}

	// A watching client must hear about the rewind: its timeline is stale
	// past this point.
	s3 := h.stream(sid, "")
	defer s3.close()
	if first := s3.next(t); first.Type != EvSnapshot {
		t.Fatalf("first frame = %s, want snapshot", first.Type)
	}

	h.post("/api/sessions/"+sid+"/control",
		fmt.Sprintf(`{"action":"rewind","message_id":%q,"mode":"chat"}`, u2.ID), http.StatusOK)
	s3.wait(t, EvRewound)

	after := h.session(sid).Hub().Snapshot()
	if len(after.Messages) != 2 {
		t.Fatalf("after: %d messages, want u1 m1 = 2", len(after.Messages))
	}
	if got := after.Messages[0].Content[0].Text; got != "first question" {
		t.Errorf("surviving user message = %q", got)
	}

	// The store's live branch agrees, and the sidebar count with it: the
	// abandoned records are still in the file but must not be counted.
	if msgs := h.session(sid).store.Messages(""); len(msgs) != 2 {
		t.Errorf("store branch: %d messages, want 2", len(msgs))
	}
	list, err := h.mgr.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Messages != 2 {
		t.Errorf("sidebar: %+v, want one session with 2 messages", list)
	}

	// A resend continues from the fork. The replacement must survive a reload
	// from disk — which only happens if the persistence baseline moved with
	// the fork instead of re-appending or dropping the branch.
	s4 := h.start(sid, "replacement question")
	s4.wait(t, EvRunEnd)
	s4.close()
	s3.close()
	final := h.session(sid).Hub().Snapshot()
	texts := fmt.Sprint(final.Messages)
	if !strings.Contains(texts, "replacement question") || strings.Contains(texts, "second question") {
		t.Errorf("final timeline = %s", texts)
	}

	h.mgr.evictAll()
	if msgs := h.session(sid).store.Messages(""); len(msgs) != 4 {
		t.Errorf("reloaded from disk: %d messages, want 4", len(msgs))
	}
}

func TestRewindRejectsUnknownMessagesAndActiveRuns(t *testing.T) {
	h := newHarness(t, scriptedTurns(
		toolTurn("bash", `{"command":"sleep 0.5"}`),
		textTurn("done"),
	))
	sid := h.createSession()
	h.setPolicy(sid, ModeAuto, 0)

	// Unknown id, while still idle.
	h.post("/api/sessions/"+sid+"/control", `{"action":"rewind","message_id":"u99","mode":"chat"}`, http.StatusNotFound)

	s1 := h.start(sid, "run it")
	defer s1.close()
	s1.wait(t, EvToolStart)
	uid := h.session(sid).Hub().Snapshot().Messages[0].ID

	// The run is in flight: rewinding under it would orphan the loop's
	// appends, so it waits its turn with a 409.
	h.post("/api/sessions/"+sid+"/control",
		fmt.Sprintf(`{"action":"rewind","message_id":%q,"mode":"chat"}`, uid), http.StatusConflict)
	s1.wait(t, EvRunEnd)

	// Idle again, the same call forks away the only user message — the root
	// case, where the fork point is the creation meta record.
	h.post("/api/sessions/"+sid+"/control",
		fmt.Sprintf(`{"action":"rewind","message_id":%q,"mode":"chat"}`, uid), http.StatusOK)
	if got := len(h.session(sid).Hub().Snapshot().Messages); got != 0 {
		t.Errorf("after rewinding the only message: %d messages, want 0", got)
	}
}

func TestRewindRestoresTheWorkspaceToTheCheckpoint(t *testing.T) {
	writeTurn := func(path, content string) llm.Response {
		return llm.Response{
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
				{Type: llm.BlockToolUse, ID: "w_" + path, Name: "write",
					Input: json.RawMessage(fmt.Sprintf(`{"path":%q,"content":%q}`, path, content))},
			}},
			StopReason: llm.StopToolUse,
		}
	}
	h := newHarness(t, scriptedTurns(
		textTurn("one"),
		writeTurn("x.txt", "v2"),
		writeTurn("y.txt", "new"),
		textTurn("two"),
	))
	sid := h.createSession()
	h.setPolicy(sid, ModeAuto, 0)

	// The workspace as it was before anything ran.
	ws := h.mgr.cfg.Cwd
	if err := os.WriteFile(filepath.Join(ws, "x.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	s1 := h.start(sid, "first")
	s1.wait(t, EvRunEnd)
	s1.close()
	s2 := h.start(sid, "second")
	s2.wait(t, EvRunEnd)
	s2.close()

	// Sanity: run two's writes landed.
	if got, _ := os.ReadFile(filepath.Join(ws, "x.txt")); string(got) != "v2" {
		t.Fatalf("x.txt = %q, want v2 before the rewind", got)
	}
	if _, err := os.Stat(filepath.Join(ws, "y.txt")); err != nil {
		t.Fatalf("y.txt must exist before the rewind: %v", err)
	}

	u2 := h.session(sid).Hub().Snapshot().Messages[2]

	// The preview names what a restore would touch, without touching it.
	resp := h.do(http.MethodPost, "/api/sessions/"+sid+"/control",
		fmt.Sprintf(`{"action":"rewind_preview","message_id":%q}`, u2.ID))
	var preview struct {
		Available bool         `json:"available"`
		Changes   []FileChange `json:"changes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !preview.Available {
		t.Fatal("no checkpoint for the second message")
	}
	set := make(map[string]string, len(preview.Changes))
	for _, c := range preview.Changes {
		set[c.Path] = c.Status
	}
	if set["x.txt"] != "M" || set["y.txt"] != "A" {
		t.Errorf("preview = %v, want x.txt M and y.txt A", set)
	}
	// Previewing changed nothing.
	if got, _ := os.ReadFile(filepath.Join(ws, "x.txt")); string(got) != "v2" {
		t.Fatalf("preview touched x.txt: %q", got)
	}

	// Now the rewind with files: the conversation forks and the workspace
	// follows — x.txt back to v1, the file the abandoned run created gone.
	h.post("/api/sessions/"+sid+"/control",
		fmt.Sprintf(`{"action":"rewind","message_id":%q,"mode":"both"}`, u2.ID), http.StatusOK)

	if got := len(h.session(sid).Hub().Snapshot().Messages); got != 2 {
		t.Errorf("messages = %d, want 2", got)
	}
	if got, _ := os.ReadFile(filepath.Join(ws, "x.txt")); string(got) != "v1" {
		t.Errorf("x.txt = %q, want v1 restored", got)
	}
	if _, err := os.Stat(filepath.Join(ws, "y.txt")); !os.IsNotExist(err) {
		t.Error("y.txt must be gone after the restore")
	}

	// And the conversation-only path is still there: rewinding the remaining
	// message without files leaves the workspace as restored.
	u1 := h.session(sid).Hub().Snapshot().Messages[0]
	h.post("/api/sessions/"+sid+"/control",
		fmt.Sprintf(`{"action":"rewind","message_id":%q,"mode":"chat"}`, u1.ID), http.StatusOK)
	if got, _ := os.ReadFile(filepath.Join(ws, "x.txt")); string(got) != "v1" {
		t.Errorf("conversation-only rewind touched x.txt: %q", got)
	}
}

func TestAuthAndOriginAreEnforcedIncludingTheStream(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("hi")))
	sid := h.createSession()

	for _, path := range []string{"/api/sessions", "/api/sessions/" + sid + "/stream"} {
		req, _ := http.NewRequest(http.MethodGet, h.srv.URL+path, nil)
		resp, err := h.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without a token: %d, want 401", path, resp.StatusCode)
		}
	}

	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin request: %d, want 403", resp.StatusCode)
	}
}

// The page and its bundle must load without a token, and everything that can
// act must not.
//
// This rule is fragile in both directions and worth pinning down. Requiring a
// token for assets produces a page that 401s against its own script tags — a
// browser cannot attach a header to a <script src> it discovered in HTML.
// Exempting more than the assets would hand the API to anyone who can reach the
// port, and pi-go's bash tool has no path restriction.
func TestStaticAssetsLoadWithoutATokenButTheAPIDoesNot(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("hi")))

	page := h.get("/")
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if page.StatusCode != http.StatusOK {
		t.Fatalf("GET / without a token: %d, want 200", page.StatusCode)
	}

	// Find whatever the current build named its bundle rather than hardcoding a
	// hash that changes on every front-end change.
	asset := regexp.MustCompile(`/assets/[A-Za-z0-9._-]+`).FindString(string(body))
	if asset == "" {
		t.Skip("front end not built into this binary; nothing to serve")
	}
	res := h.get(asset)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("GET %s without a token: %d, want 200", asset, res.StatusCode)
	}

	for _, path := range []string{"/api/models", "/api/sessions"} {
		res := h.get(path)
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without a token: %d, want 401", path, res.StatusCode)
		}
	}
}

func TestSlashCommandsCannotEnterTheTranscript(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("hi")))
	sid := h.createSession()
	h.post("/api/sessions/"+sid+"/messages", `{"prompt":"/auto"}`, http.StatusBadRequest)
	// A prompt that merely begins with a slash is still a prompt.
	h.post("/api/sessions/"+sid+"/messages", `{"prompt":"/usr/local/bin is on PATH?"}`, http.StatusAccepted)
}

func TestPolicyIsServerStateAndTravelsInTheSnapshot(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("hi")))
	sid := h.createSession()
	h.setPolicy(sid, ModeAuto, 3)

	s := h.stream(sid, "")
	defer s.close()
	snap := s.next(t)
	if snap.Snapshot.Policy.Mode != "auto" || snap.Snapshot.Policy.RemainingTurns != 3 {
		t.Errorf("policy = %+v, want auto with 3 turns left", snap.Snapshot.Policy)
	}
}

func TestSessionSurvivesAProcessRestartThroughItsFile(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("remembered")))
	sid := h.createSession()
	s := h.start(sid, "say something")
	s.wait(t, EvRunEnd)
	s.close()

	// Drop it from memory the way idle eviction does, then load it again: the
	// transcript has to come back from the JSONL file, results and all.
	h.mgr.evictAll()
	snap := h.session(sid).Hub().Snapshot()
	if len(snap.Messages) < 2 {
		t.Fatalf("reloaded %d messages, want the prompt and the answer", len(snap.Messages))
	}
	if snap.Run.Active {
		t.Error("a reloaded session must not look busy")
	}
}

// The cost total lives in the transcript's stats records; a reload has to
// restore it, or every restart reads as a session that spent nothing. And the
// restored total becomes the recorded baseline: the next run must append only
// its own delta, not history a second time.
func TestUsageSurvivesARestartThroughTheStatsRecords(t *testing.T) {
	turn := textTurn("hi")
	turn.Usage = llm.Usage{Input: 1200, Output: 300, CacheRead: 600}
	h := newHarness(t, scriptedTurns(turn))
	sid := h.createSession()
	s := h.start(sid, "hi")
	s.wait(t, EvRunEnd)
	s.close()

	before := h.session(sid).Hub().Snapshot().Usage
	if before.Input != 1200 || before.CacheRead != 600 {
		t.Fatalf("usage before eviction = %+v, want the run's 1200/600", before)
	}

	h.mgr.evictAll()
	after := h.session(sid).Hub().Snapshot().Usage
	if after != before {
		t.Fatalf("usage after reopening = %+v, want it restored to %+v", after, before)
	}

	// A second run adds exactly its own 1200: no double count.
	s2 := h.start(sid, "again")
	s2.wait(t, EvRunEnd)
	s2.close()
	h.mgr.evictAll()
	if got := h.session(sid).Hub().Snapshot().Usage.Input; got != 2400 {
		t.Errorf("usage after a second run = %d, want 2400", got)
	}
}

// A measurement that is computed but never written is worse than none: the report
// prints zeroes and they read as a fact about the session. That has happened here
// before — the whole Token Usage section existed, with a parser, and nothing
// produced a stats record — so the write path gets its own assertion rather than
// being assumed from the fact that Compose has unit tests.
func TestCompositionReachesTheTranscript(t *testing.T) {
	turn := textTurn("done")
	turn.Usage = llm.Usage{Input: 4321, Output: 100}
	h := newHarness(t, scriptedTurns(turn))
	sid := h.createSession()
	s := h.start(sid, strings.Repeat("q", 400))
	s.wait(t, EvRunEnd)
	s.close()

	stats, err := analyze.AnalyzeSession(h.session(sid).store.Path(), analyze.Config{})
	if err != nil {
		t.Fatal(err)
	}
	c := stats.Composition
	if c == nil {
		t.Fatal("the run wrote no composition")
	}
	// The fixed overhead is not in the messages, so a zero here means the caller
	// forgot to pass it — the one part of this that a pure-function test cannot see.
	if c.Fixed <= 0 {
		t.Errorf("Fixed = %d: the system prompt and tool schemas were not counted", c.Fixed)
	}
	// The user's 400 bytes of prompt have to show up as conversation.
	if c.User <= 0 {
		t.Errorf("User = %d, want the prompt counted", c.User)
	}
	// And the provider's own number has to be carried across, or the calibration
	// that justifies every share here is unavailable.
	if c.Measured != 4321 {
		t.Errorf("Measured = %d, want the scripted 4321", c.Measured)
	}
	if _, ok := c.Calibration(); !ok {
		t.Error("no calibration despite a measured turn")
	}
}

// --- harness -----------------------------------------------------------------

type harness struct {
	t     *testing.T
	mgr   *Manager
	srv   *httptest.Server
	token string

	mu    sync.Mutex
	turns int
}

// newHarness wires a Manager and Server around a scripted model, so a whole run
// can be driven without a network. That is the only way to verify the two things
// this layer exists for: that a run outlives its connection, and that an
// unanswered approval becomes a tool result instead of a dead loop.
func newHarness(t *testing.T, respond func(int) (llm.Response, error), tweaks ...func(*Config)) *harness {
	t.Helper()
	t.Setenv("KIMI_API_KEY", "test-key")

	h := &harness{t: t, token: "test-token"}
	cfg := Config{
		Cwd:         t.TempDir(),
		SessionDir:  t.TempDir(),
		Model:       "k3",
		MaxTurns:    5,
		GateTimeout: 5 * time.Second,
		RunTimeout:  time.Minute,
		IdleTimeout: time.Hour,
		NewClient: func(c config.Resolved, _ func(llm.RetryInfo)) llm.Client {
			return &scriptedClient{model: c.Model, respond: func() (llm.Response, error) {
				h.mu.Lock()
				h.turns++
				n := h.turns
				h.mu.Unlock()
				return respond(n)
			}}
		},
	}
	for _, tweak := range tweaks {
		tweak(&cfg)
	}

	mgr, err := NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.mgr = mgr
	t.Cleanup(mgr.Close)

	srv, err := NewServer(mgr, ServerOptions{Token: h.token})
	if err != nil {
		t.Fatal(err)
	}
	h.srv = httptest.NewServer(srv.Handler())
	t.Cleanup(h.srv.Close)
	return h
}

func (h *harness) calls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.turns
}

func (h *harness) session(sid string) *Session {
	h.t.Helper()
	s, err := h.mgr.Get(sid)
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

func (h *harness) createSession() string {
	h.t.Helper()
	resp := h.do(http.MethodPost, "/api/sessions", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		h.t.Fatalf("create session: %d", resp.StatusCode)
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatal(err)
	}
	return out.SessionID
}

// start subscribes before sending the prompt, so a test is never racing the run
// it is about to watch. A real client can do either, because a snapshot carries
// whatever it missed.
func (h *harness) start(sid, prompt string) *sseStream {
	h.t.Helper()
	s := h.stream(sid, "")
	if first := s.next(h.t); first.Type != EvSnapshot {
		h.t.Fatalf("first frame = %s, want snapshot", first.Type)
	}
	h.post("/api/sessions/"+sid+"/messages",
		fmt.Sprintf(`{"prompt":%q}`, prompt), http.StatusAccepted)
	return s
}

func (h *harness) setPolicy(sid string, mode Mode, turns int) {
	h.t.Helper()
	h.post("/api/sessions/"+sid+"/control",
		fmt.Sprintf(`{"action":"set_policy","mode":%q,"turns":%d}`, mode, turns), http.StatusOK)
}

func (h *harness) post(path, body string, want int) {
	h.t.Helper()
	resp := h.do(http.MethodPost, path, body)
	defer resp.Body.Close()
	if resp.StatusCode != want {
		msg, _ := io.ReadAll(resp.Body)
		h.t.Fatalf("POST %s: %d, want %d (%s)", path, resp.StatusCode, want, bytes.TrimSpace(msg))
	}
}

// get issues a request with no credentials at all, the way a browser fetches the
// assets it found in the page.
func (h *harness) get(path string) *http.Response {
	h.t.Helper()
	res, err := h.srv.Client().Get(h.srv.URL + path)
	if err != nil {
		h.t.Fatal(err)
	}
	return res
}

func (h *harness) do(method, path, body string) *http.Response {
	h.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, r)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

// sseStream reads frames in the background so a test can wait for one with a
// deadline instead of blocking on a socket.
type sseStream struct {
	resp   *http.Response
	frames chan Event
}

func (h *harness) stream(sid, from string) *sseStream {
	h.t.Helper()
	path := "/api/sessions/" + sid + "/stream"
	if from != "" {
		path += "?from=" + from
	}
	resp := h.do(http.MethodGet, path, "")
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("stream: %d", resp.StatusCode)
	}

	// An SSE handler only returns when its client goes away, so a stream left
	// open by a failing assertion would block httptest.Server.Close forever.
	h.t.Cleanup(func() { resp.Body.Close() })

	s := &sseStream{resp: resp, frames: make(chan Event, 512)}
	go func() {
		defer close(s.frames)
		r := bufio.NewReader(resp.Body)
		for {
			e, ok, err := readFrame(r)
			if err != nil {
				return
			}
			if !ok {
				continue // keepalive comment
			}
			s.frames <- e
		}
	}()
	return s
}

func (s *sseStream) close() { s.resp.Body.Close() }

func (s *sseStream) next(t *testing.T) Event {
	t.Helper()
	select {
	case e, open := <-s.frames:
		if !open {
			t.Fatal("stream ended")
		}
		return e
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for a frame")
		return Event{}
	}
}

func (s *sseStream) wait(t *testing.T, want EventType) Event {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case e, open := <-s.frames:
			if !open {
				t.Fatalf("stream ended before %s arrived", want)
			}
			if e.Type == want {
				return e
			}
			if e.Type == EvRunEnd && want != EvRunEnd {
				t.Fatalf("run ended before %s arrived (error: %q)", want, e.Error)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", want)
		}
	}
}

// readFrame parses one SSE frame. The bool is false for a keepalive comment.
func readFrame(r *bufio.Reader) (Event, bool, error) {
	var data string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return Event{}, false, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if payload, ok := strings.CutPrefix(line, "data: "); ok {
			data += payload
		}
	}
	if data == "" {
		return Event{}, false, nil
	}
	var e Event
	if err := json.Unmarshal([]byte(data), &e); err != nil {
		return Event{}, false, err
	}
	return e, true, nil
}

func waitEvent(t *testing.T, ch <-chan Event, want EventType) Event {
	t.Helper()
	for {
		select {
		case e, open := <-ch:
			if !open {
				t.Fatalf("hub closed before %s", want)
			}
			if e.Type == want {
				return e
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", want)
		}
	}
}

// waitForGate polls the hub state for an open approval, which is what a browser
// does by reading the snapshot.
func waitForGate(t *testing.T, h *Hub) string {
	t.Helper()
	return waitForNextGate(t, h, "")
}

// waitForNextGate skips a card that was already answered: resolution is published
// by the reviewer goroutine, so it can still be listed for an instant after the
// verdict was accepted.
func waitForNextGate(t *testing.T, h *Hub, answered string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if gates := h.Snapshot().Live.PendingGates; len(gates) > 0 && gates[0].GateID != answered {
			return gates[0].GateID
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("no approval appeared")
	return ""
}

// --- scripted model ----------------------------------------------------------

type scriptedClient struct {
	model   string
	respond func() (llm.Response, error)
}

func (c *scriptedClient) Model() string { return c.model }

func (c *scriptedClient) Stream(
	ctx context.Context, _ string, _ []llm.Message, _ []llm.ToolSchema, onDelta func(llm.Delta),
) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{StopReason: llm.StopAborted}, nil
	}
	resp, err := c.respond()
	if err != nil {
		return llm.Response{}, err
	}
	for _, b := range resp.Message.Content {
		if b.Type == llm.BlockText && onDelta != nil {
			onDelta(llm.Delta{Kind: llm.DeltaText, Text: b.Text})
		}
	}
	return resp, nil
}

// scriptedTurns replays one response per model call, repeating the last one if
// the loop asks for more.
func scriptedTurns(turns ...llm.Response) func(int) (llm.Response, error) {
	return func(n int) (llm.Response, error) {
		if n > len(turns) {
			n = len(turns)
		}
		return turns[n-1], nil
	}
}

func toolTurn(name, args string) llm.Response {
	return llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockToolUse, ID: "call_" + name, Name: name, Input: json.RawMessage(args)},
		}},
		StopReason: llm.StopToolUse,
	}
}

func textTurn(text string) llm.Response {
	return llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockText, Text: text},
		}},
		StopReason: llm.StopEndTurn,
	}
}

// --- skills ------------------------------------------------------------------

func writeTestSkill(t *testing.T, dir, name, description, body string) skills.Skill {
	t.Helper()
	sdir := filepath.Join(dir, name)
	if err := os.MkdirAll(sdir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sdir, "SKILL.md")
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return skills.Skill{Name: name, Description: description, Path: path, Dir: sdir, Source: "user"}
}

func TestSkillsEndpointListsWhatTheModelSees(t *testing.T) {
	dir := t.TempDir()
	sk := writeTestSkill(t, dir, "demo", "does a demo thing", "# Demo")
	h := newHarness(t, scriptedTurns(textTurn("ok")), func(c *Config) { c.Skills = []skills.Skill{sk} })

	resp := h.do(http.MethodGet, "/api/skills", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/skills: %d", resp.StatusCode)
	}
	var out struct {
		Skills []struct {
			Name, Description, Path string
		} `json:"skills"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Skills) != 1 || out.Skills[0].Name != "demo" {
		t.Fatalf("got %+v", out.Skills)
	}
	// The path is in the system prompt already; hiding it from the browser would
	// leave the user knowing less than the model.
	if out.Skills[0].Path != sk.Path {
		t.Errorf("path = %q, want %q", out.Skills[0].Path, sk.Path)
	}
}

// /skill:name is expanded on the server, because that is where the file is
// readable, and the expansion is what enters the transcript.
func TestSkillCommandExpandsIntoThePrompt(t *testing.T) {
	dir := t.TempDir()
	sk := writeTestSkill(t, dir, "demo", "d", "# Demo\n\nrun ./go.sh")
	h := newHarness(t, scriptedTurns(textTurn("ok")), func(c *Config) { c.Skills = []skills.Skill{sk} })

	sid := h.createSession()
	s := h.start(sid, "/skill:demo extract page 3")
	s.wait(t, EvRunEnd)
	s.close()

	snap := h.session(sid).Hub().Snapshot()
	var user string
	for _, m := range snap.Messages {
		if m.Role == "user" {
			for _, b := range m.Content {
				user += b.Text
			}
		}
	}
	for _, want := range []string{`<skill name="demo"`, "run ./go.sh", "extract page 3"} {
		if !strings.Contains(user, want) {
			t.Errorf("missing %q in the submitted prompt:\n%s", want, user)
		}
	}
	if strings.Contains(user, "description: d") {
		t.Error("frontmatter leaked into the transcript")
	}
}

func TestUnknownSkillCommandIsRejected(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("ok")))
	sid := h.createSession()
	h.post("/api/sessions/"+sid+"/messages", `{"prompt":"/skill:nope"}`, http.StatusBadRequest)
	if h.calls() != 0 {
		t.Error("a rejected skill command must not start a run")
	}
}

// ls is read-only and confined by the path guard, so gating it would only teach
// the habit of clicking approve.
func TestLsIsNotGatedInStandardMode(t *testing.T) {
	p := NewPolicy()
	// Every read-only tool. A search that costs an approval click is a search
	// people route through bash instead, which is the opposite of the point.
	for _, tool := range []string{"read", "ls", "find", "grep"} {
		if _, auto := p.Decide(agent.GateRequest{ToolName: tool}); !auto {
			t.Errorf("%s should not be reviewed in standard mode", tool)
		}
	}
	if _, auto := p.Decide(agent.GateRequest{ToolName: "bash", Args: []byte(`{"command":"ls"}`)}); auto {
		t.Error("bash must still be reviewed, even when the command happens to be ls")
	}
}

// The diff is the thing worth looking at after the fact, so it has to come back
// from the session file rather than existing only in the live event stream. Before
// details were carried in the transcript, a reload turned every edit into its one
// line of result text and the diff view had nothing to render.
//
// This goes through the real edit tool and a real file: a fake payload would prove
// the plumbing carries whatever it is handed, not that a diff arrives.
func TestToolDetailsSurviveAReloadFromDisk(t *testing.T) {
	var cwd string
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			return toolTurn("edit", `{"path":"f.txt","edits":[{"oldText":"before","newText":"after"}]}`), nil
		}
		return textTurn("changed it"), nil
	}, func(c *Config) {
		cwd = c.Cwd
	})
	if err := os.WriteFile(filepath.Join(cwd, "f.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sid := h.createSession()
	h.setPolicy(sid, ModeAuto, 0)
	s := h.start(sid, "make the edit")
	s.wait(t, EvRunEnd)
	s.close()

	live := h.session(sid).Hub().Snapshot()
	liveDiff := diffFromResults(t, live.Results)
	if liveDiff == "" {
		t.Fatal("the live snapshot has no diff, so this test cannot prove anything about the reloaded one")
	}

	// Drop it from memory the way idle eviction does and load it back from JSONL.
	h.mgr.evictAll()
	reloaded := h.session(sid).Hub().Snapshot()
	reloadedDiff := diffFromResults(t, reloaded.Results)

	if reloadedDiff != liveDiff {
		t.Errorf("diff after reload:\n%q\nwant the live one:\n%q", reloadedDiff, liveDiff)
	}
	if !strings.Contains(reloadedDiff, "before") || !strings.Contains(reloadedDiff, "after") {
		t.Errorf("the reloaded diff does not describe the change: %q", reloadedDiff)
	}
}

// diffFromResults digs the rendered diff out of whichever tool result carries one.
// The details arrive as a concrete struct live and as raw JSON after a reload, and
// the point of the test is that both hold the same thing, so it goes through JSON
// in both cases.
func diffFromResults(t *testing.T, results map[string]ToolResult) string {
	t.Helper()
	for _, r := range results {
		if r.Details == nil {
			continue
		}
		raw, err := json.Marshal(r.Details)
		if err != nil {
			t.Fatalf("details for %s will not marshal: %v", r.CallID, err)
		}
		var d struct {
			Diff string `json:"diff"`
		}
		if json.Unmarshal(raw, &d) == nil && d.Diff != "" {
			return d.Diff
		}
	}
	return ""
}

// --- steering -----------------------------------------------------------------

// Steering is the answer to "it is going the wrong way": before this, the only
// options were to cancel and lose the turn, or wait it out.
func TestSteerJoinsTheRunInFlightAndAppearsOnTheTimeline(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			// Hold the first turn open so the steering request is unambiguously
			// mid-run rather than racing the end of it.
			<-release
			return toolTurn("bash", `{"command":"echo one"}`), nil
		}
		return textTurn("adjusted"), nil
	})

	sid := h.createSession()
	h.setPolicy(sid, ModeAuto, 0)
	s := h.start(sid, "do the first thing")

	h.post("/api/sessions/"+sid+"/control",
		`{"action":"steer","prompt":"actually, do it the other way"}`, http.StatusOK)
	close(release)
	s.wait(t, EvRunEnd)
	s.close()

	// One run, not two: the message joined the work already in progress.
	if got := h.calls(); got != 2 {
		t.Errorf("model called %d times, want 2 turns in a single run", got)
	}

	snap := h.session(sid).Hub().Snapshot()
	var asked []string
	for _, m := range snap.Messages {
		if m.Role != "user" {
			continue
		}
		for _, b := range m.Content {
			if b.Text != "" {
				asked = append(asked, b.Text)
			}
		}
	}
	if len(asked) != 2 || asked[1] != "actually, do it the other way" {
		t.Errorf("user messages on the timeline = %v, want the prompt then the steering message", asked)
	}
}

// With no run in flight there is nothing to join. That is not an error: the
// client's next move is to send it as an ordinary message, and a status code
// would make a normal race look like a failure.
func TestSteerWithNoRunReportsFalseRatherThanFailing(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("hi")))
	sid := h.createSession()

	resp := h.do(http.MethodPost, "/api/sessions/"+sid+"/control",
		`{"action":"steer","prompt":"nobody is listening"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Steered bool `json:"steered"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Steered {
		t.Error("steered = true with no run in flight")
	}
	if h.calls() != 0 {
		t.Error("a refused steering message started a run")
	}
}

// The same reasoning as for /messages: a slash command must never become a
// prompt, and a steering message is the easier of the two to sneak one into.
func TestSteerRejectsSlashCommandsAndEmptyText(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("hi")))
	sid := h.createSession()
	for _, body := range []string{
		`{"action":"steer","prompt":"/auto"}`,
		`{"action":"steer","prompt":"   "}`,
	} {
		h.post("/api/sessions/"+sid+"/control", body, http.StatusBadRequest)
	}
}

// Fragments must stay out of the replay log. Logging them would tie memory to how
// much a command printed rather than to the size of the transcript, which is the
// property the whole replay design rests on.
func TestToolPartialIsNotLogged(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvRunStart, RunID: "r1"})
	h.Publish(Event{Type: EvToolStart, CallID: "c1", Name: "bash"})
	before := h.Seq()
	for i := 0; i < 500; i++ {
		h.Publish(Event{Type: EvToolPartial, CallID: "c1", Text: "chunk\n"})
	}

	// Sequence numbers still advance, so a live subscriber receives every fragment.
	if h.Seq() <= before {
		t.Error("fragments were not published at all")
	}
	// But a reconnect from before them must not replay them.
	backlog, _, cancel := h.Subscribe(before)
	defer cancel()
	for _, e := range backlog {
		if e.Type == EvToolPartial {
			t.Fatal("a fragment was replayed from the log")
		}
	}
}

// A client that connects mid-command has nothing to replay, so the snapshot is the
// only way it can see what the command has printed.
func TestSnapshotCarriesPartialOutputOfARunningCall(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvRunStart, RunID: "r1"})
	h.Publish(Event{Type: EvToolStart, CallID: "c1", Name: "bash"})
	h.Publish(Event{Type: EvToolPartial, CallID: "c1", Text: "step 1\n"})
	h.Publish(Event{Type: EvToolPartial, CallID: "c1", Text: "step 2\n"})

	snap := h.Snapshot()
	if len(snap.Live.PendingTools) != 1 {
		t.Fatalf("pending tools = %d, want 1", len(snap.Live.PendingTools))
	}
	if got := snap.Live.PendingTools[0].Output; got != "step 1\nstep 2\n" {
		t.Errorf("accumulated output = %q", got)
	}

	// Once the call settles, the live copy goes away and the settled result stands
	// on its own — otherwise the same output would render twice.
	h.Publish(Event{Type: EvToolEnd, CallID: "c1", Name: "bash", Text: "step 1\nstep 2\n"})
	snap = h.Snapshot()
	if len(snap.Live.PendingTools) != 0 {
		t.Errorf("pending tools = %d after tool_end, want 0", len(snap.Live.PendingTools))
	}
	if snap.Results["c1"].Text == "" {
		t.Error("the settled result did not land")
	}
}

// The live copy is bounded, because it is included in every snapshot: without a
// cap, reconnecting would get more expensive the longer a command ran.
func TestPartialOutputInTheSnapshotIsBounded(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvToolStart, CallID: "c1", Name: "bash"})
	for i := 0; i < 200; i++ {
		h.Publish(Event{Type: EvToolPartial, CallID: "c1", Text: strings.Repeat("x", 1024) + "\n"})
	}

	got := h.Snapshot().Live.PendingTools[0].Output
	if len(got) > maxPendingOutput {
		t.Errorf("kept %d bytes, want at most %d", len(got), maxPendingOutput)
	}
	// The tail is what matters: the last lines say how the command is going.
	if !strings.HasSuffix(got, "\n") {
		t.Error("the tail should end on a line boundary")
	}
}

// A fragment for a call that is not running must be ignored rather than create a
// phantom entry. Ordering across the gate and loop paths is not guaranteed, so
// this is reachable rather than theoretical.
func TestPartialForAnUnknownCallIsIgnored(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvToolPartial, CallID: "ghost", Text: "hello"})
	if n := len(h.Snapshot().Live.PendingTools); n != 0 {
		t.Errorf("pending tools = %d, want 0", n)
	}
}

// The browser and the terminal describe the same catalogue, so a field one of them
// shows and the other does not is drift waiting to happen. subagent_model in
// particular changes what runs without anyone naming it at a prompt.
func TestModelsEndpointDescribesTheWholeCatalogue(t *testing.T) {
	// The harness resolves a real model when it starts, so the catalogue is swapped
	// afterwards. The endpoint reads it per request, which is the behaviour being
	// tested: it describes the configuration in effect now, not at startup.
	h := newHarness(t, scriptedTurns(textTurn("hi")))

	savedM := config.Catalog()
	t.Cleanup(func() { config.SetCatalogForTest(savedM) })
	config.SetCatalogForTest([]config.Model{
		{ID: "big", Provider: "kimi", Aliases: []string{"b"},
			ContextWindow: 200_000, SubagentModel: "small"},
		{ID: "small", Provider: "kimi", ContextWindow: 32_000},
		{ID: "elsewhere", Provider: "zhipu", ContextWindow: 1000},
	})
	t.Setenv("KIMI_API_KEY", "x")
	t.Setenv("ZHIPU_API_KEY", "")

	res := h.do(http.MethodGet, "/api/models", "")
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/models: %d", res.StatusCode)
	}
	var got struct {
		Models []struct {
			ID            string `json:"id"`
			Configured    bool   `json:"configured"`
			SubagentModel string `json:"subagent_model"`
			KeyEnv        string `json:"key_env"`
		} `json:"models"`
		Default string `json:"default"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 3 || got.Default != "big" {
		t.Fatalf("models = %+v, default = %q", got.Models, got.Default)
	}
	if got.Models[0].SubagentModel != "small" {
		t.Errorf("subagent_model = %q, want it reported", got.Models[0].SubagentModel)
	}
	if got.Models[1].SubagentModel != "" {
		t.Errorf("a model with no mapping reported %q", got.Models[1].SubagentModel)
	}
	// A configured model needs no hint; an unconfigured one needs to say which
	// variable to set, because unlike the terminal the browser cannot print its own.
	if got.Models[0].KeyEnv != "" {
		t.Errorf("a configured model carries key_env %q", got.Models[0].KeyEnv)
	}
	if got.Models[2].Configured || got.Models[2].KeyEnv != "ZHIPU_API_KEY" {
		t.Errorf("unconfigured model = %+v, want configured=false and the variable named",
			got.Models[2])
	}
}

// Frames are not logged, for the same reason output fragments are not: a delegation
// that produced hundreds of them would make every reconnect more expensive. So the
// hub folds them into the pending call, which is the only thing a browser joining
// mid-delegation has to go on.
func TestSubagentFramesSurviveAReconnect(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvRunStart, RunID: "r1"})
	h.Publish(Event{Type: EvToolStart, CallID: "c1", Name: "subagent"})
	for _, f := range []string{
		`{"type":"session","session":"/tmp/child.jsonl"}`,
		`{"type":"turn_start","turn":1}`,
		`{"type":"tool_start","name":"read"}`,
	} {
		h.Publish(Event{Type: EvToolPartial, CallID: "c1", Frame: json.RawMessage(f)})
	}

	pending := h.Snapshot().Live.PendingTools
	if len(pending) != 1 {
		t.Fatalf("pending tools = %d, want 1", len(pending))
	}
	if got := len(pending[0].Frames); got != 3 {
		t.Fatalf("frames = %d, want 3", got)
	}
	// Order matters: this is a timeline, and the card shows the latest one.
	var last struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(pending[0].Frames[2], &last); err != nil || last.Name != "read" {
		t.Errorf("last frame = %s, want the read call", pending[0].Frames[2])
	}
	// Still not logged, so replay stays proportional to the transcript rather than to
	// how busy a delegation was.
	backlog, _, cancel := h.Subscribe(0)
	defer cancel()
	for _, e := range backlog {
		if e.Type == EvToolPartial {
			t.Fatal("a frame was replayed from the log")
		}
	}
	// And they go away with the call, or a finished delegation would keep paying for
	// its own progress.
	h.Publish(Event{Type: EvToolEnd, CallID: "c1", Name: "subagent", Text: "done"})
	if n := len(h.Snapshot().Live.PendingTools); n != 0 {
		t.Errorf("pending tools = %d after tool_end, want 0", n)
	}
}

// Bounded by count rather than bytes, because a frame is a whole event: dropping
// the oldest costs one line of history, while cutting bytes would hand the client
// something it cannot parse.
func TestPendingFramesAreBoundedByCount(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvRunStart, RunID: "r1"})
	h.Publish(Event{Type: EvToolStart, CallID: "c1", Name: "subagent"})
	for i := 0; i < maxPendingFrames+50; i++ {
		h.Publish(Event{Type: EvToolPartial, CallID: "c1",
			Frame: json.RawMessage(fmt.Sprintf(`{"type":"turn_start","turn":%d}`, i))})
	}

	frames := h.Snapshot().Live.PendingTools[0].Frames
	if len(frames) != maxPendingFrames {
		t.Fatalf("kept %d frames, want %d", len(frames), maxPendingFrames)
	}
	// The tail is what is kept: a subagent that produced hundreds of events is
	// looping, and the recent ones are the interesting ones.
	var first, last struct {
		Turn int `json:"turn"`
	}
	_ = json.Unmarshal(frames[0], &first)
	_ = json.Unmarshal(frames[len(frames)-1], &last)
	if last.Turn != maxPendingFrames+49 {
		t.Errorf("last frame turn = %d, want the newest", last.Turn)
	}
	if first.Turn != 50 {
		t.Errorf("first frame turn = %d, want the oldest 50 dropped", first.Turn)
	}
	// Every kept frame is still parseable, which byte-based trimming would break.
	for _, f := range frames {
		if !json.Valid(f) {
			t.Fatalf("kept an unparseable frame: %s", f)
		}
	}
}

// Argument fragments are transient stream state, exactly like tool_partial:
// fanned out live, folded into the live state, never stored. Logging them would
// tie memory to how much a model streamed rather than to the transcript size.
func TestToolArgsIsNotLogged(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvRunStart, RunID: "r1"})
	h.Publish(Event{Type: EvToolArgs, CallID: "c1", Name: "write"})
	before := h.Seq()
	for i := 0; i < 500; i++ {
		h.Publish(Event{Type: EvToolArgs, CallID: "c1", Text: `"content":"line`})
	}

	// Sequence numbers still advance, so a live subscriber receives every fragment.
	if h.Seq() <= before {
		t.Error("fragments were not published at all")
	}
	// But a reconnect from before them must not replay them.
	backlog, _, cancel := h.Subscribe(before)
	defer cancel()
	for _, e := range backlog {
		if e.Type == EvToolArgs {
			t.Fatal("an argument fragment was replayed from the log")
		}
	}
}

// A client that connects mid-generation has nothing to replay, so the snapshot
// is the only way it can see the in-progress write: name, size, and preview.
func TestSnapshotCarriesAnIncomingCallMidStream(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvRunStart, RunID: "r1"})
	h.Publish(Event{Type: EvToolArgs, CallID: "c1", Name: "write"})
	h.Publish(Event{Type: EvToolArgs, CallID: "c1", Text: `{"path":"a.go",`})
	h.Publish(Event{Type: EvToolArgs, CallID: "c1", Text: `"content":"hello\n"`})
	h.Publish(Event{Type: EvToolArgs, CallID: "c1", Text: `}`})

	snap := h.Snapshot()
	if len(snap.Live.Incoming) != 1 {
		t.Fatalf("incoming calls = %d, want 1", len(snap.Live.Incoming))
	}
	inc := snap.Live.Incoming[0]
	if inc.CallID != "c1" || inc.Name != "write" {
		t.Errorf("incoming call not attributed: %+v", inc)
	}
	if want := len(`{"path":"a.go","content":"hello\n"}`); inc.Bytes != want {
		t.Errorf("bytes = %d, want %d", inc.Bytes, want)
	}
	if inc.TS == 0 {
		t.Error("the entry has no timestamp")
	}
	// Below the caps, head and tail are both the whole stream so far.
	if inc.Head != `{"path":"a.go","content":"hello\n"}` {
		t.Errorf("head = %q", inc.Head)
	}
	if inc.Tail != inc.Head {
		t.Errorf("tail = %q, want the whole stream so far", inc.Tail)
	}
}

// The preview is bounded at both ends, because it goes into every snapshot;
// the byte counter is not, so the card can still report the true size.
func TestIncomingHeadAndTailAreBounded(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvRunStart, RunID: "r1"})
	h.Publish(Event{Type: EvToolArgs, CallID: "c1", Name: "write"})
	for i := 0; i < 20; i++ {
		h.Publish(Event{Type: EvToolArgs, CallID: "c1", Text: strings.Repeat("x", 1024) + "\n"})
	}

	inc := h.Snapshot().Live.Incoming[0]
	if len(inc.Head) != maxIncomingHead {
		t.Errorf("head kept %d bytes, want exactly %d", len(inc.Head), maxIncomingHead)
	}
	if len(inc.Tail) > maxIncomingTail {
		t.Errorf("tail kept %d bytes, want at most %d", len(inc.Tail), maxIncomingTail)
	}
	if inc.Bytes != 20*1025 {
		t.Errorf("bytes = %d, want the full count though the preview is bounded", inc.Bytes)
	}
	// The tail is what matters: the last lines say how the write is going.
	if !strings.HasSuffix(inc.Tail, "\n") {
		t.Error("the tail should end on a line boundary")
	}
}

// Lines counts content newlines — the two-character `\n` escape of the raw
// arguments — so the changes tab can show a live +N while a write streams.
// An escaped backslash (\\n) is literal text, and an escape split across two
// fragments counts once.
func TestIncomingLinesCountContentNewlines(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvRunStart, RunID: "r1"})
	h.Publish(Event{Type: EvToolArgs, CallID: "c1", Name: "write"})
	// Raw Go strings: `\n` below is the two characters backslash + n, exactly
	// what a JSON string escape looks like on the wire.
	h.Publish(Event{Type: EvToolArgs, CallID: "c1", Text: `{"path":"a.md","content":"one\ntwo\`}) // escape split here
	h.Publish(Event{Type: EvToolArgs, CallID: "c1", Text: `nthree\\nnot-a-newline`})

	inc := h.Snapshot().Live.Incoming[0]
	// one\ntwo is one, the split \n across the fragments is the second; \\n is text.
	if inc.Lines != 2 {
		t.Errorf("lines = %d, want 2 (split escape counted once, \\\\n not counted)", inc.Lines)
	}
}

// tool_start settles the arguments: the pending-tool card takes over, and the
// incoming preview must go away or the same call would render twice.
func TestToolStartDropsTheIncomingEntry(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvRunStart, RunID: "r1"})
	h.Publish(Event{Type: EvToolArgs, CallID: "c1", Name: "write"})
	h.Publish(Event{Type: EvToolArgs, CallID: "c1", Text: `{"path":"a.go"}`})
	h.Publish(Event{Type: EvToolStart, CallID: "c1", Name: "write"})

	snap := h.Snapshot()
	if len(snap.Live.Incoming) != 0 {
		t.Errorf("incoming calls = %d after tool_start, want 0", len(snap.Live.Incoming))
	}
	if len(snap.Live.PendingTools) != 1 {
		t.Errorf("pending tools = %d, want 1", len(snap.Live.PendingTools))
	}
}

// An abort mid-stream ends the run without tool_start: the preview must not
// linger in the snapshot, and it must not leak into the next run.
func TestRunEndClearsIncomingCalls(t *testing.T) {
	h := NewHub()
	h.Publish(Event{Type: EvRunStart, RunID: "r1"})
	h.Publish(Event{Type: EvToolArgs, CallID: "c1", Name: "write"})
	h.Publish(Event{Type: EvToolArgs, CallID: "c1", Text: `{"path":"a.go",`})
	h.Publish(Event{Type: EvRunEnd, StopReason: "aborted"})

	if n := len(h.Snapshot().Live.Incoming); n != 0 {
		t.Errorf("incoming calls = %d after run_end, want 0", n)
	}
	h.Publish(Event{Type: EvRunStart, RunID: "r2"})
	if n := len(h.Snapshot().Live.Incoming); n != 0 {
		t.Errorf("incoming calls leaked into the next run: %d", n)
	}
}

// The new-session dialog's contract: a subdirectory of the root becomes the
// session's working directory; anything outside the root is refused, because
// the session cwd is where the tool path guard bites.
func TestCreateSessionWorkspace(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("hi")))
	root := h.mgr.Cwd()
	if err := os.MkdirAll(filepath.Join(root, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plain.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	create := func(body string) (int, map[string]any) {
		t.Helper()
		resp := h.do(http.MethodPost, "/api/sessions", body)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// A subdirectory under the root is honoured: reported back relative,
	// recorded absolute in the transcript meta.
	code, out := create(`{"workspace":"sub/deep"}`)
	if code != http.StatusCreated {
		t.Fatalf("workspace create: %d (%v)", code, out)
	}
	if out["workspace"] != "sub/deep" {
		t.Fatalf("workspace = %v, want sub/deep", out["workspace"])
	}
	sid := out["session_id"].(string)
	if got := h.session(sid).store.Meta().Cwd; got != filepath.Join(root, "sub", "deep") {
		t.Fatalf("meta cwd = %q, want %q", got, filepath.Join(root, "sub", "deep"))
	}

	// No workspace is the root itself.
	if code, out := create(`{}`); code != http.StatusCreated || out["workspace"] != "" {
		t.Fatalf("root create: %d workspace=%v, want 201 and \"\"", code, out["workspace"])
	}

	// Anything leaving the root, missing, or not a directory is refused.
	for _, bad := range []string{
		`{"workspace":"/etc"}`,
		`{"workspace":"sub/../../.."}`,
		`{"workspace":"nope"}`,
		`{"workspace":"plain.txt"}`,
	} {
		if code, out := create(bad); code != http.StatusBadRequest {
			t.Fatalf("%s: %d (%v), want 400", bad, code, out)
		}
	}

	// The workspace survives a reload from disk.
	h.mgr.evictAll()
	if got := h.session(sid).Workspace(); got != "sub/deep" {
		t.Fatalf("after reload workspace = %q, want sub/deep", got)
	}
}

// A hand-edited transcript can claim any cwd. Loading it must not move the
// path guard outside the server root — the loader falls back to the root.
func TestSessionCwdTamperedFallsBackToRoot(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("hi")))
	sid := h.createSession()

	path := filepath.Join(h.mgr.cfg.SessionDir, sid+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	old := `"cwd":"` + h.mgr.Cwd() + `"`
	tampered := strings.Replace(string(data), old, `"cwd":"/etc"`, 1)
	if tampered == string(data) {
		t.Fatalf("creation meta %q not found in transcript", old)
	}
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}

	h.mgr.evictAll()
	if got := h.session(sid).Workspace(); got != "" {
		t.Fatalf("tampered cwd must fall back to root, workspace = %q", got)
	}
}

// The workspace picked at creation is where the session's tools actually run:
// a relative write must land inside the subdirectory and nowhere else. The
// create-path tests prove the cwd is recorded; this one proves it is honoured.
func TestSessionWorkspaceConfinesToolWrites(t *testing.T) {
	var root string
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			return toolTurn("write", `{"path":"note.txt","content":"hi"}`), nil
		}
		return textTurn("done"), nil
	}, func(c *Config) { root = c.Cwd })
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	resp := h.do(http.MethodPost, "/api/sessions", `{"workspace":"sub"}`)
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	h.setPolicy(out.SessionID, ModeAuto, 0)
	s := h.start(out.SessionID, "write the file")
	s.wait(t, EvRunEnd)
	s.close()

	if _, err := os.Stat(filepath.Join(root, "sub", "note.txt")); err != nil {
		t.Errorf("write did not land in the workspace subdirectory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "note.txt")); !os.IsNotExist(err) {
		t.Error("write escaped to the server root")
	}
}

// Compaction replaces the whole conversation with a summary of it and every view
// has to agree afterwards: the hub's timeline, the store's live branch and the
// sidebar count. The abandoned records stay in the file, which is what makes this a
// fork rather than an edit.
func TestCompactReplacesTheConversationAndRebuildsEveryView(t *testing.T) {
	// The answers have to be long enough that a summary is genuinely smaller,
	// because ErrNotSmaller is a real refusal and a four-line conversation trips it:
	// the pinned opening request plus the preamble outweigh the thing summarised.
	h := newHarness(t, scriptedTurns(
		textTurn("answer one "+strings.Repeat("detail ", 500)),
		textTurn("answer two "+strings.Repeat("detail ", 500)),
		textTurn("a summary"),
	))
	sid := h.createSession()

	s1 := h.start(sid, "first question")
	s1.wait(t, EvRunEnd)
	s2 := h.start(sid, "second question")
	s2.wait(t, EvRunEnd)
	s1.close()
	s2.close()

	if before := h.session(sid).Hub().Snapshot(); len(before.Messages) != 4 {
		t.Fatalf("before: %d messages, want u1 m1 u2 m2 = 4", len(before.Messages))
	}

	// A watching client must hear about it: its timeline no longer describes the
	// session's history at all.
	s3 := h.stream(sid, "")
	defer s3.close()
	if first := s3.next(t); first.Type != EvSnapshot {
		t.Fatalf("first frame = %s, want snapshot", first.Type)
	}

	h.post("/api/sessions/"+sid+"/control", `{"action":"compact"}`, http.StatusOK)
	s3.wait(t, EvCompacted)

	after := h.session(sid).Hub().Snapshot()
	if len(after.Messages) != 1 {
		t.Fatalf("after: %d messages, want the single replacement", len(after.Messages))
	}
	text := after.Messages[0].Content[0].Text
	if !strings.Contains(text, "a summary") {
		t.Errorf("the replacement does not carry the summary: %q", text)
	}
	// The opening request is pinned verbatim; it is where constraints get stated.
	if !strings.Contains(text, "first question") {
		t.Errorf("the opening request was not pinned: %q", text)
	}

	// The store's live branch agrees, and so does the sidebar: the original records
	// are still in the file but must not be reachable or counted.
	if msgs := h.session(sid).store.Messages(""); len(msgs) != 1 {
		t.Errorf("store branch: %d messages, want 1", len(msgs))
	}
	list, err := h.mgr.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Messages != 1 {
		t.Errorf("sidebar: %+v, want one session with 1 message", list)
	}

	// The next turn continues from the replacement, and survives a reload from disk
	// — which only happens if the persistence baseline moved with the fork instead
	// of re-appending the branch or slicing past the end of it.
	s4 := h.start(sid, "what next")
	s4.wait(t, EvRunEnd)
	s4.close()
	if msgs := h.session(sid).store.Messages(""); len(msgs) != 3 {
		t.Errorf("after the next turn: %d messages, want summary + user + assistant = 3", len(msgs))
	}
}

// A run in flight blocks it, the same way it blocks SetModel and rewind: the loop
// appends to the history compaction is about to replace.
func TestCompactIsRefusedWhileARunIsInFlight(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			// Hold the turn open so the request is unambiguously mid-run rather than
			// racing the end of it.
			<-release
		}
		return textTurn("done"), nil
	})
	sid := h.createSession()
	s := h.start(sid, "go")
	s.wait(t, EvRunStart)

	h.post("/api/sessions/"+sid+"/control", `{"action":"compact"}`, http.StatusConflict)

	close(release)
	s.wait(t, EvRunEnd)
	s.close()
}

// The same reasoning as every other slash command: /compact must never reach the
// transcript. It is the worst one to get wrong, because the literal text would be
// sent to the model as a prompt inside the very conversation it was meant to
// replace — and the model would answer it.
func TestCompactCannotBeSentAsAPrompt(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("hi")))
	sid := h.createSession()
	h.post("/api/sessions/"+sid+"/messages", `{"prompt":"/compact"}`, http.StatusBadRequest)
	h.post("/api/sessions/"+sid+"/control", `{"action":"steer","prompt":"/compact"}`, http.StatusBadRequest)
	// A prompt that merely mentions it is still a prompt.
	h.post("/api/sessions/"+sid+"/messages", `{"prompt":"what does /compact do?"}`, http.StatusAccepted)
}

// Nothing to compact is a well-formed request with an unhelpful answer, so it is
// 422 rather than 400 or 500.
func TestCompactRefusesAnEmptyConversation(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("hi")))
	sid := h.createSession()
	h.post("/api/sessions/"+sid+"/control", `{"action":"compact"}`, http.StatusUnprocessableEntity)
}

// The summarising call is spend, and it is recorded when it happens rather than at
// the next run: a session compacted and then closed or evicted would otherwise lose
// it, and the CLI's /compact records it immediately for the same reason.
//
// Also pins the half that was silently wrong for longer: the cost of the branch
// compaction abandons has to survive on disk, because the provider billed it
// whichever branch is live now.
func TestCompactRecordsItsOwnCostAndKeepsTheOldBranchesCost(t *testing.T) {
	billed := func(text string, in, out int64) llm.Response {
		r := textTurn(text)
		r.Usage = llm.Usage{Input: in, Output: out}
		return r
	}
	h := newHarness(t, scriptedTurns(
		billed("answer "+strings.Repeat("detail ", 500), 4000, 200),
		billed("a summary", 3500, 150),
	))
	sid := h.createSession()
	s1 := h.start(sid, "question")
	s1.wait(t, EvRunEnd)
	s1.close()

	spentBeforeCompaction, _ := h.session(sid).store.UsageTotals()
	if spentBeforeCompaction.Input != 4000 {
		t.Fatalf("precondition: recorded input = %d, want the turn's 4000", spentBeforeCompaction.Input)
	}

	h.post("/api/sessions/"+sid+"/control", `{"action":"compact"}`, http.StatusOK)

	// On disk, without another run having happened: the turn's 4000 (on a branch that
	// is now unreachable) plus the summarising call's 3500.
	after, _ := h.session(sid).store.UsageTotals()
	if after.Input != 7500 || after.Output != 350 {
		t.Errorf("recorded usage = %d/%d, want 7500/350 (4000+3500 in, 200+150 out)",
			after.Input, after.Output)
	}

	// And a fresh load of the same file agrees, which is what a resumed session and
	// the sidebar both read.
	reloaded, err := session.Open(h.session(sid).store.Path())
	if err != nil {
		t.Fatal(err)
	}
	usage, _ := reloaded.UsageTotals()
	if usage.Input != after.Input {
		t.Errorf("reloaded usage = %d, want %d", usage.Input, after.Input)
	}
}

// The gauge colours its warning bands against the clearing trigger, not against
// fractions of the window, so the trigger has to reach the client. With the trigger
// at four fifths of the window, clearing holds occupancy just below it — a gauge
// using fixed percentages would sit permanently in its warning colour and stop
// carrying information.
func TestSnapshotCarriesTheClearTrigger(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("hi")))
	sid := h.createSession()

	snap := h.session(sid).Hub().Snapshot()
	if snap.ClearTrigger <= 0 {
		t.Fatalf("ClearTrigger = %d, want the resolved trigger for this session's model", snap.ClearTrigger)
	}
	// It is a fraction of the window rather than a constant, so it has to be below the
	// ceiling and above half — the two things the number is chosen to be.
	window := snap.Run.ContextWindow
	if window <= 0 {
		t.Fatalf("snapshot has no context window to check the trigger against")
	}
	if snap.ClearTrigger >= int64(window) {
		t.Errorf("trigger %d is not below the window %d", snap.ClearTrigger, window)
	}
	if snap.ClearTrigger <= int64(window)/2 {
		t.Errorf("trigger %d is at or below half the window %d", snap.ClearTrigger, window)
	}
}

// "auto" is a fraction of the *current* model's window, so switching models moves the
// trigger — and the browser has to hear about it, or it keeps colouring against a
// threshold the session no longer uses.
func TestClearTriggerFollowsAModelSwitch(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("hi")))
	sid := h.createSession()

	before := h.session(sid).Hub().Snapshot()
	var other string
	for _, m := range config.Catalog() {
		if m.ContextWindow > 0 && m.ContextWindow != before.Run.ContextWindow && config.Configured(m.Provider) {
			other = m.ID
			break
		}
	}
	if other == "" {
		t.Skip("the catalogue has no second configured model with a different window")
	}

	h.post("/api/sessions/"+sid+"/control",
		fmt.Sprintf(`{"action":"set_model","model":%q}`, other), http.StatusOK)

	after := h.session(sid).Hub().Snapshot()
	if after.Run.ContextWindow == before.Run.ContextWindow {
		t.Fatalf("the window did not change; the switch did not take effect")
	}
	if after.ClearTrigger == before.ClearTrigger {
		t.Errorf("trigger stayed at %d across a window change (%d → %d)",
			before.ClearTrigger, before.Run.ContextWindow, after.Run.ContextWindow)
	}
	if after.ClearTrigger >= int64(after.Run.ContextWindow) {
		t.Errorf("trigger %d is not below the new window %d",
			after.ClearTrigger, after.Run.ContextWindow)
	}
}
