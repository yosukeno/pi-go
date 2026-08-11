package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/wangy/pi-go/llm"
	"github.com/wangy/pi-go/tools"
)

// big is text of a known size, so a test asserts token counts instead of ranges.
func big(tokens int64) string { return strings.Repeat("x", int(tokens)*llm.BytesPerToken) }

func use(id, name string) llm.Block {
	return llm.Block{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(`{"path":"a.go"}`)}
}

func res(id, text string) llm.Block {
	return llm.Block{Type: llm.BlockToolResult, ToolUseID: id, Text: text}
}

// history builds n read call/result pairs of the given size, oldest first.
func history(n int, each int64) []llm.Message {
	msgs := []llm.Message{llm.UserText("go")}
	for i := range n {
		id := string(rune('a' + i))
		msgs = append(msgs,
			llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{use(id, "read")}},
			llm.Message{Role: llm.RoleUser, Content: []llm.Block{res(id, big(each))}},
		)
	}
	return msgs
}

func cfg() ContextEditConfig {
	return ContextEditConfig{Trigger: 10_000, Keep: 2, ClearAtLeast: 1}
}

// Below the trigger the prompt has to come out byte-identical. Every caller that
// has not opted in relies on this, and so does the prompt cache.
func TestContextEditIsANoopBelowTheTrigger(t *testing.T) {
	msgs := history(6, 1000)
	got, edit := editContext(msgs, cfg(), nil, 9_999)
	if edit.ClearedResults != 0 {
		t.Errorf("cleared %d results below the trigger", edit.ClearedResults)
	}
	assertSameText(t, msgs, got)
}

// A zero Trigger is the zero value, so an Agent built without asking for this must
// behave exactly as it did before the feature existed.
func TestContextEditDisabledByDefault(t *testing.T) {
	msgs := history(20, 20_000)
	got, edit := editContext(msgs, ContextEditConfig{}, nil, 400_000)
	if edit.ClearedResults != 0 {
		t.Errorf("a zero config cleared %d results", edit.ClearedResults)
	}
	assertSameText(t, msgs, got)
}

// The core behaviour: oldest first, sparing the most recent Keep results.
func TestContextEditClearsOldestAndKeepsRecent(t *testing.T) {
	msgs := history(6, 1000)
	got, edit := editContext(msgs, cfg(), nil, 20_000)

	if edit.ClearedResults != 4 {
		t.Fatalf("cleared %d, want 4 (6 results minus Keep=2)", edit.ClearedResults)
	}
	if edit.ClearedTokens != 4000 {
		t.Errorf("ClearedTokens = %d, want 4000", edit.ClearedTokens)
	}
	// The four oldest are placeholders and the two newest are verbatim.
	for i, want := range []bool{true, true, true, true, false, false} {
		text := resultText(got, i)
		if cleared := strings.HasPrefix(text, "["); cleared != want {
			t.Errorf("result %d cleared=%v, want %v: %.60s", i, cleared, want, text)
		}
	}
}

// The tool call itself must survive with its arguments, which is Anthropic's
// clear_tool_inputs=false default: the record that the call happened, and against
// what, is what lets the model decide whether to re-issue it.
func TestContextEditKeepsTheCallAndOnlyBlanksTheResult(t *testing.T) {
	msgs := history(4, 5000)
	got, _ := editContext(msgs, cfg(), nil, 50_000)

	for i, m := range got {
		for j, b := range m.Content {
			if b.Type != llm.BlockToolUse {
				continue
			}
			in := msgs[i].Content[j]
			if b.Name != in.Name || string(b.Input) != string(in.Input) || b.ID != in.ID {
				t.Errorf("tool_use %d/%d was altered: %+v", i, j, b)
			}
		}
	}
	// Shape is preserved exactly: an unpaired tool_use is rejected on the next
	// request and makes the session unresumable.
	if len(got) != len(msgs) {
		t.Fatalf("message count changed: %d vs %d", len(got), len(msgs))
	}
	for i := range got {
		if len(got[i].Content) != len(msgs[i].Content) {
			t.Fatalf("block count changed in message %d", i)
		}
		for j := range got[i].Content {
			if got[i].Content[j].Type != msgs[i].Content[j].Type {
				t.Errorf("block %d/%d changed type", i, j)
			}
			if got[i].Content[j].ToolUseID != msgs[i].Content[j].ToolUseID {
				t.Errorf("block %d/%d lost its pairing id", i, j)
			}
		}
	}
}

