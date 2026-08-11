package analyze

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yosukeno/pi-go/llm"
	"github.com/yosukeno/pi-go/session"
)

// The composition crosses three boundaries between being computed and being read:
// session.Stats marshals it, this package re-declares the record shape rather than
// importing the writer's, and FormatText renders it. A field added on one side and
// spelled differently on another would be silently absent, which is the failure
// this test exists to catch — the analyzer's Token Usage section shipped complete
// and unwritten once already.
func TestCompositionSurvivesTheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "/wd", "some-model")
	if err != nil {
		t.Fatal(err)
	}

	msgs := []llm.Message{
		llm.UserText(strings.Repeat("u", 400)),
		{Role: llm.RoleAssistant, Content: []llm.Block{
			{Type: llm.BlockText, Text: strings.Repeat("a", 200)},
			{Type: llm.BlockToolUse, ID: "t1", Name: "read", Input: json.RawMessage(`{"path":"a.go"}`)},
		}},
		{Role: llm.RoleUser, Content: []llm.Block{
			{Type: llm.BlockToolResult, ToolUseID: "t1", Text: strings.Repeat("r", 8000)},
		}},
	}
	if err := store.AppendAll(msgs); err != nil {
		t.Fatal(err)
	}
	comp := session.Compose(msgs, 1000)
	comp.Measured = 4321
	if err := store.AppendStats(session.Stats{
		Usage:       &session.UsageStats{Input: 4321, Output: 100},
		Composition: &comp,
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := AnalyzeSession(store.Path(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Composition == nil {
		t.Fatal("the composition did not survive the round trip")
	}
	got := *stats.Composition
	if got.Tools["read"] != 2000 {
		t.Errorf("Tools[read] = %d, want 2000; got %v", got.Tools["read"], got.Tools)
	}
	if got.Fixed != 1000 || got.User != 100 || got.Assistant != 50 {
		t.Errorf("breakdown lost in transit: %+v", got)
	}
	if got.Measured != 4321 {
		t.Errorf("Measured = %d, want 4321", got.Measured)
	}

	// And it has to reach the human-readable report, not just the JSON.
	text := FormatText(stats)
	for _, want := range []string{"Context Composition", "Tool results", "read", "estimate reads"} {
		if !strings.Contains(text, want) {
			t.Errorf("report is missing %q:\n%s", want, text)
		}
	}
}

// Overwritten, not accumulated — the opposite of every neighbouring field. Two
// records whose tool totals summed would report twice the tool output the session
// ever held.
func TestAnalyzerKeepsTheNewestCompositionOnly(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "/wd", "m")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{400, 4000} {
		msgs := []llm.Message{llm.UserText(strings.Repeat("u", n))}
		comp := session.Compose(msgs, 0)
		if err := store.AppendStats(session.Stats{
			Usage: &session.UsageStats{Input: 10}, Composition: &comp,
		}); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := AnalyzeSession(store.Path(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Composition == nil {
		t.Fatal("no composition")
	}
	// 1000 is the last snapshot alone. 1100 would be the two summed.
	if stats.Composition.User != 1000 {
		t.Errorf("User = %d, want 1000 (the newest snapshot, not the sum)", stats.Composition.User)
	}
}

// A transcript written before the field existed must report nothing rather than a
// row of zeroes: it did not measure this, and a zeroed breakdown reads as a fact
// about the session rather than as the absence of one.
func TestOlderTranscriptsReportNoComposition(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "/wd", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendStats(session.Stats{Usage: &session.UsageStats{Input: 100, Output: 20}}); err != nil {
		t.Fatal(err)
	}
	stats, err := AnalyzeSession(store.Path(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Composition != nil {
		t.Errorf("invented a composition: %+v", stats.Composition)
	}
	if strings.Contains(FormatText(stats), "Context Composition") {
		t.Error("the report shows an empty composition section")
	}
}

// The report used to state the direction as a constant: "estimate reads 0.83x low".
// 0.83 means the estimate reads *high*, and on this project's own transcripts that
// is the ordinary case — 0.98 for ASCII, 0.83 for Chinese-heavy text, because both
// providers tokenize Chinese more densely than four bytes per token assumes.
func TestCompositionReportNamesTheDirectionOfTheError(t *testing.T) {
	for _, tc := range []struct {
		name             string
		estimated, meas  int64
		want, mustNotSay string
	}{
		{"estimate reads high", 100_000, 83_000, "high", "low"},
		{"estimate reads low", 100_000, 130_000, "low", "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := FormatText(&SessionStats{Composition: &session.Composition{
				Fixed: 2_000, Estimated: tc.estimated, Measured: tc.meas, Messages: 10,
				Tools: map[string]int64{"read": tc.estimated - 2_000},
			}})
			if !strings.Contains(out, tc.want) {
				t.Errorf("report does not say %q:\n%s", tc.want, out)
			}
			// The wrong word must not appear on that line at all, or the reader
			// takes the number in the opposite direction and tightens a threshold
			// the wrong way.
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, "Provider's own count") &&
					strings.Contains(line, tc.mustNotSay) {
					t.Errorf("the count line says %q: %s", tc.mustNotSay, line)
				}
			}
		})
	}
}

// The cleared amount has to appear when it is non-zero, because it is why the
// estimate and the provider's count describe different bodies of text.
func TestCompositionReportNamesWhatClearingRemoved(t *testing.T) {
	with := FormatText(&SessionStats{Composition: &session.Composition{
		Fixed: 2_000, Estimated: 100_000, Cleared: 40_000, Measured: 60_000, Messages: 10,
		Tools: map[string]int64{"read": 98_000},
	}})
	if !strings.Contains(with, "Cleared from the last prompt") {
		t.Errorf("a session that cleared 40k does not say so:\n%s", with)
	}
	// And that correction is what makes the ratio read as 1.00 rather than 0.60.
	if !strings.Contains(with, "1.00") {
		t.Errorf("ratio was not corrected by the cleared amount:\n%s", with)
	}

	without := FormatText(&SessionStats{Composition: &session.Composition{
		Fixed: 2_000, Estimated: 100_000, Measured: 98_000, Messages: 10,
		Tools: map[string]int64{"read": 98_000},
	}})
	if strings.Contains(without, "Cleared from the last prompt") {
		t.Errorf("a session that cleared nothing mentions clearing:\n%s", without)
	}
}
