package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// gateStub is a child that asks for approval over fd 3 and reports what it was
// told. Written in sh because that is the cheapest way to have a *real* process on
// the other end of a real pipe: the thing under test is the descriptor plumbing,
// which an in-process fake would skip entirely.
const gateStubScript = `#!/bin/sh
say() { printf '%s\n' "$1"; }
say '{"type":"session","ts":1,"session":"/tmp/child.jsonl"}'
say '{"type":"turn_start","turn":1}'
# Ask over fd 3, read the answer from fd 4.
printf '{"call_id":"c1","tool":"bash","args":{"command":"%s"}}\n' "$GATE_ASK" >&3
read -r verdict <&4
# The verdict is JSON, so its quotes have to be escaped before it can be quoted
# inside another JSON string. Getting this wrong produces a line the parent skips
# as unparseable, which is correct of it and confusing here.
esc=$(printf '%s' "$verdict" | sed 's/"/\\"/g')
say '{"type":"token","text":"verdict was '"$esc"'"}'
say '{"type":"run_end","stop_reason":"end_turn"}'
`

func gateStub(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gate-stub")
	if err := os.WriteFile(p, []byte(gateStubScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// The hole this closes, tested end to end over real pipes: a child's tool call is
// decided by the parent, so a subagent cannot do what its parent would have needed
// approval for.
func TestChildCallsAreDecidedByTheParent(t *testing.T) {
	root, _ := repoFixture(t)
	stub := gateStub(t)
	t.Setenv("STUB_MODE", "gate")
	t.Setenv("GATE_ASK", "rm -rf /")

	var (
		mu   sync.Mutex
		seen []Approval
	)
	s := &Subagent{Cwd: root, Exe: stub, Review: func(_ context.Context, a Approval) Decision {
		mu.Lock()
		seen = append(seen, a)
		mu.Unlock()
		return Decision{Allow: false, Reason: "not on my watch"}
	}}

	res, err := call(t, s, ModeEdit, "try something")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("the parent was asked %d times, want exactly 1", len(seen))
	}
	got := seen[0]
	if got.Tool != "bash" {
		t.Errorf("Approval.Tool = %q, want bash", got.Tool)
	}
	if !strings.Contains(string(got.Args), "rm -rf /") {
		t.Errorf("Approval.Args = %s, want the command the child proposed", got.Args)
	}
	// The child must be able to identify itself, or a reviewer cannot tell which
	// delegated task a command belongs to.
	if got.Subagent == "" || !strings.HasPrefix(got.Subagent, "sub") {
		t.Errorf("Approval.Subagent = %q, want the worktree id", got.Subagent)
	}
	// And the refusal reached the child, which is what makes a block information
	// rather than a crash.
	if !strings.Contains(res.Text, "not on my watch") {
		t.Errorf("child answer = %q, want the parent's reason in it", res.Text)
	}
}

// An allowed call comes back as an allow, and the reason field stays out of the way.
func TestChildCallsCanBeAllowed(t *testing.T) {
	root, _ := repoFixture(t)
	stub := gateStub(t)
	t.Setenv("GATE_ASK", "go test ./...")

	s := &Subagent{Cwd: root, Exe: stub, Review: func(_ context.Context, _ Approval) Decision {
		return Decision{Allow: true}
	}}
	res, err := call(t, s, ModeEdit, "run the tests")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Text, `"allow":true`) {
		t.Errorf("child answer = %q, want the allow verdict to have reached it", res.Text)
	}
}

// Without a Review callback there is no channel at all, and the child must not
// think it has one: that is the terminal's situation, where `pi-go -p` has always
// run every call.
func TestNoReviewMeansNoChannel(t *testing.T) {
	root, _ := repoFixture(t)
	// A stub that would block forever on fd 4 if it believed a gate existed.
	p := filepath.Join(t.TempDir(), "nogate-stub")
	script := `#!/bin/sh
printf '%s\n' '{"type":"session","ts":1,"session":"/tmp/c.jsonl"}'
printf '%s\n' '{"type":"token","text":"gatefd=['"$PI_GO_GATE_FD"']"}'
printf '%s\n' '{"type":"run_end","stop_reason":"end_turn"}'
`
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Subagent{Cwd: root, Exe: p}
	res, err := call(t, s, ModeEdit, "no gate here")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Text, "gatefd=[]") {
		t.Errorf("child answer = %q, want no gate descriptor advertised", res.Text)
	}
}