// The input is a view's source, never its target. The session file, -resume and the
// web diff view all read the original, which is the same division Anthropic draws
// by doing this server-side.
func TestContextEditNeverMutatesTheTranscript(t *testing.T) {
	msgs := history(6, 5000)
	before := resultText(msgs, 0)
	editContext(msgs, cfg(), nil, 60_000)
	if after := resultText(msgs, 0); after != before {
		t.Errorf("the input history was modified: %.60s", after)
	}
}

// Details is display data with no path to a provider, so clearing must not disturb
// it — the web diff view survives a reload because of it.
func TestContextEditPreservesDetails(t *testing.T) {
	msgs := history(4, 5000)
	msgs[2].Content[0].Details = json.RawMessage(`{"path":"a.go"}`)
	got, _ := editContext(msgs, cfg(), nil, 50_000)
	if string(got[2].Content[0].Details) != `{"path":"a.go"}` {
		t.Errorf("Details lost: %s", got[2].Content[0].Details)
	}
}

// Monotonicity: a result already cleared is cleared again even when the prompt has
// dropped back under the trigger. Without this the text would be restored, cleared
// again next turn, and the prompt prefix would flap — a cache miss per cycle, which
// on Kimi is billed at about ten times the hit rate.
func TestContextEditKeepsClearedResultsCleared(t *testing.T) {
	msgs := history(6, 1000)
	_, first := editContext(msgs, cfg(), nil, 20_000)
	if first.ClearedResults != 4 {
		t.Fatalf("first pass cleared %d, want 4", first.ClearedResults)
	}

	// Well under the trigger now.
	got, second := editContext(msgs, cfg(), first.cleared, 1_000)
	if second.ClearedResults != 4 {
		t.Errorf("second pass cleared %d, want the same 4 still cleared", second.ClearedResults)
	}
	if !strings.HasPrefix(resultText(got, 0), "[") {
		t.Error("a previously cleared result came back")
	}
}

// The placeholder must be byte-identical across passes, or the prefix changes every
// turn even when the set of cleared results does not.
func TestContextEditPlaceholderIsStable(t *testing.T) {
	msgs := history(6, 1000)
	a, edit := editContext(msgs, cfg(), nil, 20_000)
	b, _ := editContext(msgs, cfg(), edit.cleared, 20_000)
	assertSameText(t, a, b)
}

// The floor exists because clearing invalidates the cached prefix: a pass that
// frees a little buys a cache miss for nothing. Anthropic documents clear_at_least
// for exactly this.
func TestContextEditRefusesToClearTooLittle(t *testing.T) {
	msgs := history(6, 100) // 4 clearable results, 100 tokens each
	c := cfg()
	c.ClearAtLeast = 1000
	got, edit := editContext(msgs, c, nil, 20_000)
	if edit.ClearedResults != 0 {
		t.Errorf("cleared %d results for only %d tokens, below the floor",
			edit.ClearedResults, edit.ClearedTokens)
	}
	assertSameText(t, msgs, got)
}

// But the floor must not un-clear what is already cleared: that would restore the
// text and reintroduce the flapping the frozen set prevents.
func TestContextEditFloorDoesNotUndoEarlierClearing(t *testing.T) {
	msgs := history(6, 1000)
	_, first := editContext(msgs, cfg(), nil, 20_000)

	c := cfg()
	c.ClearAtLeast = 1_000_000 // nothing new could ever qualify
	_, second := editContext(msgs, c, first.cleared, 20_000)
	if second.ClearedResults != first.ClearedResults {
		t.Errorf("an unreachable floor un-cleared results: %d vs %d",
			second.ClearedResults, first.ClearedResults)
	}
}

