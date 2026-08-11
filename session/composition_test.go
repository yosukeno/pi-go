package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangy/pi-go/llm"
)

// text of exactly n bytes, so a test can assert token counts instead of ranges.
func text(n int) string { return strings.Repeat("x", n) }

func toolUse(id, name, input string) llm.Block {
	return llm.Block{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}
}

func toolResult(id, out string) llm.Block {
	return llm.Block{Type: llm.BlockToolResult, ToolUseID: id, Text: out}
}

// The comparison the whole record exists to make: tool output against
// conversation. If this number is wrong, every later decision about eviction
// versus summarisation is made on a wrong reading.
func TestComposeSplitsToolOutputFromConversation(t *testing.T) {
	msgs := []llm.Message{
		llm.UserText(text(400)),
		{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockText, Text: text(200)},
			toolUse("t1", "read", `{"path":"a.go"}`),
		}},
		{Role: llm.RoleUser, Content: []llm.Block{toolResult("t1", text(8000))}},
	}

	c := Compose(msgs, 1000)
	if c.Fixed != 1000 {
		t.Errorf("Fixed = %d, want the value passed in", c.Fixed)
	}
	if c.User != 100 {
		t.Errorf("User = %d, want 100 (400 bytes / %d)", c.User, BytesPerToken)
	}
	if c.Assistant != 50 {
		t.Errorf("Assistant = %d, want 50", c.Assistant)
	}
	if c.Tools["read"] != 2000 {
		t.Errorf("Tools[read] = %d, want 2000", c.Tools["read"])
	}
	if c.ToolTotal() != 2000 {
		t.Errorf("ToolTotal = %d, want 2000", c.ToolTotal())
	}
	if c.Messages != 3 {
		t.Errorf("Messages = %d, want 3", c.Messages)
	}
	// A tool result 40x the size of the conversation is precisely the shape that
	// says "evict, do not summarise", so the ratio has to survive the arithmetic.
	if c.ToolTotal() <= c.Assistant+c.User {
		t.Errorf("tool output %d did not dominate conversation %d", c.ToolTotal(), c.Assistant+c.User)
	}
}

// The parts must visibly add up to the total. A breakdown that disagrees with its
// own sum is the kind of thing a reader spends ten minutes on before deciding the
// whole report is untrustworthy.
func TestComposeTotalEqualsItsParts(t *testing.T) {
	msgs := []llm.Message{
		llm.UserText(text(37)),
		{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockText, Text: text(91)},
			toolUse("t1", "grep", `{"pattern":"x"}`),
			toolUse("t2", "bash", `{"command":"go test ./..."}`),
		}},
		{Role: llm.RoleUser, Content: []llm.Block{
			toolResult("t1", text(555)),
			toolResult("t2", text(1234)),
		}},
	}
	c := Compose(msgs, 777)
	sum := c.Fixed + c.User + c.Assistant + c.ToolArgs + c.ToolTotal()
	if c.Estimated != sum {
		t.Errorf("Estimated = %d but its parts sum to %d", c.Estimated, sum)
	}
}

// Thinking is never replayed (see llm/convert.go), so counting it would push the
// estimate permanently above the provider's count and make the calibration —
// the one number that says how wrong the divisor is — useless.
func TestComposeIgnoresThinking(t *testing.T) {
	withThinking := []llm.Message{{Role: llm.RoleAssistant, Content: []llm.Block{
		{Type: llm.BlockThinking, Text: text(40000)},
		{Type: llm.BlockText, Text: text(100)},
	}}}
	without := []llm.Message{{Role: llm.RoleAssistant, Content: []llm.Block{
		{Type: llm.BlockText, Text: text(100)},
	}}}
	if a, b := Compose(withThinking, 0), Compose(without, 0); a.Estimated != b.Estimated {
		t.Errorf("thinking changed the estimate: %d vs %d", a.Estimated, b.Estimated)
	}
}

// Details is display data that convert.go has no path to send, so it must not be
// counted either. Same reasoning as thinking, different mechanism.
func TestComposeIgnoresDetails(t *testing.T) {
	bare := toolResult("t1", text(80))
	fat := bare
	fat.Details = json.RawMessage(`{"diff":"` + text(20000) + `"}`)

	msgs := func(b llm.Block) []llm.Message {
		return []llm.Message{
			{Role: llm.RoleAssistant, Content: []llm.Block{toolUse("t1", "edit", `{}`)}},
			{Role: llm.RoleUser, Content: []llm.Block{b}},
		}
	}
	if a, b := Compose(msgs(bare), 0), Compose(msgs(fat), 0); a.Estimated != b.Estimated {
		t.Errorf("Details changed the estimate: %d vs %d", a.Estimated, b.Estimated)
	}
}

// A result block carries only the call id, so the tool name has to be recovered
// by pairing it back to the tool_use — the same back-fill the web Hub does. Two
// tools in one batch is the case that catches a lookup keyed on anything else.
func TestComposeAttributesResultsToTheRightTool(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.Block{
			toolUse("a", "read", `{}`),
			toolUse("b", "bash", `{}`),
		}},
		// Deliberately out of call order: the batch runs in parallel and finishes in
		// whatever order it finishes.
		{Role: llm.RoleUser, Content: []llm.Block{
			toolResult("b", text(400)),
			toolResult("a", text(80)),
		}},
	}
	c := Compose(msgs, 0)
	if c.Tools["bash"] != 100 || c.Tools["read"] != 20 {
		t.Errorf("attribution wrong: %v", c.Tools)
	}
}

