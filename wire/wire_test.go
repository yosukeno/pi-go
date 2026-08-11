package wire_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/yosukeno/pi-go/agent"
	"github.com/yosukeno/pi-go/llm"
	"github.com/yosukeno/pi-go/wire"
)

// loopNames are the event names shared by every programmatic consumer. The list
// is written out rather than derived so that adding a name to the package is a
// deliberate act that shows up in this test's diff.
var loopNames = []wire.Type{
	wire.TurnStart, wire.Thinking, wire.Token, wire.Message,
	wire.ToolArgs, wire.ToolStart, wire.ToolPartial, wire.ToolEnd, wire.UserMessage, wire.RunEnd,
}

// The contract must not depend on the browser. If it did, `pi-go -mode json`
// would drag an embedded single-page app into a code path that only needs to
// print lines, and splitting the binary later (#22) would start by undoing this.
func TestEventPackageDoesNotImportWeb(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/yosukeno/pi-go/wire").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if dep == "github.com/yosukeno/pi-go/web" {
			t.Errorf("wire depends on web; the contract must not know about the browser")
		}
	}
}

// One definition, two consumers. A second copy of the string "tool_end" inside a
// serialising package is how the browser and a script end up disagreeing about
// what an event means, so the literals are counted there and required to appear
// only in this package.
//
// The scan is scoped to the packages that put these names on a wire — main
// (-mode json), web (SSE) and wire itself — rather than the whole tree. Other
// packages have their own vocabularies that happen to reuse the words: the loop's
// internal agent.EventKind, the provider format in llm, the transcript record
// types in session. Policing those would make this test fail on unrelated code
// and teach people to delete it.
func TestLoopEventNamesAreDefinedOnceInSerialisingPackages(t *testing.T) {
	where := map[string][]string{}
	for _, dir := range []string{"..", "../web", "."} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			for _, n := range loopNames {
				// A definition, not any mention: `X = "token"` counts, while
				// web/server.go's `given = r.URL.Query().Get("token")` does not.
				// The property under test is "one definition", and a plain
				// substring scan cannot tell an event name from a URL parameter
				// that happens to share the word.
				if regexp.MustCompile(`=\s*"` + regexp.QuoteMeta(string(n)) + `"`).Match(b) {
					where[string(n)] = append(where[string(n)], path)
				}
			}
		}
	}
	for _, n := range loopNames {
		files := where[string(n)]
		if len(files) != 1 || filepath.Base(files[0]) != "wire.go" {
			t.Errorf("event name %q appears as a literal in %v, want only wire/wire.go", n, files)
		}
	}
}

// An error must survive translation. agent.Event carries a plain `error`, which
// marshals to `{}` — a consumer of the raw struct would watch a run fail and be
// told nothing.
func TestRunEndCarriesTheErrorText(t *testing.T) {
	ev, ok := wire.FromAgent(agent.Event{
		Kind: agent.EventAgentEnd, StopReason: llm.StopError, Err: errors.New("upstream refused"),
	})
	if !ok {
		t.Fatal("agent_end was dropped")
	}
	if ev.Error != "upstream refused" {
		t.Errorf("Error = %q, want the flattened message", ev.Error)
	}
	blob, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(blob), "upstream refused") {
		t.Errorf("marshalled run_end = %s, want the error text in it", blob)
	}
}

// The reason a run ended must reach the wire as a field, not only as prose inside
// Error. This is the whole point of agent.EndReason: a driver script deciding
// whether to start another run branches on `end_reason`, and before it existed the
// only machine-readable signal for a turn cap was the wording of the error message —
// which made every reword a breaking change to an interface nobody had declared.
func TestRunEndCarriesTheEndReason(t *testing.T) {
	ev, ok := wire.FromAgent(agent.Event{
		Kind:      agent.EventAgentEnd,
		EndReason: agent.EndTurnLimit,
		Err:       errors.New("stopped after 50 turns without a final answer"),
	})
	if !ok {
		t.Fatal("agent_end was dropped")
	}
	if ev.EndReason != string(agent.EndTurnLimit) {
		t.Errorf("EndReason = %q, want %q", ev.EndReason, agent.EndTurnLimit)
	}

	var got map[string]any
	blob, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Asserted through the JSON rather than the struct because the name is the
	// contract: `jq -r .end_reason` is how this gets consumed.
	if got["end_reason"] != "turn_limit" {
		t.Errorf("marshalled run_end = %s, want end_reason=turn_limit", blob)
	}
}

// end_reason is additive: stop_reason keeps its old meaning and its old value. The
// two answer different questions — the provider's reason for ending a reply versus
// the harness's for ending the run — and a consumer written before end_reason
// existed must see no change.
func TestEndReasonDoesNotDisturbStopReason(t *testing.T) {
	ev, _ := wire.FromAgent(agent.Event{
		Kind:       agent.EventAgentEnd,
		StopReason: llm.StopEndTurn,
		EndReason:  agent.EndCompleted,
	})
	if ev.StopReason != string(llm.StopEndTurn) {
		t.Errorf("StopReason = %q, want %q", ev.StopReason, llm.StopEndTurn)
	}
	if ev.EndReason != string(agent.EndCompleted) {
		t.Errorf("EndReason = %q, want %q", ev.EndReason, agent.EndCompleted)
	}

	// And it is absent rather than empty when the loop did not set it, so a
	// consumer can tell "no reason given" from a reason it does not recognise.
	bare, _ := wire.FromAgent(agent.Event{Kind: agent.EventAgentEnd, StopReason: llm.StopEndTurn})
	blob, err := json.Marshal(bare)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), "end_reason") {
		t.Errorf("marshalled run_end = %s, want no end_reason key when unset", blob)
	}
}

// Malformed tool arguments must not break the framing. The loop never parses
// them, so a model can emit anything; embedding it verbatim would produce a line
// the consumer cannot read, turning one bad tool call into a broken stream.
func TestMalformedToolArgsStayValidJSON(t *testing.T) {
	ev, ok := wire.FromAgent(agent.Event{
		Kind: agent.EventToolStart, ToolCallID: "c1", ToolName: "read", ToolArgs: `{"path": broken`,
	})
	if !ok {
		t.Fatal("tool_start was dropped")
	}
	blob, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Errorf("event with malformed args did not round-trip: %v", err)
	}
	if _, ok := back["args"].(string); !ok {
		t.Errorf("args = %v, want the unparseable text carried as a JSON string", back["args"])
	}
}

// Events the contract does not name are reported as such rather than emitted with
// an empty type, which a consumer would have to special-case.
func TestUnnamedEventsAreDropped(t *testing.T) {
	if _, ok := wire.FromAgent(agent.Event{Kind: agent.EventAgentStart}); ok {
		t.Error("agent_start was translated; web publishes run_start and json mode a header")
	}
	if _, ok := wire.FromAgent(agent.Event{Kind: "something_new"}); ok {
		t.Error("an unknown kind was translated")
	}
}

// EventToolResults is the one named loop event that must stay off the wire: the
// results it aggregates already reach every consumer as tool_end events, and a
// second copy would be noise. Its audience is the in-process persister
// (agent.TurnPersister), which reads agent.Event directly.
func TestToolResultsStayOffTheWire(t *testing.T) {
	if _, ok := wire.FromAgent(agent.Event{Kind: agent.EventToolResults}); ok {
		t.Error("tool_results was translated; the wire already carries the same results as tool_end")
	}
}