// Failed results are spared. Anthropic's strategy does not do this; pi-go does,
// because recovering from an error string is its signature behaviour and the error
// text is short enough that sparing it costs almost nothing.
func TestContextEditSparesFailures(t *testing.T) {
	msgs := history(6, 1000)
	msgs[2].Content[0].IsError = true
	failed := resultText(msgs, 0) // message 2 holds the first result
	got, _ := editContext(msgs, cfg(), nil, 20_000)
	if resultText(got, 0) != failed {
		t.Errorf("a failed result was cleared: %.60s", resultText(got, 0))
	}
}

func TestContextEditHonoursExcludeTools(t *testing.T) {
	msgs := history(6, 1000)
	c := cfg()
	c.ExcludeTools = []string{"read"}
	got, edit := editContext(msgs, c, nil, 20_000)
	if edit.ClearedResults != 0 {
		t.Errorf("cleared %d results from an excluded tool", edit.ClearedResults)
	}
	assertSameText(t, msgs, got)
}

// The task list is state, not an event: only the newest one is the plan. Superseded
// ones are pure waste and go regardless of the keep window, while the newest is
// pinned even when it is the oldest thing in the history — it is the only record of
// what the work is that survives to a later context window.
func TestContextEditPinsTheNewestTaskListAndClearsTheRest(t *testing.T) {
	msgs := []llm.Message{
		llm.UserText("go"),
		{Role: llm.RoleAssistant, Content: []llm.Block{use("t1", "todo")}},
		{Role: llm.RoleUser, Content: []llm.Block{res("t1", "1. [pending] first plan")}},
		{Role: llm.RoleAssistant, Content: []llm.Block{use("t2", "todo")}},
		{Role: llm.RoleUser, Content: []llm.Block{res("t2", "1. [in_progress] second plan")}},
		{Role: llm.RoleAssistant, Content: []llm.Block{use("r1", "read")}},
		{Role: llm.RoleUser, Content: []llm.Block{res("r1", big(1000))}},
		{Role: llm.RoleAssistant, Content: []llm.Block{use("r2", "read")}},
		{Role: llm.RoleUser, Content: []llm.Block{res("r2", big(1000))}},
	}

	// Far below the trigger: the superseded list still goes, because it is not
	// eviction for space, it is a stale copy of a state.
	got, edit := editContext(msgs, cfg(), nil, 1)
	if edit.ClearedResults != 1 {
		t.Fatalf("cleared %d, want just the superseded list", edit.ClearedResults)
	}
	if !strings.HasPrefix(got[2].Content[0].Text, "[") {
		t.Errorf("the superseded list survived: %q", got[2].Content[0].Text)
	}
	if got[4].Content[0].Text != "1. [in_progress] second plan" {
		t.Errorf("the newest list was altered: %q", got[4].Content[0].Text)
	}

	// And over the trigger, with a keep window that would otherwise evict it: the
	// newest list is still there while the reads are gone.
	got, _ = editContext(msgs, cfg(), nil, 20_000)
	if got[4].Content[0].Text != "1. [in_progress] second plan" {
		t.Errorf("the newest list was evicted under pressure: %q", got[4].Content[0].Text)
	}
}

// The placeholder has to tell the model what it lost and that it can get it back.
// The tool call is still directly above with its arguments, so the path is not
// restated.
func TestContextEditPlaceholderSaysWhatToDo(t *testing.T) {
	msgs := history(6, 1000)
	got, _ := editContext(msgs, cfg(), nil, 20_000)
	text := resultText(got, 0)
	for _, want := range []string{"read", "removed", "Call the tool again"} {
		if !strings.Contains(text, want) {
			t.Errorf("placeholder is missing %q: %s", want, text)
		}
	}
}

// An empty result has nothing to clear, and blanking it would replace nothing with
// a sentence.
func TestContextEditIgnoresEmptyResults(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.Block{use("a", "bash")}},
		{Role: llm.RoleUser, Content: []llm.Block{res("a", "")}},
	}
	_, edit := editContext(msgs, cfg(), nil, 100_000)
	if edit.ClearedResults != 0 {
		t.Errorf("cleared an empty result")
	}
}

// --- helpers ---

// resultText returns the nth tool_result's text in message order.
func resultText(msgs []llm.Message, n int) string {
	i := 0
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type != llm.BlockToolResult {
				continue
			}
			if i == n {
				return b.Text
			}
			i++
		}
	}
	return ""
}

