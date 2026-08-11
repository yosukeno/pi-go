package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangy/pi-go/llm"
)

// newStore creates a session with the given user messages already appended.
func newStore(t *testing.T, texts ...string) *Store {
	t.Helper()
	s, err := Create(t.TempDir(), "/work", "k3", "triage")
	if err != nil {
		t.Fatal(err)
	}
	for _, txt := range texts {
		if err := s.Append(llm.UserText(txt)); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func texts(ms []llm.Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Text()
	}
	return out
}

// chopTail truncates the file mid-record, which is what a process killed during
// append leaves behind.
func chopTail(t *testing.T, path string, n int) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n >= len(raw) {
		t.Fatalf("cannot chop %d bytes off a %d byte file", n, len(raw))
	}
	if err := os.WriteFile(path, raw[:len(raw)-n], 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRoundTripPreservesOrder(t *testing.T) {
	s := newStore(t, "one", "two", "three")

	got := texts(s.Messages(""))
	want := []string{"one", "two", "three"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("in memory: got %v, want %v", got, want)
	}

	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(reopened.Messages("")); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("from disk: got %v, want %v", got, want)
	}
	if d := reopened.Diagnostics(); len(d) != 0 {
		t.Errorf("healthy file reported %v", d)
	}
}

// A process killed mid-append leaves a partial final line. Recovering the rest is
// the whole point of tolerating it.
func TestTruncatedFinalLineRecoversTheRest(t *testing.T) {
	s := newStore(t, "one", "two", "three")
	chopTail(t, s.Path(), 25)

	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	got := texts(reopened.Messages(""))
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("got %v, want [one two]", got)
	}
	// Nothing was lost that had finished being written, so nothing to report.
	if d := reopened.Diagnostics(); len(d) != 0 {
		t.Errorf("truncated tail reported %v; it is the expected outcome of a kill", d)
	}
}

// The regression this whole change exists for. Before the fix this sequence
// reported one message and silently discarded four.
func TestResumeAfterMidWriteKillKeepsEveryMessage(t *testing.T) {
	s := newStore(t, "one", "two", "three")
	path := s.Path()

	// The kill.
	chopTail(t, path, 25)

	// -resume: load what survived, then keep working in the same file. The
	// partial line is now in the middle.
	resumed, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, txt := range []string{"four", "five"} {
		if err := resumed.Append(llm.UserText(txt)); err != nil {
			t.Fatal(err)
		}
	}

	// A second -resume, which is where the loss used to surface.
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := texts(again.Messages(""))
	want := []string{"one", "two", "four", "five"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}

	// "three" never finished being written, so it is gone for good — but the
	// damage must be reported rather than passed off as a healthy file.
	if len(again.Diagnostics()) == 0 {
		t.Error("a damaged file loaded without a word about it")
	}
}

// The write half of the fix, isolated: appending must not fuse a new record onto
// an unterminated line.
func TestAppendClosesOffAnUnterminatedLine(t *testing.T) {
	s := newStore(t, "one")
	chopTail(t, s.Path(), 20)

	resumed, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := resumed.Append(llm.UserText("next")); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	// Every line must still be one record. The fused line is what destroyed the
	// newly written record before.
	var parsed int
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		var r Record
		if json.Unmarshal([]byte(line), &r) == nil {
			parsed++
		}
	}
	if parsed != 2 {
		t.Fatalf("parsed %d of 3 lines; the appended record was fused onto the damaged one", parsed)
	}
	if got := texts(resumed.Messages("")); len(got) != 1 || got[0] != "next" {
		t.Fatalf("got %v, want [next]", got)
	}
}

// Damage in the middle of a healthy file, with good records on both sides.
func TestCorruptMiddleLineKeepsBothSides(t *testing.T) {
	s := newStore(t, "one", "two", "three", "four")

	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected meta + 4 messages, got %d lines", len(lines))
	}
	lines[2] = `{"id":"broken","type":"mess` // "two"
	if err := os.WriteFile(s.Path(), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	got := texts(reopened.Messages(""))
	want := []string{"one", "three", "four"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v: history before the damage was dropped", got, want)
	}
	if !strings.Contains(strings.Join(reopened.Diagnostics(), " "), "damaged") {
		t.Errorf("diagnostics = %v, want the damage named", reopened.Diagnostics())
	}
}

