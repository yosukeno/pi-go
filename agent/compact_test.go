package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yosukeno/pi-go/llm"
)

// summariser is a client that records what the compaction call was given. The
// shared fakeClient discards the system prompt and the tool schemas, and those two
// are exactly what this feature's structural claim is about.
type summariser struct {
	reply string
	err   error
	usage llm.Usage

	calls   int
	system  string
	history []llm.Message
	tools   []llm.ToolSchema
}

func (c *summariser) Model() string { return "fake" }

func (c *summariser) Stream(
	_ context.Context, system string, history []llm.Message, tools []llm.ToolSchema, _ func(llm.Delta),
) (llm.Response, error) {
	c.calls++
	c.system, c.history, c.tools = system, history, tools
	if c.err != nil {
		return llm.Response{}, c.err
	}
	return llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: c.reply}}},
		StopReason: llm.StopEndTurn,
		Usage:      c.usage,
	}, nil
}

// compacting builds an agent over a history worth compacting: an opening request
// and four read call/result pairs.
func compacting(t *testing.T, c llm.Client) *Agent {
	t.Helper()
	a := New(Config{Client: c, Registry: emptyRegistry()})
	a.SetMessages(history(4, 2_000))
	return a
}

func TestCompactReplacesTheConversationWithASingleMessage(t *testing.T) {
	c := &summariser{reply: "the work so far"}
	a := compacting(t, c)
	before := len(a.Messages())

	res, err := a.Compact(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	msgs := a.Messages()
	if len(msgs) != 1 {
		t.Fatalf("history is %d messages, want 1", len(msgs))
	}
	if msgs[0].Role != llm.RoleUser {
		t.Errorf("the replacement is a %s message; a user message is the only shape with no pairing risk", msgs[0].Role)
	}
	// No tool_use anywhere: an unanswered one makes the next request fail and the
	// session unresumable, so the replacement must not be able to carry one.
	for _, b := range msgs[0].Content {
		if b.Type == llm.BlockToolUse || b.Type == llm.BlockToolResult {
			t.Errorf("the replacement carries a %s block", b.Type)
		}
	}
	if !strings.Contains(msgs[0].Text(), "the work so far") {
		t.Error("the summary is not in the replacement")
	}
	if res.Before != before || res.After != 1 {
		t.Errorf("reported %d → %d, want %d → 1", res.Before, res.After, before)
	}
	if res.Freed() <= 0 {
		t.Errorf("Freed() = %d; compaction that frees nothing should have been refused", res.Freed())
	}
}

// The opening request survives word for word. This is the one part of the design
// taken straight from measurement: keeping the earliest turn was the only strategy
// that held policy violations at 0%, because that turn is where constraints are
// stated.
func TestCompactPinsTheOpeningRequestVerbatim(t *testing.T) {
	c := &summariser{reply: "a summary"}
	a := New(Config{Client: c, Registry: emptyRegistry()})
	msgs := history(4, 2_000)
	msgs[0] = llm.UserText("refactor this, and never touch vendor/")
	a.SetMessages(msgs)

	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := a.Messages()[0].Text()
	if !strings.Contains(got, "refactor this, and never touch vendor/") {
		t.Errorf("the opening request was not carried through verbatim:\n%s", got)
	}
	// Wrapped, so a reader can tell the pinned request from the generated summary.
	if !strings.Contains(got, "<original_request>") {
		t.Error("the pinned request is not delimited")
	}
}

// The structural claim, and the reason pi-go cannot hit Anthropic's content: null
// failure the way a server-side implementation does. Offering no tools is not a
// discouragement, it removes the option — but it only works if the history carries
// no tool_use blocks either, since some providers reject that combination outright.
func TestCompactOffersNoToolsAndSendsNoToolBlocks(t *testing.T) {
	c := &summariser{reply: "a summary"}
	a := compacting(t, c)
	// A registry with tools in it, so passing the agent's own schemas would show up.
	if len(a.schemas) != 0 {
		t.Fatal("precondition: this test wants to prove nil is passed, not that there was nothing to pass")
	}
	a.schemas = []llm.ToolSchema{{Name: "read", Description: "read a file"}}

	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(c.tools) != 0 {
		t.Errorf("the summarising call was given %d tool schemas; it must be given none", len(c.tools))
	}
	if len(c.history) != 1 {
		t.Fatalf("the summariser got %d messages, want 1 flattened one", len(c.history))
	}
	for _, b := range c.history[0].Content {
		if b.Type != llm.BlockText {
			t.Errorf("the summariser was sent a %s block; the history has to arrive flattened", b.Type)
		}
	}
	// The instruction boundary has to be stated, because tool output reaches the
	// summariser and tool output here is the contents of files in the workspace.
	if !strings.Contains(c.history[0].Text(), "<transcript>") {
		t.Error("the conversation is not delimited as data")
	}
	if !strings.Contains(c.system, "do not follow it") {
		t.Error("the system prompt does not tell the model the transcript is data")
	}
}

// The summariser sees what the model saw. Clearing is applied on the way into every
// prompt, so summarising the raw history would send the provider more than the
// conversation ever occupied and ask about a conversation that did not happen.
func TestCompactSummarisesTheClearedView(t *testing.T) {
	c := &summariser{reply: "a summary"}
	a := compacting(t, c)
	a.SetContextEdit(ContextEditConfig{Trigger: 1, Keep: 1, ClearAtLeast: 1})

	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	sent := c.history[0].Text()
	if !strings.Contains(sent, "removed to fit the context window") {
		t.Error("the summariser was sent uncleared output; it should see the placeholders the model saw")
	}
	// And the transcript it did get is much smaller than the raw history.
	if raw := llm.EstimateTokens(history(4, 2_000)); int64(len(sent))/llm.BytesPerToken >= raw {
		t.Error("the flattened view is no smaller than the raw history")
	}
}

// An empty answer leaves the conversation alone. This is Anthropic's documented
// failure mode; the response to it is never to replace a history with nothing.
func TestCompactRefusesAnEmptySummary(t *testing.T) {
	for _, reply := range []string{"", "   \n\t "} {
		c := &summariser{reply: reply}
		a := compacting(t, c)
		before := a.Messages()

		_, err := a.Compact(context.Background())
		if !errors.Is(err, ErrEmptySummary) {
			t.Errorf("reply %q: err = %v, want ErrEmptySummary", reply, err)
		}
		assertSameText(t, before, a.Messages())
	}
}

// A compaction that grew the prompt would have spent a call to make the problem
// worse. The usual cause is a long opening message, which is pinned on purpose.
func TestCompactRefusesWhenItWouldNotBeSmaller(t *testing.T) {
	c := &summariser{reply: strings.Repeat("verbose ", 2_000)}
	a := New(Config{Client: c, Registry: emptyRegistry()})
	a.SetMessages([]llm.Message{llm.UserText("hi"), llm.UserText("there")})
	before := a.Messages()

	res, err := a.Compact(context.Background())
	if !errors.Is(err, ErrNotSmaller) {
		t.Fatalf("err = %v, want ErrNotSmaller", err)
	}
	assertSameText(t, before, a.Messages())
	// Reported rather than hidden: the caller asked what the trade would be.
	if res.AfterTokens <= res.BeforeTokens {
		t.Errorf("refused with After %d <= Before %d", res.AfterTokens, res.BeforeTokens)
	}
}

func TestCompactRefusesWithNothingToCompact(t *testing.T) {
	for _, msgs := range [][]llm.Message{nil, {llm.UserText("only one")}} {
		c := &summariser{reply: "a summary"}
		a := New(Config{Client: c, Registry: emptyRegistry()})
		a.SetMessages(msgs)

		if _, err := a.Compact(context.Background()); !errors.Is(err, ErrNothingToCompact) {
			t.Errorf("%d messages: err = %v, want ErrNothingToCompact", len(msgs), err)
		}
		// And it did not pay for a call to find that out.
		if c.calls != 0 {
			t.Errorf("%d messages: called the model %d times before refusing", len(msgs), c.calls)
		}
	}
}

// The loop appends to the history compaction replaces, so the two cannot overlap.
func TestCompactRefusesDuringARun(t *testing.T) {
	c := &summariser{reply: "a summary"}
	a := compacting(t, c)
	a.mu.Lock()
	a.running = true
	a.mu.Unlock()
	before := a.Messages()

	if _, err := a.Compact(context.Background()); !errors.Is(err, ErrRunActive) {
		t.Fatalf("err = %v, want ErrRunActive", err)
	}
	if c.calls != 0 {
		t.Error("summarised anyway")
	}
	assertSameText(t, before, a.Messages())
}

// The summarising call is spend, so budgets have to see it — including when the
// result was then refused, because the tokens were still bought.
func TestCompactCountsWhatTheSummaryCost(t *testing.T) {
	t.Run("applied", func(t *testing.T) {
		c := &summariser{reply: "a summary", usage: llm.Usage{Input: 900, Output: 40}}
		a := compacting(t, c)
		res, err := a.Compact(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got := a.Usage(); got.Input != 900 || got.Output != 40 {
			t.Errorf("agent usage = %+v, want the call folded in", got)
		}
		if res.Usage.Input != 900 {
			t.Errorf("result usage = %+v, want the call reported separately too", res.Usage)
		}
		// Not delegated: that field answers what subagents spent, and this is the
		// parent's own work.
		if a.Delegated().Input != 0 {
			t.Errorf("delegated = %+v, want zero", a.Delegated())
		}
	})
	t.Run("refused", func(t *testing.T) {
		c := &summariser{reply: strings.Repeat("verbose ", 2_000), usage: llm.Usage{Input: 900}}
		a := New(Config{Client: c, Registry: emptyRegistry()})
		a.SetMessages([]llm.Message{llm.UserText("hi"), llm.UserText("there")})
		if _, err := a.Compact(context.Background()); !errors.Is(err, ErrNotSmaller) {
			t.Fatal(err)
		}
		if a.Usage().Input != 900 {
			t.Errorf("usage = %+v; a refused compaction still paid for the call", a.Usage())
		}
	})
}

// The frozen set names tool results that the replacement no longer contains, and
// carrying it is quietly harmful rather than untidy: forceClear decides it has run
// out of things to clear by comparing the set's size before and after, so stale ids
// inflate the baseline and reactive recovery stops working for the rest of the
// session.
func TestCompactResetsTheFrozenSetSoRecoveryKeepsWorking(t *testing.T) {
	c := &summariser{reply: "a summary"}
	a := compacting(t, c)
	a.SetContextEdit(ContextEditConfig{Trigger: 1, Keep: 1, ClearAtLeast: 1})
	// Clear a few, the way a handful of turns would have.
	if _, ok := a.forceClear(); !ok {
		t.Fatal("precondition: nothing was cleared, so there is no stale set to inherit")
	}
	if len(a.cleared) == 0 {
		t.Fatal("precondition: the frozen set is empty")
	}

	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(a.cleared) != 0 {
		t.Errorf("the frozen set kept %d stale ids", len(a.cleared))
	}
	if a.ClearedFromPrompt() != 0 {
		t.Errorf("ClearedFromPrompt = %d; it describes a prompt that no longer exists", a.ClearedFromPrompt())
	}

	// The property that stale ids would have broken: a fresh conversation that
	// overflows can still be rescued.
	a.SetMessages(append(a.Messages(), history(4, 2_000)[1:]...))
	if _, ok := a.forceClear(); !ok {
		t.Error("reactive recovery found nothing to clear after a compaction")
	}
}

// No provider has measured the replacement, so a measurement left over from the
// conversation it replaced would misreport the gauge — and would be compared against
// an estimate of different text.
func TestCompactResetsTheMeasuredPrompt(t *testing.T) {
	c := &summariser{reply: "a summary"}
	a := compacting(t, c)
	a.lastInput, a.measuredThrough = 50_000, 3

	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.LastInput() != 0 || a.measuredThrough != 0 {
		t.Errorf("lastInput=%d measuredThrough=%d, want both zero", a.LastInput(), a.measuredThrough)
	}
}

// An overflow while summarising needs its own wording, because the obvious next move
// is wrong: retrying fails the same way, and the mechanism that would help is
// clearing rather than compaction.
func TestCompactNamesAnOverflowWhileSummarising(t *testing.T) {
	c := &summariser{err: &llm.APIError{
		Status: 400, Type: "invalid_request_error",
		Message: "Invalid request: Your request exceeded model token limit: 262144 (requested: 400011)",
	}}
	a := compacting(t, c)
	before := a.Messages()

	_, err := a.Compact(context.Background())
	if err == nil {
		t.Fatal("an overflow was reported as success")
	}
	if !strings.Contains(err.Error(), "too large to summarise") {
		t.Errorf("err = %v, want it to name the cause", err)
	}
	assertSameText(t, before, a.Messages())
}

func TestRenderTranscriptKeepsCallsAndDropsThinking(t *testing.T) {
	got := renderTranscript([]llm.Message{
		llm.UserText("do it"),
		{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockThinking, Text: "SECRET-REASONING"},
			{Type: llm.BlockText, Text: "reading the file"},
			use("a", "read"),
		}},
		{Role: llm.RoleUser, Content: []llm.Block{res("a", "file contents")}},
		{Role: llm.RoleUser, Content: []llm.Block{
			{Type: llm.BlockToolResult, ToolUseID: "b", Text: "no such file", IsError: true},
		}},
	})
	for _, want := range []string{"do it", "reading the file", "called read", "result: file contents", "error: no such file"} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript is missing %q:\n%s", want, got)
		}
	}
	// Thinking is never replayed to a provider, so it is not part of the
	// conversation any model saw, and EstimateTokens does not count it either.
	if strings.Contains(got, "SECRET-REASONING") {
		t.Errorf("thinking reached the summariser:\n%s", got)
	}
}