func assertSameText(t *testing.T, want, got []llm.Message) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("message count %d, want %d", len(got), len(want))
	}
	for i := range want {
		if len(want[i].Content) != len(got[i].Content) {
			t.Fatalf("block count in message %d: %d, want %d", i, len(got[i].Content), len(want[i].Content))
		}
		for j := range want[i].Content {
			if want[i].Content[j].Text != got[i].Content[j].Text {
				t.Errorf("block %d/%d text changed:\n got %.60q\nwant %.60q",
					i, j, got[i].Content[j].Text, want[i].Content[j].Text)
			}
		}
	}
}

// --- payload arguments -------------------------------------------------------
//
// Reader tools keep their arguments, per Anthropic's clear_tool_inputs=false.
// Writers do not, because there the large argument is the payload and the file on
// disk holds it. See payloadArgs for the measurement that prompted this.

// writeCall is a write call/result pair with content of a known size.
func writeCall(id, path string, contentTokens int64) []llm.Message {
	args, err := json.Marshal(map[string]string{"path": path, "content": big(contentTokens)})
	if err != nil {
		panic(err)
	}
	return []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.Block{{
			Type: llm.BlockToolUse, ID: id, Name: "write", Input: args,
		}}},
		{Role: llm.RoleUser, Content: []llm.Block{
			res(id, "Successfully wrote "+strconv.Itoa(int(contentTokens)*llm.BytesPerToken)+" bytes to "+path),
		}},
	}
}

// editCall is an edit call/result pair with n replacements of a known size.
func editCall(id, path string, n int, each int64) []llm.Message {
	ops := make([]map[string]string, n)
	for i := range ops {
		ops[i] = map[string]string{"oldText": big(each), "newText": big(each)}
	}
	args, err := json.Marshal(map[string]any{"path": path, "edits": ops})
	if err != nil {
		panic(err)
	}
	return []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.Block{{
			Type: llm.BlockToolUse, ID: id, Name: "edit", Input: args,
		}}},
		{Role: llm.RoleUser, Content: []llm.Block{res(id, "Successfully replaced 1 block(s) in "+path)}},
	}
}

// argsOf returns the nth tool_use's arguments in message order.
func argsOf(t *testing.T, msgs []llm.Message, n int) map[string]any {
	t.Helper()
	i := 0
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type != llm.BlockToolUse {
				continue
			}
			if i == n {
				var out map[string]any
				if err := json.Unmarshal(b.Input, &out); err != nil {
					t.Fatalf("arguments are not a JSON object: %s", b.Input)
				}
				return out
			}
			i++
		}
	}
	t.Fatalf("no tool_use at index %d", n)
	return nil
}

// The case the measurement found: a session whose history is almost entirely write
// content. The path survives, because that is the part Anthropic's default is
// actually protecting; the content does not, because the file holds it.
func TestContextEditClearsWritePayloadAndKeepsThePath(t *testing.T) {
	msgs := append([]llm.Message{llm.UserText("go")}, writeCall("w1", "big.go", 10_000)...)
	msgs = append(msgs, history(3, 100)[1:]...) // reads after it, so keep=2 spares those

	got, edit := editContext(msgs, cfg(), nil, 50_000)

	if edit.ClearedArgs != 1 {
		t.Fatalf("ClearedArgs = %d, want 1", edit.ClearedArgs)
	}
	if edit.ClearedArgTokens < 9_000 {
		t.Errorf("ClearedArgTokens = %d, want ~10000", edit.ClearedArgTokens)
	}
	args := argsOf(t, got, 0)
	if args["path"] != "big.go" {
		t.Errorf("the path was altered: %v", args["path"])
	}
	content, _ := args["content"].(string)
	if !strings.HasPrefix(content, "[") || len(content) > 64 {
		t.Errorf("content was not blanked: %.80q", content)
	}
	// The split has to be real. c.tokens carries both halves internally so the floor
	// can weigh them, and the report has to take them apart again: a write's result
	// is a one-line acknowledgement, so the payload belongs in the argument half. An
	// order of magnitude apart, not an exact figure, because this history also holds
	// a couple of ordinary read results.
	if edit.ClearedTokens*10 >= edit.ClearedArgTokens {
		t.Errorf("results %d vs arguments %d; the payload leaked into the result total",
			edit.ClearedTokens, edit.ClearedArgTokens)
	}
}