// A branched file must not be stitched by file order: file order is not chain
// order there, so guessing could splice two conversations together.
func TestBranchedFileIsReportedRatherThanGuessed(t *testing.T) {
	s := newStore(t, "one", "two")
	root := s.records[1].ID // the "one" record

	if err := s.Fork(root); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(llm.UserText("two-alt")); err != nil {
		t.Fatal(err)
	}

	// Damage the shared ancestor.
	raw, _ := os.ReadFile(s.Path())
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	lines[1] = `{"id":"broken","type":"mes` // "one", parent of both branches
	if err := os.WriteFile(s.Path(), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(reopened.Messages("")); len(got) != 1 || got[0] != "two-alt" {
		t.Fatalf("got %v, want just the leaf: a branched file must not be stitched", got)
	}
	joined := strings.Join(reopened.Diagnostics(), " ")
	if !strings.Contains(joined, "branches") {
		t.Errorf("diagnostics = %v, want the refusal to guess explained", reopened.Diagnostics())
	}
}

func TestMetaIsReadableAfterReopen(t *testing.T) {
	s := newStore(t, "one")
	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	m := reopened.Meta()
	if m == nil {
		t.Fatal("meta is nil")
	}
	if m.Cwd != "/work" || m.Model != "k3" {
		t.Errorf("got cwd=%q model=%q", m.Cwd, m.Model)
	}
	if len(m.Skills) != 1 || m.Skills[0] != "triage" {
		t.Errorf("skills = %v, want [triage]", m.Skills)
	}
}

func TestForkBranchesWithoutRewriting(t *testing.T) {
	s := newStore(t, "one", "two")
	forkAt := s.records[1].ID

	if err := s.Fork(forkAt); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(llm.UserText("two-alt")); err != nil {
		t.Fatal(err)
	}

	if got := texts(s.Messages("")); strings.Join(got, ",") != "one,two-alt" {
		t.Errorf("new branch: got %v", got)
	}
	// The original branch is still there, which is the point of a tree.
	if got := texts(s.Messages(s.records[2].ID)); strings.Join(got, ",") != "one,two" {
		t.Errorf("original branch: got %v", got)
	}
	if err := s.Fork("nope"); err == nil {
		t.Error("forking to an unknown record should fail")
	}
}

func TestForkToRootAbandonsEverything(t *testing.T) {
	s := newStore(t, "one", "two")

	if err := s.Fork(""); err != nil {
		t.Fatal(err)
	}
	if got := s.Messages(""); len(got) != 0 {
		t.Errorf("after a root fork: got %v, want empty", texts(got))
	}
	if err := s.Append(llm.UserText("fresh")); err != nil {
		t.Fatal(err)
	}
	if got := texts(s.Messages("")); strings.Join(got, ",") != "fresh" {
		t.Errorf("new chain: got %v", got)
	}
}

func TestRewindPointCountsOnlyVisibleUserMessages(t *testing.T) {
	s, err := Create(t.TempDir(), "/work", "k3")
	if err != nil {
		t.Fatal(err)
	}
	append := func(m llm.Message) {
		t.Helper()
		if err := s.Append(m); err != nil {
			t.Fatal(err)
		}
	}
	append(llm.UserText("first question")) // u1
	append(llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "answer one"}}})
	append(llm.Message{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "c1", Text: "out"}}}) // invisible on a timeline
	append(llm.UserText("second question"))                                                                                  // u2
	append(llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "answer two"}}})

	// records: 0=meta, 1=u1, 2=a1, 3=tool-result, 4=u2, 5=a2
	point, ok := s.RewindPoint(1)
	if !ok || point != s.records[0].ID {
		t.Errorf("k=1: got (%q, %v), want the creation meta as fork point", point, ok)
	}
	point, ok = s.RewindPoint(2)
	if !ok || point != s.records[3].ID {
		// The tool-result record is what u2 hangs off, and it must go with its
		// turn: forking to it keeps answer one plus the result that closed it.
		t.Errorf("k=2: got (%q, %v), want the tool-result record", point, ok)
	}
	if _, ok = s.RewindPoint(3); ok {
		t.Error("k=3: only two visible user messages exist")
	}
	if _, ok = s.RewindPoint(0); ok {
		t.Error("k=0 is not an ordinal")
	}
}

