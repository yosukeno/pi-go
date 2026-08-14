package agent

import (
	"strings"
	"testing"
)

// The system prompt is sent on every turn of every session, so its size is a
// standing cost and not a one-off. This ceiling exists to make that cost a decision
// instead of a drift: SystemPrompt's own header says long prompts crowd out the task,
// and the batching paragraph was added under exactly that argument, so the next
// paragraph should have to argue past a failing test rather than past nobody.
//
// The number is deliberately close to the current size. A ceiling with room to spare
// is a ceiling nobody notices moving.
func TestSystemPromptStaysSmall(t *testing.T) {
	const ceiling = 1400
	p := SystemPrompt(t.TempDir())
	t.Logf("system prompt is %d bytes", len(p))
	if len(p) > ceiling {
		t.Errorf("system prompt is %d bytes, over the %d ceiling. Adding to it is allowed — "+
			"raise this deliberately and say what the new text buys", len(p), ceiling)
	}
}

// The batching guidance has to say both halves. "Do independent work in one call" on
// its own reads as "batch everything", and a call whose arguments come from another
// call's output cannot be batched — a model that tries spends a turn producing wrong
// arguments, which is worse than the round trip it was trying to save.
func TestSystemPromptSaysWhatCannotBeBatched(t *testing.T) {
	p := SystemPrompt(t.TempDir())
	if !strings.Contains(p, "round trip") {
		t.Error("the prompt does not say why batching matters")
	}
	if !strings.Contains(p, "depend") {
		t.Error("the prompt invites batching without naming the case that cannot be batched")
	}
}

// Guidance that belongs to one tool must not be in the prompt, because the prompt is
// sent whether or not that tool is registered. A read-only subagent has no bash, and
// telling it about a shell it cannot reach is both a lie and a tax on every turn.
// This is the same rule the todo tool's description follows; see the note in
// SystemPrompt.
func TestSystemPromptLeavesToolSpecificsToTools(t *testing.T) {
	p := SystemPrompt(t.TempDir())
	for _, leak := range []string{"cd ", "bash -c", "timeout", "&&"} {
		if strings.Contains(p, leak) {
			t.Errorf("the prompt carries %q, which belongs in a tool description", leak)
		}
	}
}