// A child whose parent vanishes must not carry on unsupervised. Failing closed is
// the same rule the browser gate follows when nobody answers in time.
func TestFDGateFailsClosedWhenTheParentIsGone(t *testing.T) {
	reqR, reqW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	verR, verW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	g := &FDGate{requests: reqW, verdicts: newReader(verR), enc: json.NewEncoder(reqW)}

	// The parent closes both ends without answering, as it would if it crashed.
	reqR.Close()
	verW.Close()

	allow, reason := g.Ask("c1", "bash", json.RawMessage(`{"command":"ls"}`))
	if allow {
		t.Error("a call was allowed with no parent to allow it")
	}
	if reason == "" {
		t.Error("a refusal with no reason gives the model nothing to react to")
	}
}

// Requests are answered one at a time. The channel is one pipe with one reply per
// request, so a second concurrent question would read the first one's answer — and
// the timeout arithmetic assumes a child holds at most one approval at a time.
func TestGateChannelAnswersOneAtATime(t *testing.T) {
	reqR, reqW, _ := os.Pipe()
	verR, verW, _ := os.Pipe()
	defer verR.Close()

	var inFlight, maxInFlight int
	var mu sync.Mutex
	s := &Subagent{Review: func(_ context.Context, _ Approval) Decision {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()
		defer func() { mu.Lock(); inFlight--; mu.Unlock() }()
		return Decision{Allow: true}
	}}

	done := make(chan struct{})
	go func() { defer close(done); s.serveGate(reqR, verW, "sub1") }()

	enc := json.NewEncoder(reqW)
	for i := 0; i < 5; i++ {
		if err := enc.Encode(gateRequest{CallID: "c", Tool: "bash"}); err != nil {
			t.Fatal(err)
		}
	}
	// Drain the answers so the server is never blocked on the write side.
	r := newReader(verR)
	for i := 0; i < 5; i++ {
		if _, err := r.ReadString('\n'); err != nil {
			t.Fatalf("verdict %d: %v", i, err)
		}
	}
	reqW.Close()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if maxInFlight != 1 {
		t.Errorf("%d approvals were in flight at once, want 1", maxInFlight)
	}
}

// A request that cannot be parsed is refused rather than dropped: the child is
// blocked on an answer, and silence would hang it until the subagent timeout.
func TestMalformedRequestGetsARefusal(t *testing.T) {
	reqR, reqW, _ := os.Pipe()
	verR, verW, _ := os.Pipe()
	defer verR.Close()

	asked := false
	s := &Subagent{Review: func(_ context.Context, _ Approval) Decision {
		asked = true
		return Decision{Allow: true}
	}}
	done := make(chan struct{})
	go func() { defer close(done); s.serveGate(reqR, verW, "sub1") }()

	if _, err := reqW.Write([]byte("this is not json\n")); err != nil {
		t.Fatal(err)
	}
	line, err := newReader(verR).ReadString('\n')
	if err != nil {
		t.Fatalf("no verdict for a malformed request: %v", err)
	}
	var v gateVerdict
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &v); err != nil {
		t.Fatalf("verdict %q: %v", line, err)
	}
	if v.Allow {
		t.Error("a malformed request was allowed")
	}
	if asked {
		t.Error("a malformed request reached the reviewer")
	}
	reqW.Close()
	<-done
}

// newReader keeps the test's use of bufio in one place.
func newReader(f *os.File) *bufio.Reader { return bufio.NewReader(f) }