func TestSummariseCountsOnlyTheLiveBranch(t *testing.T) {
	dir := t.TempDir()
	s, err := Create(dir, "/work", "k3")
	if err != nil {
		t.Fatal(err)
	}
	append := func(m llm.Message) {
		t.Helper()
		if err := s.Append(m); err != nil {
			t.Fatal(err)
		}
	}
	append(llm.UserText("keep me"))
	append(llm.UserText("abandoned question"))
	append(llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "abandoned answer"}}})

	point, ok := s.RewindPoint(2)
	if !ok {
		t.Fatal("no rewind point for the second user message")
	}
	if err := s.Fork(point); err != nil {
		t.Fatal(err)
	}
	append(llm.UserText("replacement question"))

	list, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d sessions, want 1", len(list))
	}
	// The live branch is keep-me + replacement = 2 messages; the abandoned
	// pair still sits in the file and must not be counted.
	if got := list[0].Messages; got != 2 {
		t.Errorf("messages = %d, want 2 (the abandoned branch must not count)", got)
	}
	if got := list[0].Title; got != "keep me" {
		t.Errorf("title = %q, want the first message of the live branch", got)
	}
}

func TestMessagesSurvivesACycle(t *testing.T) {
	s := newStore(t, "one")
	// Only a hand-edited file can do this; reading one must not hang.
	s.records = append(s.records, Record{ID: "a", ParentID: "b", Type: "message", Message: msg("a")})
	s.records = append(s.records, Record{ID: "b", ParentID: "a", Type: "message", Message: msg("b")})
	s.head = "a"

	done := make(chan []string, 1)
	go func() { done <- texts(s.Messages("")) }()
	select {
	case got := <-done:
		if len(got) == 0 {
			t.Error("expected the reachable part of the cycle")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Messages did not terminate on a cyclic file")
	}
}

func TestListSummarisesWithoutLoadingHistory(t *testing.T) {
	dir := t.TempDir()
	s, err := Create(dir, "/work", "k3", "triage")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(llm.UserText("  first   question\nsecond line ")); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(llm.UserText("later")); err != nil {
		t.Fatal(err)
	}
	// A file that is not a session must not hide the ones that are.
	if err := os.WriteFile(dir+"/notes.txt", []byte("ignore me"), 0o600); err != nil {
		t.Fatal(err)
	}

	list, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d sessions, want 1", len(list))
	}
	got := list[0]
	if got.Messages != 2 {
		t.Errorf("messages = %d, want 2", got.Messages)
	}
	if got.Title != "first question second line" {
		t.Errorf("title = %q, want the first prompt on one line", got.Title)
	}
	if got.Cwd != "/work" || got.Model != "k3" {
		t.Errorf("got cwd=%q model=%q", got.Cwd, got.Model)
	}
	if len(got.Skills) != 1 || got.Skills[0] != "triage" {
		t.Errorf("skills = %v, want [triage]", got.Skills)
	}
}

func TestListOnMissingDirIsEmptyNotAnError(t *testing.T) {
	list, err := List(t.TempDir() + "/nope")
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("got %d sessions", len(list))
	}
}

func TestListMergesAppendedMetaEdits(t *testing.T) {
	dir := t.TempDir()
	s, err := Create(dir, "/work", "k3")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(llm.UserText("the derived title")); err != nil {
		t.Fatal(err)
	}
	// Sidebar edits land as a meta record appended long after creation.
	pinned, name := true, "renamed session"
	if err := s.AppendMeta(&Meta{Pinned: &pinned, Title: &name}); err != nil {
		t.Fatal(err)
	}

	list, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d sessions, want 1", len(list))
	}
	got := list[0]
	if !got.Pinned {
		t.Error("pinned edit did not merge")
	}
	if got.Title != "renamed session" {
		t.Errorf("title = %q, want the custom name to beat the derived one", got.Title)
	}
	// The creation meta still owns these; appended edits must not blank them.
	if got.Cwd != "/work" || got.Model != "k3" || got.Created == 0 {
		t.Errorf("creation meta clobbered: cwd=%q model=%q created=%d", got.Cwd, got.Model, got.Created)
	}

	// Unpinning writes an explicit false; the merge must honour it, not just
	// the last true. And an edit that says nothing about the title must not
	// drop it.
	pinned = false
	if err := s.AppendMeta(&Meta{Pinned: &pinned}); err != nil {
		t.Fatal(err)
	}
	list, err = List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Pinned {
		t.Error("an explicit unpin was lost")
	}
	if list[0].Title != "renamed session" {
		t.Errorf("unrelated edit dropped the title: %q", list[0].Title)
	}
}