// Reader arguments are the description of the call — a path, a pattern, a command —
// and blanking them would remove the only thing worth keeping.
func TestContextEditKeepsReaderArgumentsIntact(t *testing.T) {
	for _, tool := range []string{"read", "grep", "bash", "ls", "find", "todo"} {
		t.Run(tool, func(t *testing.T) {
			payload, err := json.Marshal(map[string]string{"path": "a.go", "command": big(5_000)})
			if err != nil {
				t.Fatal(err)
			}
			msgs := []llm.Message{
				llm.UserText("go"),
				{Role: llm.RoleAssistant, Content: []llm.Block{{
					Type: llm.BlockToolUse, ID: "x", Name: tool, Input: payload,
				}}},
				{Role: llm.RoleUser, Content: []llm.Block{res("x", big(1000))}},
			}
			msgs = append(msgs, history(3, 100)[1:]...)

			got, edit := editContext(msgs, cfg(), nil, 50_000)
			if edit.ClearedArgs != 0 {
				t.Errorf("cleared arguments of %s, a tool whose arguments are its description", tool)
			}
			if string(got[1].Content[0].Input) != string(payload) {
				t.Errorf("%s arguments were rewritten", tool)
			}
		})
	}
}

// A failed call keeps its arguments, and for edit that is the whole point: oldText
// is exactly what the model needs in order to correct itself. This falls out of
// clearable skipping error results, so the test pins the consequence rather than
// the mechanism.
func TestContextEditSparesArgumentsOfAFailedCall(t *testing.T) {
	msgs := append([]llm.Message{llm.UserText("go")}, editCall("e1", "a.go", 2, 5_000)...)
	msgs[2].Content[0].IsError = true
	msgs = append(msgs, history(3, 100)[1:]...)

	before := string(msgs[1].Content[0].Input)
	got, edit := editContext(msgs, cfg(), nil, 50_000)
	if edit.ClearedArgs != 0 {
		t.Errorf("cleared the arguments of a failed edit; oldText is what the retry needs")
	}
	if string(got[1].Content[0].Input) != before {
		t.Error("a failed call's arguments were rewritten")
	}
}

// The array stays an array of objects with both fields, and its length still
// answers "how many replacements". A reshaped record is a false record, and a
// provider that validated historical arguments would reject it.
func TestContextEditPreservesTheEditsArrayShape(t *testing.T) {
	msgs := append([]llm.Message{llm.UserText("go")}, editCall("e1", "a.go", 3, 5_000)...)
	msgs = append(msgs, history(3, 100)[1:]...)

	got, edit := editContext(msgs, cfg(), nil, 50_000)
	if edit.ClearedArgs != 1 {
		t.Fatalf("ClearedArgs = %d, want 1", edit.ClearedArgs)
	}
	args := argsOf(t, got, 0)
	edits, ok := args["edits"].([]any)
	if !ok {
		t.Fatalf("edits stopped being an array: %T", args["edits"])
	}
	if len(edits) != 3 {
		t.Errorf("edits has %d entries, want the original 3", len(edits))
	}
	for i, raw := range edits {
		op, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("edits[%d] stopped being an object: %T", i, raw)
		}
		for _, f := range []string{"oldText", "newText"} {
			s, ok := op[f].(string)
			if !ok {
				t.Errorf("edits[%d].%s is missing or not a string: %v", i, f, op[f])
			}
			if !strings.HasPrefix(s, "[") {
				t.Errorf("edits[%d].%s was not blanked: %.40q", i, f, s)
			}
		}
	}
}

// Byte-identical across passes, or the prefix changes every turn and the cache
// never settles. Map key ordering is what makes this hold; the test is here
// because nothing else states that dependency.
func TestContextEditArgPlaceholderIsStable(t *testing.T) {
	msgs := append([]llm.Message{llm.UserText("go")}, writeCall("w1", "big.go", 10_000)...)
	msgs = append(msgs, editCall("e1", "b.go", 2, 5_000)...)
	msgs = append(msgs, history(3, 100)[1:]...)

	a, edit := editContext(msgs, cfg(), nil, 80_000)
	b, _ := editContext(msgs, cfg(), edit.cleared, 80_000)
	for i := range a {
		for j := range a[i].Content {
			if x, y := a[i].Content[j].Input, b[i].Content[j].Input; string(x) != string(y) {
				t.Errorf("block %d/%d arguments differ between passes:\n %s\n %s", i, j, x, y)
			}
		}
	}
}

