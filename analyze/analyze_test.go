package analyze

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fourTurnSession writes a transcript shaped the way a real one is: a meta record,
// then turns that alternate assistant messages with the user message carrying their
// tool results, each followed by the stats record for that turn.
//
// It is built here rather than read from a fixture path because this test used to
// open /tmp/test-session-with-stats.jsonl — a file outside the repository, which
// means it could only ever pass on the machine that happened to still have it. It
// had been failing on any fresh checkout, and a test that depends on the state of
// /tmp is not testing the analyzer.
//
// The shape carries one property on purpose: **every stats record here follows a
// user message, not an assistant one.** That is what a turn ending in tool results
// looks like, and the aggregation used to reach stats only from the assistant branch
// — so exactly these records were dropped and the session reported a fraction of its
// real cost. See the note on the "stats" case in AnalyzeSession.
func fourTurnSession(t *testing.T) string {
	t.Helper()
	lines := []string{
		`{"id":"r0","type":"meta","time":1000,"meta":{"cwd":"/w","model":"glm-5.2"}}`,
		`{"id":"r1","parentId":"r0","type":"message","time":1010,"message":{"role":"user",` +
			`"content":[{"type":"text"}]}}`,

		// Turn 1: one tool call.
		`{"id":"r2","parentId":"r1","type":"message","time":1100,"message":{"role":"assistant",` +
			`"content":[{"type":"text"},{"type":"tool_use","name":"read"}]}}`,
		`{"id":"r3","parentId":"r2","type":"message","time":1150,"message":{"role":"user",` +
			`"content":[{"type":"tool_result"}]}}`,
		`{"id":"r4","parentId":"r3","type":"stats","time":1160,"stats":{"usage":{"input":100,"output":20},` +
			`"tools":{"call_count":1,"duration":40,"exec_time":[{"name":"read","duration":40}]}}}`,

		// Turn 2: two tool calls in one batch, and a retry.
		`{"id":"r5","parentId":"r4","type":"message","time":1200,"message":{"role":"assistant",` +
			`"content":[{"type":"tool_use","name":"read"},{"type":"tool_use","name":"grep"}]}}`,
		`{"id":"r6","parentId":"r5","type":"message","time":1260,"message":{"role":"user",` +
			`"content":[{"type":"tool_result"},{"type":"tool_result"}]}}`,
		`{"id":"r7","parentId":"r6","type":"stats","time":1270,"stats":{"usage":{"input":200,"output":60},` +
			`"tools":{"call_count":2,"duration":90,"exec_time":[{"name":"read","duration":30},` +
			`{"name":"grep","duration":60}]},"retry":{"count":1,"reason":"429 rate limited"}}}`,

		// Turn 3: one tool call, another retry.
		`{"id":"r8","parentId":"r7","type":"message","time":1300,"message":{"role":"assistant",` +
			`"content":[{"type":"tool_use","name":"bash"}]}}`,
		`{"id":"r9","parentId":"r8","type":"message","time":1400,"message":{"role":"user",` +
			`"content":[{"type":"tool_result"}]}}`,
		`{"id":"r10","parentId":"r9","type":"stats","time":1410,"stats":{"usage":{"input":250,"output":70},` +
			`"tools":{"call_count":1,"duration":120,"exec_time":[{"name":"bash","duration":120}]},` +
			`"retry":{"count":1,"reason":"503 upstream"}}}`,

		// Turn 4: the answer, no tools.
		`{"id":"r11","parentId":"r10","type":"message","time":1500,"message":{"role":"assistant",` +
			`"content":[{"type":"text"}]}}`,
		`{"id":"r12","parentId":"r11","type":"stats","time":1510,"stats":{"usage":{"input":80,"output":50}}}`,
	}
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAnalyzeSession(t *testing.T) {
	stats, err := AnalyzeSession(fourTurnSession(t), Config{})
	if err != nil {
		t.Fatalf("AnalyzeSession failed: %v", err)
	}

	// A round is an assistant message; the fixture has four.
	if stats.RoundCount != 4 {
		t.Errorf("RoundCount = %d, want 4", stats.RoundCount)
	}
	// Summed across all four stats records: 100+200+250+80 and 20+60+70+50.
	if got := stats.TokenUsage.TotalInput; got != 630 {
		t.Errorf("TotalInput = %d, want 630", got)
	}
	if got := stats.TokenUsage.TotalOutput; got != 200 {
		t.Errorf("TotalOutput = %d, want 200", got)
	}
	// Averages are per round, not per stats record — the same four turns.
	if got := stats.TokenUsage.AverageInput; got != 157.5 {
		t.Errorf("AverageInput = %v, want 157.5", got)
	}
	// Reported call counts: 1 + 2 + 1.
	if got := stats.ToolTiming.TotalCalls; got != 4 {
		t.Errorf("ToolTiming.TotalCalls = %d, want 4", got)
	}
	// Batches come from the tool_use blocks, so they are counted independently of
	// what the stats records claim. Turns 1 and 3 had one call, turn 2 had two, and
	// turn 4 had none — a turn with no calls must not count as a batch of zero.
	b := stats.BatchSizes
	if b.Singles != 2 || b.Doubles != 1 || b.Multiple != 0 || b.TotalCalls != 4 {
		t.Errorf("batches = %+v, want singles 2, doubles 1, multiple 0, total 4", b)
	}
	// Both retries, each attributed to the turn it followed.
	if got := stats.RetryStats.TotalRetries; got != 2 {
		t.Errorf("TotalRetries = %d, want 2", got)
	}
	if len(stats.RetryStats.Reasons) != 2 {
		t.Errorf("retry reasons = %v, want both kept apart", stats.RetryStats.Reasons)
	}
	// First to last record.
	if stats.Duration != 510 {
		t.Errorf("Duration = %d, want 510", stats.Duration)
	}
}

// The bug the fixture is shaped to catch, asserted directly so it cannot come back
// quietly: a stats record is aggregated wherever it sits. Turns that end in tool
// results end with a user message, so requiring adjacency to an assistant message
// dropped their cost — and a session total that depends on record ordering reads as
// a fact rather than as the absence of one.
func TestAnalyzeCountsStatsThatDoNotFollowAnAssistantMessage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	lines := []string{
		`{"id":"1","type":"meta","time":1,"meta":{"cwd":"/w","model":"m"}}`,
		`{"id":"2","parentId":"1","type":"message","time":2,"message":{"role":"assistant",` +
			`"content":[{"type":"tool_use","name":"read"}]}}`,
		// The turn's results, then its cost. Nothing here is adjacent to an assistant
		// message, which is the whole point.
		`{"id":"3","parentId":"2","type":"message","time":3,"message":{"role":"user",` +
			`"content":[{"type":"tool_result"}]}}`,
		`{"id":"4","parentId":"3","type":"stats","time":4,"stats":{"usage":{"input":777,"output":88}}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stats, err := AnalyzeSession(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.TokenUsage.TotalInput != 777 || stats.TokenUsage.TotalOutput != 88 {
		t.Errorf("totals = %d/%d, want 777/88: a turn ending in tool results still cost something",
			stats.TokenUsage.TotalInput, stats.TokenUsage.TotalOutput)
	}
}

func TestAnalyzeEmptySession(t *testing.T) {
	// Create a temporary empty file
	tmpfile, err := os.CreateTemp("", "empty-session-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())
	tmpfile.Close()

	config := Config{
		IncludeTurns: false,
	}

	stats, err := AnalyzeSession(tmpfile.Name(), config)
	if err != nil {
		t.Fatalf("AnalyzeSession failed on empty file: %v", err)
	}

	if stats.RoundCount != 0 {
		t.Errorf("Expected 0 rounds for empty session, got %d", stats.RoundCount)
	}
}

func TestAnalyzeCorruptedSession(t *testing.T) {
	// Create a file with some corrupted lines
	tmpfile, err := os.CreateTemp("", "corrupted-session-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	// Write mix of valid and invalid JSON
	content := `{"id":"1","type":"meta","time":1000,"meta":{"cwd":"/test"}}
invalid json line
{"id":"2","type":"message","time":2000,"message":{"role":"user","content":[{"type":"text","text":"hello"}]}}
`
	if _, err := tmpfile.WriteString(content); err != nil {
		t.Fatal(err)
	}
	tmpfile.Close()

	config := Config{
		IncludeTurns: false,
	}

	stats, err := AnalyzeSession(tmpfile.Name(), config)
	if err != nil {
		t.Fatalf("AnalyzeSession failed on corrupted file: %v", err)
	}

	// Should skip invalid lines and process valid ones
	if stats.RoundCount != 0 {
		t.Errorf("Expected 0 rounds for file with only user message, got %d", stats.RoundCount)
	}
}

func TestFormatText(t *testing.T) {
	stats := &SessionStats{
		SessionPath: "/test/session.jsonl",
		RoundCount:  10,
		Duration:    60000, // 1 minute
		TokenUsage:  TokenAnalysis{TotalInput: 1000, TotalOutput: 500},
		ToolTiming:  ToolTimingAnalysis{TotalCalls: 5, TotalDuration: 2000},
	}

	output := FormatText(stats)
	if output == "" {
		t.Error("FormatText returned empty string")
	}

	// Check for key sections in output
	if !contains(output, "Session Analysis") {
		t.Error("FormatText output missing 'Session Analysis' header")
	}
	if !contains(output, "Token Usage") {
		t.Error("FormatText output missing 'Token Usage' section")
	}
}

func TestFormatJSON(t *testing.T) {
	stats := &SessionStats{
		SessionPath: "/test/session.jsonl",
		RoundCount:  5,
	}

	output, err := FormatJSON(stats)
	if err != nil {
		t.Fatalf("FormatJSON failed: %v", err)
	}

	if output == "" {
		t.Error("FormatJSON returned empty string")
	}

	// Verify it's valid JSON
	if !contains(output, "{") || !contains(output, "}") {
		t.Error("FormatJSON output doesn't look like JSON")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && findSubstring(s, substr) >= 0
}

func findSubstring(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// The delegated split. Before it, a parent's report was one total and the only way to
// attribute a delegation's cost was to find each child's transcript and analyse it
// separately — while adding the two together silently double-counted, because a
// parent's counters already include what its subagents spent.
func TestAnalyzeSeparatesDelegatedSpending(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	lines := []string{
		`{"id":"1","type":"meta","time":1,"meta":{"cwd":"/tmp","model":"m"}}`,
		// A turn the agent spent entirely itself.
		`{"id":"2","type":"stats","time":2,"stats":{"usage":{"input":100,"output":10}}}`,
		// A turn where a subagent spent 400 of the 500.
		`{"id":"3","type":"stats","time":3,"stats":{"usage":{"input":500,"output":60},` +
			`"delegated":{"input":400,"output":40}}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stats, err := AnalyzeSession(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	u := stats.TokenUsage
	if u.TotalInput != 600 || u.TotalOutput != 70 {
		t.Errorf("totals = %d/%d, want 600/70 (the all-in cost)", u.TotalInput, u.TotalOutput)
	}
	if u.DelegatedInput != 400 || u.DelegatedOutput != 40 {
		t.Errorf("delegated = %d/%d, want 400/40", u.DelegatedInput, u.DelegatedOutput)
	}
	// Subtracting is the arithmetic that works; adding would count 400 twice.
	if own := u.TotalInput - u.DelegatedInput; own != 200 {
		t.Errorf("own input = %d, want 200", own)
	}

	out := FormatText(stats)
	for _, want := range []string{"delegated to subagents: 400 in", "by this agent itself:      200 in"} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
}

// A session that never delegated says nothing about delegation. Putting a
// subagent-shaped line in front of everyone who never used one is noise, and a
// transcript written before the field existed reads as zero, which is correct for it.
func TestAnalyzeStaysQuietWithoutDelegation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	lines := []string{
		`{"id":"1","type":"meta","time":1,"meta":{"cwd":"/tmp","model":"m"}}`,
		`{"id":"2","type":"stats","time":2,"stats":{"usage":{"input":100,"output":10}}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stats, err := AnalyzeSession(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.TokenUsage.DelegatedInput != 0 {
		t.Errorf("delegated = %d with no delegated records", stats.TokenUsage.DelegatedInput)
	}
	if out := FormatText(stats); strings.Contains(out, "delegated") {
		t.Errorf("report mentions delegation with none:\n%s", out)
	}
}

// A stats record counts wherever it sits.
//
// This used to be reached only from the assistant-message branch, so a record was
// aggregated only when it happened to be the very next line after an assistant
// message. A turn that ends in tool results ends with a *user* message — the loop
// appends the results that way — so its cost was silently dropped, and the session
// looked cheaper than it was. Found while adding the delegated split, not by the
// live run before it, which happened to end with an assistant answer.
func TestAnalyzeCountsStatsAfterAnyRecord(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
	}{{
		name: "after an assistant message, as before",
		lines: []string{
			`{"id":"2","type":"message","time":2,"message":{"role":"assistant","content":[]}}`,
			`{"id":"3","type":"stats","time":3,"stats":{"usage":{"input":50,"output":5}}}`,
		},
	}, {
		// The dropped case: a turn whose last message carries tool results.
		name: "after a user message carrying tool results",
		lines: []string{
			`{"id":"2","type":"message","time":2,"message":{"role":"assistant","content":[{"type":"tool_use"}]}}`,
			`{"id":"3","type":"message","time":3,"message":{"role":"user","content":[]}}`,
			`{"id":"4","type":"stats","time":4,"stats":{"usage":{"input":50,"output":5}}}`,
		},
	}, {
		name: "with no message before it at all",
		lines: []string{
			`{"id":"2","type":"stats","time":2,"stats":{"usage":{"input":50,"output":5}}}`,
		},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.jsonl")
			all := append([]string{
				`{"id":"1","type":"meta","time":1,"meta":{"cwd":"/tmp","model":"m"}}`,
			}, c.lines...)
			if err := os.WriteFile(path, []byte(strings.Join(all, "\n")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			stats, err := AnalyzeSession(path, Config{})
			if err != nil {
				t.Fatal(err)
			}
			if stats.TokenUsage.TotalInput != 50 || stats.TokenUsage.TotalOutput != 5 {
				t.Errorf("totals = %d/%d, want 50/5",
					stats.TokenUsage.TotalInput, stats.TokenUsage.TotalOutput)
			}
		})
	}
}

// And it is counted once. Aggregating in its own branch while the old one also fired
// would have doubled every record that did follow an assistant message.
func TestAnalyzeCountsEachStatsRecordOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := []string{
		`{"id":"1","type":"meta","time":1,"meta":{"cwd":"/tmp","model":"m"}}`,
		`{"id":"2","type":"message","time":2,"message":{"role":"assistant","content":[]}}`,
		`{"id":"3","type":"stats","time":3,"stats":{"usage":{"input":100,"output":10}}}`,
		`{"id":"4","type":"message","time":4,"message":{"role":"assistant","content":[]}}`,
		`{"id":"5","type":"stats","time":5,"stats":{"usage":{"input":200,"output":20}}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stats, err := AnalyzeSession(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.TokenUsage.TotalInput != 300 || stats.TokenUsage.TotalOutput != 30 {
		t.Errorf("totals = %d/%d, want 300/30 counted once each",
			stats.TokenUsage.TotalInput, stats.TokenUsage.TotalOutput)
	}
	if stats.TokenUsage.MaxInput != 200 {
		t.Errorf("max input = %d, want 200", stats.TokenUsage.MaxInput)
	}
}