func TestListSortsPinnedFirst(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"one", "two"} {
		s, err := Create(dir, "/work", "k3")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Append(llm.UserText(name)); err != nil {
			t.Fatal(err)
		}
		if name == "one" {
			pinned := true
			if err := s.AppendMeta(&Meta{Pinned: &pinned}); err != nil {
				t.Fatal(err)
			}
		}
	}
	list, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d sessions, want 2", len(list))
	}
	// Whichever file is newer, the pinned one leads.
	if list[0].Title != "one" {
		t.Errorf("pinned session should sort first, got %q", list[0].Title)
	}
}

func TestLatestPicksTheNewestByName(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"20240101T000000Z-aaaa.jsonl", "20250101T000000Z-bbbb.jsonl", "skip.md"} {
		if err := os.WriteFile(dir+"/"+n, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Latest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "20250101T000000Z-bbbb.jsonl") {
		t.Errorf("got %q", got)
	}
	if _, err := Latest(t.TempDir()); err == nil {
		t.Error("an empty dir should report that there is nothing to resume")
	}
}

func TestTitleOfALongPromptIsTrimmed(t *testing.T) {
	long := strings.Repeat("字", 200)
	got := title(long)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("long title not marked as trimmed: %q", got)
	}
	// Runes, not bytes: multi-byte text must not be cut mid-character.
	if n := len([]rune(got)); n != titleMaxRunes+1 {
		t.Errorf("title is %d runes, want %d plus the ellipsis", n, titleMaxRunes)
	}
}

func msg(text string) *llm.Message {
	m := llm.UserText(text)
	return &m
}

// The writer that never existed. The Stats type, the parser in package analyze, and
// the "Token Usage" section of -analyze-session were all complete, and nothing
// produced a record — so every session reported costing zero tokens, which reads as
// a fact rather than as the absence of one.
func TestUsageDeltaRecordsWhatWasAddedNotTheRunningTotal(t *testing.T) {
	var recorded Recorded
	none := llm.Usage{}

	// First turn.
	st, ok := UsageDelta(&recorded, llm.Usage{Input: 100, Output: 20}, none)
	if !ok || st.Usage.Input != 100 || st.Usage.Output != 20 {
		t.Fatalf("first delta = %+v, ok=%v", st.Usage, ok)
	}
	// Second turn: the agent's counters are cumulative, so the record must be the
	// difference. Writing the total each time would make a five-turn session look
	// like fifteen turns' worth of tokens once the analyzer sums them.
	st, ok = UsageDelta(&recorded, llm.Usage{Input: 350, Output: 55}, none)
	if !ok || st.Usage.Input != 250 || st.Usage.Output != 35 {
		t.Fatalf("second delta = %+v, ok=%v", st.Usage, ok)
	}
	// A flush that added nothing writes nothing, or the transcript fills with rows
	// of zeroes.
	if _, ok = UsageDelta(&recorded, llm.Usage{Input: 350, Output: 55}, none); ok {
		t.Error("an unchanged total produced a record")
	}
	// Cache and reasoning ride along, because a session where only those moved still
	// cost something.
	st, ok = UsageDelta(&recorded, llm.Usage{Input: 350, Output: 55, CacheRead: 9, Reasoning: 4}, none)
	if !ok || st.Usage.CacheRead != 9 || st.Usage.Reasoning != 4 {
		t.Errorf("cache/reasoning delta = %+v, ok=%v", st.Usage, ok)
	}
}