// The floor has to weigh what clearing the call actually frees. A write's result is
// a one-line acknowledgement, so judging by the result alone would refuse to clear
// the largest thing in the history.
func TestContextEditWeighsArgumentsAgainstTheFloor(t *testing.T) {
	msgs := append([]llm.Message{llm.UserText("go")}, writeCall("w1", "big.go", 10_000)...)
	msgs = append(msgs, history(3, 10)[1:]...)

	c := cfg()
	c.ClearAtLeast = 5_000 // far above the result texts, far below the payload
	_, edit := editContext(msgs, c, nil, 50_000)
	if edit.ClearedArgs != 1 {
		t.Errorf("the floor refused a %d-token saving because the result was small", 10_000)
	}

	// And the floor is still a floor: a small write is not worth a cache miss.
	small := append([]llm.Message{llm.UserText("go")}, writeCall("w2", "tiny.go", 300)...)
	small = append(small, history(3, 10)[1:]...)
	_, edit = editContext(small, c, nil, 50_000)
	if freed := edit.ClearedTokens + edit.ClearedArgTokens; freed >= c.ClearAtLeast {
		t.Errorf("freed %d tokens, which should not have passed a floor of %d",
			freed, c.ClearAtLeast)
	}
}

// Below minPayloadBytes the placeholder is comparable to the payload, so the only
// certain effect would be a disturbed prefix.
func TestContextEditLeavesSmallPayloadsAlone(t *testing.T) {
	msgs := append([]llm.Message{llm.UserText("go")}, writeCall("w1", "a.go", 8)...) // 32 bytes
	msgs = append(msgs, history(3, 100)[1:]...)

	before := string(msgs[1].Content[0].Input)
	got, edit := editContext(msgs, cfg(), nil, 50_000)
	if edit.ClearedArgs != 0 {
		t.Errorf("blanked a %d-byte payload", 8*llm.BytesPerToken)
	}
	if string(got[1].Content[0].Input) != before {
		t.Error("a payload below the threshold was rewritten")
	}
}

// Arguments that could not be parsed are left exactly as they arrived.
//
// Recorded honestly: this property is currently structural rather than defended.
// encoding/json leaves the destination empty when it errors, so removing the parse
// guard in clearArgs would not change the output — mutation-tested, and it was the
// one mutation no test caught, which is the finding rather than a gap. The test is
// here for the refactor that would end it: a lenient decoder, or a "blank what we
// could parse" pass, would start rewriting records nobody understood.
func TestContextEditLeavesUnparseableArgumentsAlone(t *testing.T) {
	broken := json.RawMessage(`{"path":"a.go","content":"` + big(5_000)) // truncated
	msgs := []llm.Message{
		llm.UserText("go"),
		{Role: llm.RoleAssistant, Content: []llm.Block{{
			Type: llm.BlockToolUse, ID: "w1", Name: "write", Input: broken,
		}}},
		{Role: llm.RoleUser, Content: []llm.Block{res("w1", "ok")}},
	}
	msgs = append(msgs, history(3, 100)[1:]...)

	got, edit := editContext(msgs, cfg(), nil, 50_000)
	if edit.ClearedArgs != 0 {
		t.Error("rewrote arguments that could not be parsed")
	}
	if string(got[1].Content[0].Input) != string(broken) {
		t.Error("malformed arguments were altered")
	}
}

// The transcript is the view's source, never its target — now for the call as well
// as the result, since clearing touches two messages instead of one.
func TestContextEditNeverMutatesClearedArguments(t *testing.T) {
	msgs := append([]llm.Message{llm.UserText("go")}, writeCall("w1", "big.go", 10_000)...)
	msgs = append(msgs, history(3, 100)[1:]...)

	before := string(msgs[1].Content[0].Input)
	got, edit := editContext(msgs, cfg(), nil, 50_000)
	if edit.ClearedArgs != 1 {
		t.Fatal("nothing was cleared, so this proves nothing")
	}
	if after := string(msgs[1].Content[0].Input); after != before {
		t.Errorf("the input history was modified:\n %.80s", after)
	}
	if string(got[1].Content[0].Input) == before {
		t.Error("the view was not edited either, so the assertion above is vacuous")
	}
}

