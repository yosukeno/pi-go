package analyze

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSession writes lines as a transcript and returns its path.
func writeSession(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func msg(id, parent, role string, time int, blocks string) string {
	return `{"id":"` + id + `","parentId":"` + parent + `","type":"message","time":` +
		itoa(time) + `,"message":{"role":"` + role + `","content":[` + blocks + `]}}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

const (
	blkText   = `{"type":"text"}`
	blkUse    = `{"type":"tool_use","name":"read"}`
	blkResult = `{"type":"tool_result"}`
)

// The unit that matters. -max-turns bounds a Run, and a session holds several, so
// the count that answers "how long is a run" is not the count SessionStats reports.
// This transcript is two runs of three and two turns; RoundCount is five.
func TestRunsAreSegmentedPerPromptNotPerSession(t *testing.T) {
	dir := t.TempDir()
	path := writeSession(t, dir, "s.jsonl",
		`{"id":"r0","type":"meta","time":1000,"meta":{"cwd":"/w","model":"m"}}`,
		// Run 1: two tool turns then an answer.
		msg("r1", "r0", "user", 1010, blkText),
		msg("r2", "r1", "assistant", 1020, blkUse),
		msg("r3", "r2", "user", 1030, blkResult),
		msg("r4", "r3", "assistant", 1040, blkUse),
		msg("r5", "r4", "user", 1050, blkResult),
		msg("r6", "r5", "assistant", 1060, blkText),
		// Run 2: one tool turn then an answer.
		msg("r7", "r6", "user", 2000, blkText),
		msg("r8", "r7", "assistant", 2010, blkUse),
		msg("r9", "r8", "user", 2020, blkResult),
		msg("r10", "r9", "assistant", 2030, blkText),
	)

	runs, err := Runs(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2: %+v", len(runs), runs)
	}
	if runs[0].Turns != 3 || runs[1].Turns != 2 {
		t.Errorf("turns = %d, %d; want 3, 2", runs[0].Turns, runs[1].Turns)
	}
	if !runs[0].Finished || !runs[1].Finished {
		t.Errorf("both runs reached an answer, got finished = %v, %v",
			runs[0].Finished, runs[1].Finished)
	}
	if runs[0].Index != 1 || runs[1].Index != 2 {
		t.Errorf("index = %d, %d; want 1, 2", runs[0].Index, runs[1].Index)
	}

	// The number this replaces, spelled out: five assistant messages in the file.
	stats, err := AnalyzeSession(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.RoundCount != 5 {
		t.Fatalf("RoundCount = %d, want 5", stats.RoundCount)
	}
	if stats.RoundCount == runs[0].Turns {
		t.Error("RoundCount happens to equal the first run's turns, so this fixture " +
			"no longer distinguishes the two units it exists to distinguish")
	}
}

// A run whose last assistant message asked for tools never reached an answer. Its
// turn count is a lower bound on the work, so it must not enter the percentiles.
func TestUnfinishedRunIsNotAnObservation(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "s.jsonl",
		msg("r1", "", "user", 1010, blkText),
		msg("r2", "r1", "assistant", 1020, blkUse),
		msg("r3", "r2", "user", 1030, blkResult),
		msg("r4", "r3", "assistant", 1040, blkUse),
		msg("r5", "r4", "user", 1050, blkResult),
	)

	report, err := AnalyzeRuns(dir, Config{})
	if err != nil {
		t.Fatal(err)
	}
	d := report.Distribution
	if d.Runs != 1 || d.Unfinished != 1 || d.Finished != 0 {
		t.Fatalf("runs=%d finished=%d unfinished=%d; want 1/0/1", d.Runs, d.Finished, d.Unfinished)
	}
	if d.Population != 0 {
		t.Errorf("population = %d, want 0: a cut-off run is censored data, not a sample", d.Population)
	}
	if d.P75 != 0 {
		t.Errorf("p75 = %d, want 0 with nothing to compute it from", d.P75)
	}
	if len(d.Censored) != 1 || d.Censored[0] != 2 {
		t.Errorf("censored = %v, want [2]", d.Censored)
	}
	if out := FormatRunsText(report); !strings.Contains(out, "No finished run") {
		t.Errorf("report should say the set has no finished run, got:\n%s", out)
	}
}

// A run that called no tool ends on turn 1 whatever the cap is. Counting it would
// answer a question about how much plain Q&A the history holds.
func TestToollessRunsAreExcludedFromThePercentiles(t *testing.T) {
	dir := t.TempDir()
	// Four toolless one-turn runs and one three-turn run that used tools. Left in,
	// the p75 would be 1; excluded, it is 3.
	lines := []string{
		msg("r1", "", "user", 1000, blkText),
		msg("r2", "r1", "assistant", 1010, blkText),
		msg("r3", "r2", "user", 1020, blkText),
		msg("r4", "r3", "assistant", 1030, blkText),
		msg("r5", "r4", "user", 1040, blkText),
		msg("r6", "r5", "assistant", 1050, blkText),
		msg("r7", "r6", "user", 1060, blkText),
		msg("r8", "r7", "assistant", 1070, blkText),
		msg("r9", "r8", "user", 1080, blkText),
		msg("r10", "r9", "assistant", 1090, blkUse),
		msg("r11", "r10", "user", 1100, blkResult),
		msg("r12", "r11", "assistant", 1110, blkUse),
		msg("r13", "r12", "user", 1120, blkResult),
		msg("r14", "r13", "assistant", 1130, blkText),
	}
	writeSession(t, dir, "s.jsonl", lines...)

	report, err := AnalyzeRuns(dir, Config{})
	if err != nil {
		t.Fatal(err)
	}
	d := report.Distribution
	if d.Runs != 5 || d.Finished != 5 {
		t.Fatalf("runs=%d finished=%d; want 5/5", d.Runs, d.Finished)
	}
	if d.Trivial != 4 {
		t.Fatalf("trivial = %d, want 4", d.Trivial)
	}
	if d.Population != 1 {
		t.Fatalf("population = %d, want 1", d.Population)
	}
	if d.P75 != 3 {
		t.Errorf("p75 = %d, want 3; a toolless run leaked into the population", d.P75)
	}
}

// A rewind abandons the records after the fork without deleting them. Those runs
// were cut short by a person, so counting them would put human edits into a
// measurement of how long work takes.
func TestAbandonedBranchesAreNotCounted(t *testing.T) {
	dir := t.TempDir()
	path := writeSession(t, dir, "s.jsonl",
		msg("r1", "", "user", 1000, blkText),
		msg("r2", "r1", "assistant", 1010, blkUse),
		msg("r3", "r2", "user", 1020, blkResult),
		// Abandoned: a long continuation nobody kept.
		msg("x1", "r3", "assistant", 1030, blkUse),
		msg("x2", "x1", "user", 1040, blkResult),
		msg("x3", "x2", "assistant", 1050, blkUse),
		msg("x4", "x3", "user", 1060, blkResult),
		msg("x5", "x4", "assistant", 1070, blkText),
		// The live branch resumes from r3.
		msg("r4", "r3", "assistant", 1080, blkText),
	)

	runs, err := Runs(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	if runs[0].Turns != 2 {
		t.Errorf("turns = %d, want 2: the abandoned branch was counted", runs[0].Turns)
	}
}

// Steering appends a plain user text message mid-run, the same shape a fresh prompt
// has. It is told apart by position: it follows the previous turn's tool results, so
// its predecessor is another user message, which an alternating history never has.
func TestSteeredMessageDoesNotSplitARun(t *testing.T) {
	dir := t.TempDir()
	path := writeSession(t, dir, "s.jsonl",
		msg("r1", "", "user", 1000, blkText),
		msg("r2", "r1", "assistant", 1010, blkUse),
		msg("r3", "r2", "user", 1020, blkResult),
		msg("r4", "r3", "user", 1025, blkText), // steered in
		msg("r5", "r4", "assistant", 1030, blkUse),
		msg("r6", "r5", "user", 1040, blkResult),
		msg("r7", "r6", "assistant", 1050, blkText),
	)

	runs, err := Runs(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1: the steered message was read as a new prompt: %+v", len(runs), runs)
	}
	if runs[0].Turns != 3 {
		t.Errorf("turns = %d, want 3", runs[0].Turns)
	}
	if !runs[0].Steered {
		t.Error("run should be marked steered, since that is what bounds how much " +
			"the segmentation can be trusted")
	}
}

// A run kept open by steering has an earlier tool-free assistant message that did
// not end it. The last one decides, not the first.
func TestLastAssistantMessageDecidesWhetherARunFinished(t *testing.T) {
	dir := t.TempDir()
	path := writeSession(t, dir, "s.jsonl",
		msg("r1", "", "user", 1000, blkText),
		msg("r2", "r1", "assistant", 1010, blkUse),
		msg("r3", "r2", "user", 1020, blkResult),
		msg("r4", "r3", "user", 1025, blkText), // steered
		msg("r5", "r4", "assistant", 1030, blkText),
		msg("r6", "r5", "assistant", 1040, blkUse),
		msg("r7", "r6", "user", 1050, blkResult),
	)

	runs, err := Runs(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	if runs[0].Finished {
		t.Error("run ended on a tool call, so it did not finish; an earlier " +
			"tool-free message was allowed to decide")
	}
}

// Nearest rank, rounding up, so "the p75" is a value at least 75% of the runs came
// in at or under — the claim the caller then relies on.
func TestPercentileRoundsUpSoTheClaimHolds(t *testing.T) {
	cases := []struct {
		sorted []int
		p      int
		want   int
	}{
		{[]int{1, 2, 3, 4}, 75, 3},
		{[]int{1, 2, 3, 4}, 50, 2},
		{[]int{5}, 75, 5},
		{[]int{1, 1, 1, 9}, 75, 1},
		{[]int{1, 1, 1, 9}, 90, 9},
		{[]int{1, 2, 3}, 75, 3}, // ceil(2.25) = 3
		{nil, 75, 0},
	}
	for _, c := range cases {
		if got := percentile(c.sorted, c.p); got != c.want {
			t.Errorf("percentile(%v, %d) = %d, want %d", c.sorted, c.p, got, c.want)
		}
		if c.want == 0 {
			continue
		}
		// The property, not just the value: at least p% of the data is at or below.
		under := 0
		for _, v := range c.sorted {
			if v <= c.want {
				under++
			}
		}
		if under*100 < c.p*len(c.sorted) {
			t.Errorf("percentile(%v, %d) = %d covers only %d/%d",
				c.sorted, c.p, c.want, under, len(c.sorted))
		}
	}
}

// A pile-up at one turn count is the cap that was in effect, but a handful of
// scattered interruptions is not, and naming their mode invents a limit. This
// distinction was added after the real numbers were read: four runs stopping at 4 is
// a cap, one stopping at 7 and one at 50 is not.
func TestACapIsOnlyNamedWhenTheCensoredRunsAgree(t *testing.T) {
	dir := t.TempDir()
	// Three sessions, each one unfinished run, stopping at 1, 2 and 3 turns.
	for i, turns := range []int{1, 2, 3} {
		var lines []string
		lines = append(lines, msg("r1", "", "user", 1000, blkText))
		prev := "r1"
		for turn := 0; turn < turns; turn++ {
			a := "a" + itoa(turn)
			u := "u" + itoa(turn)
			lines = append(lines, msg(a, prev, "assistant", 1010+turn*10, blkUse))
			lines = append(lines, msg(u, a, "user", 1015+turn*10, blkResult))
			prev = u
		}
		writeSession(t, dir, "s"+itoa(i)+".jsonl", lines...)
	}

	report, err := AnalyzeRuns(dir, Config{})
	if err != nil {
		t.Fatal(err)
	}
	out := FormatRunsText(report)
	if strings.Contains(out, "what a turn cap looks like") {
		t.Errorf("three scattered stops were reported as a cap:\n%s", out)
	}
	if !strings.Contains(out, "scattered") {
		t.Errorf("report should say the censored runs show no cap:\n%s", out)
	}
	if !strings.Contains(out, "at 1, 2, 3 turns") {
		t.Errorf("report should list the censored turn counts:\n%s", out)
	}
}

// Heavy censoring biases every percentile downward, hardest in the tail a cap is
// chosen from, so the number must not be presented bare.
func TestHeavyCensoringIsWarnedAbout(t *testing.T) {
	dir := t.TempDir()
	// One finished tool-using run and one cut off: 50% censored.
	writeSession(t, dir, "a.jsonl",
		msg("r1", "", "user", 1000, blkText),
		msg("r2", "r1", "assistant", 1010, blkUse),
		msg("r3", "r2", "user", 1020, blkResult),
		msg("r4", "r3", "assistant", 1030, blkText),
	)
	writeSession(t, dir, "b.jsonl",
		msg("r1", "", "user", 1000, blkText),
		msg("r2", "r1", "assistant", 1010, blkUse),
		msg("r3", "r2", "user", 1020, blkResult),
	)

	report, err := AnalyzeRuns(dir, Config{})
	if err != nil {
		t.Fatal(err)
	}
	out := FormatRunsText(report)
	if !strings.Contains(out, "biased low") {
		t.Errorf("a half-censored set should warn that the percentiles are biased low:\n%s", out)
	}
	if !strings.Contains(out, "floor") {
		t.Errorf("the warning should say p75 is a floor rather than the answer:\n%s", out)
	}
}

// A percentile over an unknown fraction of the data is not a measurement, so an
// unreadable file is reported rather than dropped.
func TestUnreadableSessionsAreReported(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "good.jsonl",
		msg("r1", "", "user", 1000, blkText),
		msg("r2", "r1", "assistant", 1010, blkUse),
		msg("r3", "r2", "user", 1020, blkResult),
		msg("r4", "r3", "assistant", 1030, blkText),
	)
	// Unreadable by permission, not by content: a file of garbage lines parses to an
	// empty session, which is a different thing from one that could not be opened.
	bad := filepath.Join(dir, "bad.jsonl")
	if err := os.WriteFile(bad, []byte("{}\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(bad, 0o644) })
	if _, err := os.ReadFile(bad); err == nil {
		t.Skip("running as a user that can read 0000 files")
	}

	report, err := AnalyzeRuns(dir, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Distribution.Unreadable != 1 {
		t.Errorf("unreadable = %d, want 1", report.Distribution.Unreadable)
	}
	if report.Distribution.Sessions != 1 {
		t.Errorf("sessions = %d, want 1; an unreadable file must not count as read",
			report.Distribution.Sessions)
	}
	if out := FormatRunsText(report); !strings.Contains(out, "Unreadable") {
		t.Errorf("report should name the unreadable file count:\n%s", out)
	}
}

// Damage that severs the parent chain makes the history before it unreachable, and
// an assistant message with no prompt in front of it has no run to belong to.
// Counting it would report a turn count for work whose beginning is missing.
func TestAssistantMessageWithNoPromptIsNotARun(t *testing.T) {
	dir := t.TempDir()
	path := writeSession(t, dir, "s.jsonl",
		// Parent "gone" is absent, so the walk stops here going backwards.
		msg("r2", "gone", "assistant", 1010, blkUse),
		msg("r3", "r2", "user", 1020, blkResult),
		msg("r4", "r3", "assistant", 1030, blkText),
	)

	runs, err := Runs(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Errorf("got %d runs, want 0: %+v", len(runs), runs)
	}
}

// The JSON form is what a script reads, so the fields a script would branch on have
// to survive marshalling.
func TestRunReportJSONCarriesThePopulationAndTheCensoring(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "a.jsonl",
		msg("r1", "", "user", 1000, blkText),
		msg("r2", "r1", "assistant", 1010, blkUse),
		msg("r3", "r2", "user", 1020, blkResult),
		msg("r4", "r3", "assistant", 1030, blkText),
	)

	report, err := AnalyzeRuns(dir, Config{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := FormatRunsJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"population": 1`, `"p75": 2`, `"runs": 1`, `"finished": 1`} {
		if !strings.Contains(out, want) {
			t.Errorf("JSON missing %s:\n%s", want, out)
		}
	}
}

// IncludeTurns is what makes an outlier findable: a p95 of 25 turns is only
// actionable if the run behind it can be opened.
func TestIncludeTurnsNamesTheSessionBehindEachRun(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "named.jsonl",
		msg("r1", "", "user", 1000, blkText),
		msg("r2", "r1", "assistant", 1010, blkUse),
		msg("r3", "r2", "user", 1020, blkResult),
		msg("r4", "r3", "assistant", 1030, blkText),
	)

	report, err := AnalyzeRuns(dir, Config{IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Runs) != 1 {
		t.Fatalf("got %d runs listed, want 1", len(report.Runs))
	}
	if report.Runs[0].Session != "named.jsonl" {
		t.Errorf("session = %q, want named.jsonl", report.Runs[0].Session)
	}

	plain, err := AnalyzeRuns(dir, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Runs) != 0 {
		t.Errorf("runs should be omitted without IncludeTurns, got %d", len(plain.Runs))
	}
}