// The delegated half. Usage answers "what has this cost, all in" — which is what a
// budget must bound — and Delegated says how much of that someone else spent. The
// two are fixed by different decisions, so a single total cannot stand in for both.
func TestUsageDeltaSeparatesDelegatedSpending(t *testing.T) {
	var recorded Recorded

	// A turn where the agent spent 100 itself and a subagent spent 400 of the 500.
	st, ok := UsageDelta(&recorded,
		llm.Usage{Input: 500, Output: 60}, llm.Usage{Input: 400, Output: 40})
	if !ok {
		t.Fatal("no record for a turn that spent tokens")
	}
	if st.Usage.Input != 500 || st.Delegated == nil || st.Delegated.Input != 400 {
		t.Fatalf("usage=%+v delegated=%+v; want the total and the delegated part of it",
			st.Usage, st.Delegated)
	}
	// A subset, never a sibling: subtracting is what gives the agent's own spending,
	// and adding them would count the same tokens twice.
	if own := st.Usage.Input - st.Delegated.Input; own != 100 {
		t.Errorf("own input = %d, want 100", own)
	}

	// A later turn with no delegation carries no delegated section at all. A field
	// that is always present says nothing by being present, and most turns delegate
	// nothing.
	st, ok = UsageDelta(&recorded,
		llm.Usage{Input: 700, Output: 90}, llm.Usage{Input: 400, Output: 40})
	if !ok || st.Usage.Input != 200 {
		t.Fatalf("second delta = %+v, ok=%v", st.Usage, ok)
	}
	if st.Delegated != nil {
		t.Errorf("delegated = %+v on a turn that delegated nothing", st.Delegated)
	}

	// Both advance together. A caller that moved one and forgot the other would
	// attribute the next turn's own spending to delegation, or the reverse — which is
	// why they travel in one struct.
	if recorded.Usage.Input != 700 || recorded.Delegated.Input != 400 {
		t.Errorf("recorded = %+v, want both advanced", recorded)
	}
}