// "Call the tool again" is advice a model acts on, and for a writer it is wrong:
// re-running a write would rewrite the file with content the model no longer holds,
// and re-running an edit fails because oldText is no longer in the file. Every
// cleared write result carried that advice before payloadArgs existed.
func TestContextEditTellsAWriterToReadRatherThanRetry(t *testing.T) {
	msgs := append([]llm.Message{llm.UserText("go")}, writeCall("w1", "big.go", 10_000)...)
	msgs = append(msgs, history(3, 100)[1:]...)

	got, _ := editContext(msgs, cfg(), nil, 50_000)
	text := resultText(got, 0)
	if strings.Contains(text, "Call the tool again") {
		t.Errorf("a cleared write still tells the model to call write again: %s", text)
	}
	for _, want := range []string{"on disk", "read the file", "arguments were removed"} {
		if !strings.Contains(text, want) {
			t.Errorf("placeholder is missing %q: %s", want, text)
		}
	}

	// A reader keeps the original advice, which is correct for it.
	got, _ = editContext(history(6, 1000), cfg(), nil, 20_000)
	if !strings.Contains(resultText(got, 0), "Call the tool again") {
		t.Errorf("a cleared read lost its re-read advice: %s", resultText(got, 0))
	}
}

// payloadArgs names tools and fields with strings, so it can drift away from the
// tools without any compiler error — and the failure would be silent, which is the
// shape of bug this project has already shipped once (a .worktreeinclude pattern
// that matched nothing and reported nothing). So the table is driven against the
// real registry and the real schemas.
func TestPayloadArgsMatchTheRealTools(t *testing.T) {
	reg := tools.Default(t.TempDir())

	for name, fields := range payloadArgs {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("payloadArgs names %q, which is not a registered tool", name)
		}
		props := schemaProps(t, tool.InputSchema())

		// The path must exist and must not be in the blank list: it is what the
		// placeholder relies on surviving, and Anthropic's reason for keeping
		// arguments at all.
		if _, ok := props["path"]; !ok {
			t.Errorf("%s has no path property; the placeholder relies on it", name)
		}
		for _, f := range fields {
			if f == "path" {
				t.Errorf("%s would blank its own path", name)
			}
		}
	}

	// write: the payload is a top-level string.
	if _, ok := schemaProps(t, mustGet(t, reg, "write").InputSchema())["content"]; !ok {
		t.Error(`write has no "content" property; payloadArgs would blank nothing`)
	}

	// edit: the payload is nested inside the edits array. The flat oldText/newText
	// shorthand is also in the table and deliberately absent from the schema — see
	// Edit.ValidateArgs, which accepts it because models keep sending it.
	edits, ok := schemaProps(t, mustGet(t, reg, "edit").InputSchema())["edits"].(map[string]any)
	if !ok {
		t.Fatal(`edit has no "edits" property`)
	}
	items, ok := edits["items"].(map[string]any)
	if !ok {
		t.Fatal("edit.edits has no items schema")
	}
	itemProps, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatal("edit.edits.items has no properties")
	}
	for _, f := range []string{"oldText", "newText"} {
		if _, ok := itemProps[f]; !ok {
			t.Errorf("edit.edits.items has no %q; payloadArgs would blank nothing", f)
		}
	}

	// And the tools deliberately left out stay out: their arguments are the
	// description of the call, so blanking them removes the only thing worth having.
	for _, name := range []string{"read", "ls", "find", "grep", "bash", "todo"} {
		if _, ok := payloadArgs[name]; ok {
			t.Errorf("%s is in payloadArgs, but its arguments describe the call", name)
		}
	}
}

func mustGet(t *testing.T, reg *tools.Registry, name string) tools.Tool {
	t.Helper()
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("no %q tool in the default registry", name)
	}
	return tool
}

