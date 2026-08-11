package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/wangy/pi-go/agent"
	"github.com/wangy/pi-go/llm"
	"github.com/wangy/pi-go/wire"
)

// events is one run's worth of loop events, including the shapes most likely to
// corrupt a line-oriented stream: a tool call whose arguments are not valid JSON,
// output with an embedded newline, and a run that ends in an error.
func events() []agent.Event {
	return []agent.Event{
		{Kind: agent.EventAgentStart},
		{Kind: agent.EventTurnStart, Turn: 1},
		{Kind: agent.EventThinkingDelta, Text: "let me look"},
		{Kind: agent.EventToolStart, ToolCallID: "c1", ToolName: "bash", ToolArgs: `{"command":"ls"}`},
		{Kind: agent.EventToolPartial, ToolCallID: "c1", ToolName: "bash", Text: "a.go\nb.go\n"},
		{Kind: agent.EventToolEnd, ToolCallID: "c1", ToolName: "bash", ToolOutput: "a.go\nb.go\n"},
		// Malformed arguments are a real case: the loop never parses them, so a
		// model can emit anything. It must not be able to break the framing.
		{Kind: agent.EventToolStart, ToolCallID: "c2", ToolName: "read", ToolArgs: `{"path": broken`},
		{Kind: agent.EventToolEnd, ToolCallID: "c2", ToolName: "read", ToolOutput: "no such file", IsError: true},
		{Kind: agent.EventMessage, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "done"}}}, Usage: llm.Usage{Input: 10, Output: 2}},
		{Kind: agent.EventTextDelta, Text: "here is the answer\nwith a newline"},
		{Kind: agent.EventSteer, Text: "actually, do it the other way"},
		{Kind: agent.EventAgentEnd, StopReason: llm.StopError, Usage: llm.Usage{Input: 10, Output: 2}, Err: errors.New("boom")},
	}
}

func feed(list []agent.Event) <-chan agent.Event {
	ch := make(chan agent.Event, len(list))
	for _, e := range list {
		ch <- e
	}
	close(ch)
	return ch
}

// The whole contract of -mode json in one assertion: every line on stdout parses
// as a JSON object, and the first one identifies the session. A consumer must not
// have to skip a banner, tolerate a progress line, or know that tool output can
// contain newlines.
func TestJSONModeStdoutIsPureJSONL(t *testing.T) {
	var stdout, diag bytes.Buffer
	restore := diagOut
	diagOut = &diag
	defer func() { diagOut = restore }()

	j := newJSONEmitter(&stdout)
	if err := j.header(wire.Header{Session: "/tmp/s.jsonl", Cwd: "/w", Model: "m"}); err != nil {
		t.Fatalf("header: %v", err)
	}
	// The resumed banner is the line that used to go to stdout. It is emitted here
	// rather than mocked so that moving it back would fail this test.
	reportResumed("/tmp/s.jsonl", 7)

	runErr := j.Consume(feed(events()))
	if runErr == nil || runErr.Error() != "boom" {
		t.Errorf("Consume() = %v, want the run's own error to survive the front end", runErr)
	}

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("got %d lines, want the header plus events", len(lines))
	}
	for i, line := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Errorf("line %d is not a JSON object: %v\n  %q", i+1, err, line)
			continue
		}
		if obj["type"] == nil || obj["type"] == "" {
			t.Errorf("line %d has no type: %q", i+1, line)
		}
	}

	var head map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &head); err != nil {
		t.Fatalf("header line: %v", err)
	}
	if head["type"] != string(wire.Session) {
		t.Errorf("first line type = %v, want %q", head["type"], wire.Session)
	}
	if head["session"] != "/tmp/s.jsonl" {
		t.Errorf("header session = %v, want the transcript path", head["session"])
	}

	// The diagnostics went somewhere, and that somewhere is not stdout.
	if !strings.Contains(diag.String(), "resumed /tmp/s.jsonl") {
		t.Errorf("diagnostics = %q, want the resumed banner", diag.String())
	}
	if strings.Contains(stdout.String(), "resumed") {
		t.Error("the resumed banner reached stdout, which breaks every piped consumer")
	}
}

// Events the contract does not name are dropped rather than guessed at, and
// dropping them must not shift anything else. agent_start is the case that
// matters: web publishes its own run_start with the run id, and JSON mode says
// the same thing in its header.
func TestJSONModeSkipsUnnamedEvents(t *testing.T) {
	var stdout bytes.Buffer
	j := newJSONEmitter(&stdout)
	_ = j.Consume(feed([]agent.Event{
		{Kind: agent.EventAgentStart},
		{Kind: agent.EventTurnStart, Turn: 1},
	}))
	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want only the named event: %q", len(lines), stdout.String())
	}
	if !strings.Contains(lines[0], string(wire.TurnStart)) {
		t.Errorf("line = %q, want the turn_start event", lines[0])
	}
}

// An unknown mode is refused rather than silently treated as text: a typo in a
// script must not produce output the script then misparses.
func TestUnknownModeIsRefused(t *testing.T) {
	err := run(options{mode: "jsonl", prompt: "hi"})
	if err == nil || !strings.Contains(err.Error(), "unknown -mode") {
		t.Errorf("run(mode=jsonl) = %v, want a refusal naming the flag", err)
	}
}

// JSON mode has no REPL. Reading prompts from stdin line by line is the shape of
// a protocol, not of a one-shot, and a banner plus a prompt would corrupt the
// stream anyway.
func TestJSONModeNeedsAPrompt(t *testing.T) {
	var diag bytes.Buffer
	restore := diagOut
	diagOut = &diag
	defer func() { diagOut = restore }()

	err := run(options{mode: modeJSON, quiet: true, cwd: t.TempDir()})
	// Named exactly, so this cannot pass because of an unrelated failure such as a
	// missing API key: the refusal has to come from the flag combination.
	if err == nil || !strings.Contains(err.Error(), "-mode json needs a prompt") {
		t.Fatalf("run(mode=json) with no prompt = %v, want the prompt refusal", err)
	}
	// -quiet is answered rather than obeyed, and the answer goes to stderr.
	if !strings.Contains(diag.String(), "-quiet has no effect") {
		t.Errorf("diagnostics = %q, want the -quiet notice", diag.String())
	}
}