// Written, then read back the way -analyze-session reads it: the record has to
// survive the round trip through the file, not merely through the struct.
func TestAppendStatsRoundTripsThroughTheFile(t *testing.T) {
	s := newStore(t, "hello")
	if err := s.AppendStats(Stats{
		Usage: &UsageStats{Input: 1200, Output: 300},
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var rec struct {
			Type  string `json:"type"`
			Stats *struct {
				Usage *struct {
					Input  int64 `json:"input"`
					Output int64 `json:"output"`
				} `json:"usage"`
			} `json:"stats"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil || rec.Type != "stats" {
			continue
		}
		found = true
		if rec.Stats == nil || rec.Stats.Usage == nil {
			t.Fatalf("stats record has no usage: %s", line)
		}
		if rec.Stats.Usage.Input != 1200 || rec.Stats.Usage.Output != 300 {
			t.Errorf("usage = %+v, want 1200/300", rec.Stats.Usage)
		}
	}
	if !found {
		t.Fatalf("no stats record in the transcript:\n%s", raw)
	}
	// Reopening must not choke on it, and must not mistake it for a message: a
	// transcript with stats in it is still a resumable conversation.
	again, err := Open(s.Path())
	if err != nil {
		t.Fatalf("Open after AppendStats: %v", err)
	}
	if got := texts(again.Messages("")); len(got) != 1 || got[0] != "hello" {
		t.Errorf("messages after reopening = %v, want [hello]", got)
	}
}

// UsageTotals is what a reopened session seeds its counters from: the sum of
// the branch's stats deltas, cache and delegation included. Summing across
// transcripts would double-count (see AppendStats); summing within one is the
// session's all-in cost.
func TestUsageTotalsSurviveReopening(t *testing.T) {
	s := newStore(t, "hello")
	if err := s.AppendStats(Stats{
		Usage:     &UsageStats{Input: 1200, Output: 300, CacheRead: 600, Reasoning: 12},
		Delegated: &UsageStats{Input: 400, Output: 40},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendStats(Stats{Usage: &UsageStats{Input: 250, Output: 35}}); err != nil {
		t.Fatal(err)
	}

	again, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	usage, delegated := again.UsageTotals()
	if usage.Input != 1450 || usage.Output != 335 || usage.CacheRead != 600 || usage.Reasoning != 12 {
		t.Errorf("usage = %+v, want the two deltas summed (1450/335/600/12)", usage)
	}
	if delegated.Input != 400 || delegated.Output != 40 {
		t.Errorf("delegated = %+v, want 400/40", delegated)
	}
}

// The web UI writes the file when a session is created, so every abandoned
// "new session" click leaves a message-less file behind. CleanEmpty is the
// startup sweep for those — and its "when in doubt, keep it" rules, because
// the file is the only copy.
func TestCleanEmpty(t *testing.T) {
	dir := t.TempDir()
	mk := func() *Store {
		t.Helper()
		s, err := Create(dir, "/work", "k3")
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	empty := mk() // created, never used: the case the sweep exists for

	full := mk()
	if err := full.Append(llm.UserText("hi")); err != nil {
		t.Fatal(err)
	}

	pinned := mk() // empty, but someone marked it on purpose
	yes := true
	if err := pinned.AppendMeta(&Meta{Pinned: &yes}); err != nil {
		t.Fatal(err)
	}

	damaged := mk() // an unparseable line cannot prove emptiness
	f, err := os.OpenFile(damaged.Path(), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("not json\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	fresh := mk() // younger than the grace window: its first message may be coming

	// Age everything but fresh past the grace window.
	past := time.Now().Add(-2 * time.Hour)
	for _, s := range []*Store{empty, full, pinned, damaged} {
		if err := os.Chtimes(s.Path(), past, past); err != nil {
			t.Fatal(err)
		}
	}

	n, err := CleanEmpty(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("removed %d, want 1", n)
	}
	if _, err := os.Stat(empty.Path()); !os.IsNotExist(err) {
		t.Errorf("empty session survived: %v", err)
	}
	for _, s := range []*Store{full, pinned, damaged, fresh} {
		if _, err := os.Stat(s.Path()); err != nil {
			t.Errorf("%s should have been kept: %v", filepath.Base(s.Path()), err)
		}
	}
}

func TestCleanEmptyMissingDir(t *testing.T) {
	n, err := CleanEmpty(filepath.Join(t.TempDir(), "nope"))
	if n != 0 || err != nil {
		t.Fatalf("got %d, %v; want 0, nil", n, err)
	}
}

// Abandoning a branch does not un-spend its tokens. UsageTotals walked the live
// chain once, which meant a compaction — Fork("") drops everything reachable — made
// a session report having cost nothing, and -token-budget forgot the whole session
// every time the conversation was reorganised.
func TestUsageTotalsKeepTheCostOfAbandonedBranches(t *testing.T) {
	s := newStore(t, "hello")
	if err := s.AppendStats(Stats{Usage: &UsageStats{Input: 5000, Output: 400}}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.UsageTotals()
	if before.Input != 5000 {
		t.Fatalf("precondition: usage = %+v", before)
	}

	// What /compact does: abandon everything reachable and start a fresh chain.
	if err := s.Fork(""); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(llm.UserText("a summary of it")); err != nil {
		t.Fatal(err)
	}

	after, _ := s.UsageTotals()
	if after.Input != 5000 || after.Output != 400 {
		t.Errorf("after Fork(\"\"): usage = %+v, want the 5000/400 that was really spent", after)
	}
	// The live branch is genuinely short — the cost surviving is not an accident of
	// the fork having failed to take effect.
	if n := len(s.Messages("")); n != 1 {
		t.Errorf("live branch has %d messages, want just the summary", n)
	}

	// And it survives the reload, which is where a resumed session seeds its counters.
	again, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	reopened, _ := again.UsageTotals()
	if reopened.Input != 5000 || reopened.Output != 400 {
		t.Errorf("reopened: usage = %+v, want 5000/400", reopened)
	}
	if n := len(again.Messages("")); n != 1 {
		t.Errorf("reopened live branch has %d messages, want 1", n)
	}
}

// A partial fork — what rewind does — has the same property for the same reason.
// The turns after the fork point are unreachable, and they were still paid for.
func TestUsageTotalsKeepTheCostOfARewoundTurn(t *testing.T) {
	s := newStore(t, "first")
	if err := s.AppendStats(Stats{Usage: &UsageStats{Input: 100, Output: 10}}); err != nil {
		t.Fatal(err)
	}
	point := s.Head()
	if err := s.Append(llm.UserText("second")); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendStats(Stats{Usage: &UsageStats{Input: 900, Output: 90}}); err != nil {
		t.Fatal(err)
	}

	if err := s.Fork(point); err != nil {
		t.Fatal(err)
	}
	usage, _ := s.UsageTotals()
	if usage.Input != 1000 || usage.Output != 100 {
		t.Errorf("usage = %+v, want 1000/100: the rewound turn was billed too", usage)
	}
}