// schemaProps reads a tool's properties in either of the two forms the tools
// package emits: a plain map, or tools.orderedProps for the schemas whose property
// order is load-bearing — write and edit both declare path first on purpose, so the
// streaming preview can name the file before the content arrives. Going through
// JSON normalises the two without this test needing to know which is which.
func schemaProps(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(schema["properties"])
	if err != nil {
		t.Fatalf("schema properties do not marshal: %v", err)
	}
	var props map[string]any
	if err := json.Unmarshal(raw, &props); err != nil {
		t.Fatalf("schema has no property object: %s", raw)
	}
	return props
}

// --- what a cleared result costs to get back ---------------------------------
//
// The placeholder ends in advice a model acts on, so it has to be true for the tool
// it is attached to. Anthropic says one sentence to every cleared result, which is
// right when reads are the only clearable thing. A survey of this project's own
// transcripts found `git add -A && git commit`, `git cherry-pick <sha>` and a
// heredoc overwriting a config file among the bash calls — three cases where
// "call the tool again" invites a second side effect.

// resultFor builds a one-call history for the named tool, plus enough reads after it
// that the keep window does not spare the call under test.
func resultFor(tool string, tokens int64) []llm.Message {
	msgs := []llm.Message{
		llm.UserText("go"),
		{Role: llm.RoleAssistant, Content: []llm.Block{use("x", tool)}},
		{Role: llm.RoleUser, Content: []llm.Block{res("x", big(tokens))}},
	}
	return append(msgs, history(3, 100)[1:]...)
}

func TestPlaceholderAdviceMatchesWhatTheToolCostsToRepeat(t *testing.T) {
	cases := []struct {
		tool    string
		want    string
		mustNot string
		why     string
	}{
		{"read", "Call the tool again", "",
			"re-reading is the case the original wording was written for"},
		{"grep", "Call the tool again", "", "a search re-derives from current state"},
		{"write", "read the file", "Call the tool again",
			"repeating a write would rewrite the file with content the model no longer holds"},
		{"edit", "read the file", "Call the tool again",
			"repeating an edit fails: oldText is no longer in the file"},
		{"bash", "only if repeating it is safe", "Call the tool again",
			"real transcripts hold git commit, git cherry-pick and a config-overwriting heredoc"},
		{"subagent", "another full run", "Call the tool again",
			"the answer cost a whole child run, not a tool call"},
		{"someFutureTool", "if repeating the call is safe", "Call the tool again",
			"an unclassified tool must not inherit the encouraging wording"},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			got, _ := editContext(resultFor(tc.tool, 5_000), cfg(), nil, 50_000)
			text := resultText(got, 0)
			if !strings.Contains(text, tc.want) {
				t.Errorf("advice for %s should say %q (%s):\n  %s", tc.tool, tc.want, tc.why, text)
			}
			if tc.mustNot != "" && strings.Contains(text, tc.mustNot) {
				t.Errorf("advice for %s still says %q, but %s:\n  %s", tc.tool, tc.mustNot, tc.why, text)
			}
		})
	}
}

// reRun is keyed by string, so a new tool would silently inherit the zero value.
// That zero value is reRunFree, which is the one default capable of turning an
// eviction into an action — hence a guard rather than a convention.
func TestEveryToolIsClassifiedForRepeatCost(t *testing.T) {
	// The full set, including the two the default registry withholds by depth.
	reg := tools.New(tools.Options{Cwd: t.TempDir(), Subagent: &tools.Subagent{}})
	for _, tool := range reg.All() {
		name := tool.Name()
		if name == "todo" {
			// Pinned when newest, and a superseded list is a stale state nobody
			// should be invited to restore, so it never reaches an advice string.
			continue
		}
		if _, ok := reRun[name]; !ok {
			t.Errorf("tool %q has no entry in reRun, so a cleared result of it would "+
				"be told to call it again; decide what repeating it costs", name)
		}
	}

	// And the classification is not vacuous: the four costs must produce four
	// different sentences, or the table is decoration.
	seen := map[string]string{}
	for _, name := range []string{"read", "write", "bash", "subagent"} {
		a := advice(name)
		if other, dup := seen[a]; dup {
			t.Errorf("%s and %s give identical advice: %q", name, other, a)
		}
		seen[a] = name
	}
}
