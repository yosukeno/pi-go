package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// Block.Details exists to reach the session file, never a provider. Sending it
// would spend input tokens on a diff of content the model just wrote, and on
// every subsequent turn of the conversation.
//
// The guarantee is structural — toWireMessages names the fields it copies — so
// this test exists to notice the day someone replaces that with a blanket
// marshal of the neutral type.
func TestDetailsNeverReachTheWire(t *testing.T) {
	const secret = "DETAILS-MUST-NOT-BE-SENT"
	history := []Message{
		{Role: RoleAssistant, Content: []Block{
			{Type: BlockToolUse, ID: "c1", Name: "edit", Input: json.RawMessage(`{"path":"a.go"}`)},
		}},
		{Role: RoleUser, Content: []Block{{
			Type:      BlockToolResult,
			ToolUseID: "c1",
			Text:      "Successfully replaced 1 block(s)",
			Details:   json.RawMessage(`{"diff":"` + secret + `"}`),
		}}},
	}

	payload, err := json.Marshal(toWireMessages("sys", history))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), secret) {
		t.Errorf("details reached the provider payload:\n%s", payload)
	}
	// The result text itself must still be there, or this test would pass on a
	// conversion that dropped the whole block.
	if !strings.Contains(string(payload), "Successfully replaced") {
		t.Errorf("the tool result went missing:\n%s", payload)
	}
}

// The same field must survive a JSON round trip, because that is the trip it
// takes through the session file.
func TestDetailsSurviveMessageRoundTrip(t *testing.T) {
	in := Message{Role: RoleUser, Content: []Block{{
		Type:      BlockToolResult,
		ToolUseID: "c1",
		Text:      "ok",
		Details:   json.RawMessage(`{"diff":"@@ -1 +1 @@","added":1,"removed":1}`),
	}}}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Message
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}

	var got struct {
		Diff  string `json:"diff"`
		Added int    `json:"added"`
	}
	if err := json.Unmarshal(out.Content[0].Details, &got); err != nil {
		t.Fatalf("details did not survive: %v", err)
	}
	if got.Diff != "@@ -1 +1 @@" || got.Added != 1 {
		t.Errorf("got %+v", got)
	}
}

// A block without details must not grow a `"details":null` field: session files
// are read by people, and every tool_use block would carry one.
func TestAbsentDetailsAreOmitted(t *testing.T) {
	raw, err := json.Marshal(Block{Type: BlockText, Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "details") {
		t.Errorf("got %s", raw)
	}
}