// A damaged transcript should still be measurable. Dropping the bytes would make
// the shares add up to less than the whole for no visible reason.
func TestComposeBucketsOrphanedResults(t *testing.T) {
	msgs := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{toolResult("gone", text(200))}}}
	c := Compose(msgs, 0)
	if c.Tools[unknownTool] != 50 {
		t.Errorf("orphaned result went missing: %v", c.Tools)
	}
	if c.Estimated != 50 {
		t.Errorf("Estimated = %d, want the orphaned bytes counted", c.Estimated)
	}
}

// Calibration is the reason Measured is recorded at all: four bytes per token is
// roughly right for English and off by about 2.5x for Chinese, so without it every
// share here would be an unfalsifiable claim.
func TestCalibration(t *testing.T) {
	c := Compose([]llm.Message{llm.UserText(text(4000))}, 0)
	if c.Estimated != 1000 {
		t.Fatalf("Estimated = %d, want 1000", c.Estimated)
	}
	if _, ok := c.Calibration(); ok {
		t.Error("calibrated against no measurement")
	}
	c.Measured = 2500
	ratio, ok := c.Calibration()
	if !ok || ratio != 2.5 {
		t.Errorf("Calibration = %v, %v; want 2.5, true", ratio, ok)
	}
}

// An empty history is not an error, and it must not divide by zero.
func TestComposeEmptyHistory(t *testing.T) {
	c := Compose(nil, 500)
	if c.Estimated != 500 || c.Messages != 0 || c.Tools != nil {
		t.Errorf("empty history gave %+v", c)
	}
	if _, ok := c.Calibration(); ok {
		t.Error("calibrated with no measurement")
	}
}

// The field is omitted from a record that has nothing to say, so an old transcript
// stays byte-identical rather than gaining a block of zeroes.
func TestCompositionOmitsEmptyFields(t *testing.T) {
	raw, err := json.Marshal(Stats{Usage: &UsageStats{Input: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "composition") {
		t.Errorf("a stats record with no composition mentions one: %s", raw)
	}
}

// Written down because the surrounding convention is the opposite: every other
// field in Stats is a delta the analyzer sums, and this one is a snapshot. Two
// records from the same session must not be addable into something meaningful.
func TestCompositionIsASnapshotNotADelta(t *testing.T) {
	first := Compose([]llm.Message{llm.UserText(text(400))}, 100)
	// The history grew; the second snapshot describes all of it, not the increment.
	second := Compose([]llm.Message{llm.UserText(text(400)), llm.UserText(text(400))}, 100)
	if second.User != first.User*2 {
		t.Fatalf("expected the second snapshot to cover the whole history: %d vs %d", second.User, first.User)
	}
	if second.Fixed != first.Fixed {
		t.Errorf("Fixed is per-turn, not cumulative: %d vs %d", second.Fixed, first.Fixed)
	}
}

// Calibration compares the provider's count for the last prompt against an
// estimate of that prompt. Once clearing is in play, the history and the prompt are
// different content, and a ratio over different content is not a ratio.
func TestCalibrationAccountsForWhatClearingRemoved(t *testing.T) {
	// 40,000 estimated in the history, 15,000 of it blanked before sending, and the
	// provider counted 25,000 — a divisor that was exactly right.
	c := Composition{Estimated: 40_000, Cleared: 15_000, Measured: 25_000}
	ratio, ok := c.Calibration()
	if !ok {
		t.Fatal("no ratio reported")
	}
	if ratio != 1 {
		t.Errorf("ratio = %.2f, want 1.00: the estimate was right about what was sent", ratio)
	}

	// Ignoring Cleared is the bug this field exists to prevent: it would report the
	// estimate as 60% high and send the reader after the divisor.
	uncorrected := float64(c.Measured) / float64(c.Estimated)
	if uncorrected > 0.7 {
		t.Fatalf("this test proves nothing: uncorrected ratio %.2f is already near 1", uncorrected)
	}
}

// No clearing, no correction: the ordinary case has to be untouched.
func TestCalibrationWithoutClearingIsTheRawRatio(t *testing.T) {
	c := Composition{Estimated: 10_000, Measured: 9_700}
	ratio, ok := c.Calibration()
	if !ok || ratio != 0.97 {
		t.Errorf("ratio = %.4f ok=%v, want 0.97", ratio, ok)
	}
}

// Below 1 is the normal direction on these providers, not an error to be clamped:
// both endpoints tokenize Chinese efficiently, so four bytes per token overstates.
// Measured across this project's transcripts: 0.98 ASCII, 0.83 Chinese-heavy.
func TestCalibrationReportsRatiosBelowOne(t *testing.T) {
	for _, tc := range []struct {
		name string
		comp Composition
		want float64
	}{
		{"chinese heavy", Composition{Estimated: 100_000, Measured: 83_000}, 0.83},
		{"mostly ascii", Composition{Estimated: 100_000, Measured: 98_000}, 0.98},
		{"estimate low", Composition{Estimated: 100_000, Measured: 111_000}, 1.11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.comp.Calibration()
			if !ok {
				t.Fatal("no ratio reported")
			}
			if got != tc.want {
				t.Errorf("ratio = %.2f, want %.2f", got, tc.want)
			}
		})
	}
}

// Clearing that accounts for the whole estimate leaves nothing to calibrate
// against, and inventing a ratio from a zero or negative denominator is worse than
// saying nothing.
func TestCalibrationRefusesWhenNothingWasLeftToSend(t *testing.T) {
	for _, c := range []Composition{
		{Estimated: 10_000, Cleared: 10_000, Measured: 500},
		{Estimated: 10_000, Cleared: 12_000, Measured: 500},
		{Estimated: 10_000, Measured: 0},
		{Measured: 500},
	} {
		if _, ok := c.Calibration(); ok {
			t.Errorf("reported a ratio for %+v", c)
		}
	}
}
