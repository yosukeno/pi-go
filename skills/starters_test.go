package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeStarters(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, StartersFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func onlyDiag(t *testing.T, diags []Diagnostic) string {
	t.Helper()
	if len(diags) != 1 {
		t.Fatalf("want 1 diagnostic, got %d: %+v", len(diags), diags)
	}
	return diags[0].Message
}

func TestStartersLoadsBothCardKinds(t *testing.T) {
	st, diags := parseStarters([]byte(`{
	  "heading": "今天要分析什么？",
	  "cards": [
	    {"icon": "search", "title": "找一个未检出的样本", "label": "零检出", "prompt": "找一个 detections 为 0 的样本"},
	    {"icon": "graph", "title": "浏览聚簇", "panel": "样本库", "at": "#/clusters"}
	  ]
	}`), "t.json")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	if st.Heading != "今天要分析什么？" || len(st.Cards) != 2 {
		t.Fatalf("bad parse: %+v", st)
	}
	if st.Send {
		t.Error("send must default to false: pi-go does not speak for the user")
	}
	if st.Cards[1].Panel != "样本库" || st.Cards[1].At != "#/clusters" {
		t.Errorf("panel card lost its target: %+v", st.Cards[1])
	}
}

func TestStartersRejectsSlashCommandButAllowsSkillInvocation(t *testing.T) {
	_, diags := parseStarters([]byte(`{"cards":[{"title":"go","prompt":"/auto"}]}`), "t.json")
	if len(diags) == 0 || !strings.Contains(diags[0].Message, "slash command") {
		t.Fatalf("a slash command must not be shippable as a prompt: %+v", diags)
	}

	st, diags := parseStarters([]byte(`{"cards":[{"title":"go","prompt":"/skill:malware-analysis 看下库"}]}`), "t.json")
	if len(diags) != 0 {
		t.Fatalf("/skill: is a prompt that expands, not a command: %+v", diags)
	}
	if len(st.Cards) != 1 {
		t.Fatal("skill invocation card was dropped")
	}
}

func TestStartersSkipsBadCardsAndKeepsTheRest(t *testing.T) {
	st, diags := parseStarters([]byte(`{"cards":[
	  {"title":"ok","prompt":"do it"},
	  {"prompt":"no title"},
	  {"title":"both","prompt":"x","panel":"y"},
	  {"title":"neither"},
	  {"title":"bad icon","icon":"nope","prompt":"x"},
	  {"title":"bad at","prompt":"x","at":"#/clusters"},
	  {"title":"bad hash","panel":"p","at":"clusters"}
	]}`), "t.json")
	if len(st.Cards) != 1 || st.Cards[0].Title != "ok" {
		t.Fatalf("want only the valid card, got %+v", st.Cards)
	}
	if len(diags) != 6 {
		t.Fatalf("every bad card should say why: %+v", diags)
	}
}

func TestStartersCapsCardsAndHeading(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"heading":"` + strings.Repeat("长", MaxStarterHeading+1) + `","cards":[`)
	for i := 0; i < MaxStarterCards+2; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"title":"c","prompt":"p"}`)
	}
	b.WriteString(`]}`)

	st, diags := parseStarters([]byte(b.String()), "t.json")
	if len(st.Cards) != MaxStarterCards {
		t.Errorf("want %d cards, got %d", MaxStarterCards, len(st.Cards))
	}
	if st.Heading != "" {
		t.Error("an over-long heading should be dropped, not truncated mid-word")
	}
	if len(diags) != 2 {
		t.Errorf("want a diagnostic per cap, got %+v", diags)
	}
}

func TestStartersRejectsUnknownFieldsAndBadJSON(t *testing.T) {
	// A typo'd key would otherwise render a card that does nothing at all.
	_, diags := parseStarters([]byte(`{"prompts":[{"title":"x"}]}`), "t.json")
	if len(diags) == 0 || !strings.Contains(diags[0].Message, "not valid") {
		t.Fatalf("unknown field should be reported: %+v", diags)
	}

	_, diags = parseStarters([]byte(`{nope}`), "t.json")
	if !strings.Contains(onlyDiag(t, diags), "not valid") {
		t.Error("malformed JSON should be reported once")
	}

	_, diags = parseStarters([]byte(`{"cards":[]}`), "t.json")
	if !strings.Contains(onlyDiag(t, diags), "no usable cards") {
		t.Error("an empty card list should say so")
	}
}

func TestLoadStartersReadsFromTheSkillDirectory(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStarters(t, skillDir, `{"cards":[{"title":"go","prompt":"do it"}],"send":true}`)

	list := []Skill{
		{Name: "demo", Dir: skillDir},
		{Name: "no-starters", Dir: dir}, // missing file is not a problem
	}
	got, diags := LoadStarters(list)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	if len(got) != 1 {
		t.Fatalf("want one contribution, got %d", len(got))
	}
	if got[0].Skill != "demo" || !got[0].Send {
		t.Errorf("contribution not attributed or send lost: %+v", got[0])
	}
}

func TestFollowupsParseAndClean(t *testing.T) {
	st, diags := parseStarters([]byte(`{
	  "cards": [{"title": "开始", "prompt": "go"}],
	  "followups": [
	    {"when": ["  mal-decompile  ", "", "反编译"], "chips": [
	      {"title": "生成 Yara", "prompt": "为它生成 Yara 规则"},
	      {"title": "看聚簇", "panel": "样本库", "at": "#/clusters"}
	    ]}
	  ]
	}`), "t.json")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	if len(st.Followups) != 1 {
		t.Fatalf("want one group, got %+v", st.Followups)
	}
	// Whitespace is trimmed and blanks dropped, or a stray space would make a
	// group silently unmatchable.
	if got := st.Followups[0].When; len(got) != 2 || got[0] != "mal-decompile" {
		t.Errorf("when not cleaned: %q", got)
	}
	if len(st.Followups[0].Chips) != 2 {
		t.Errorf("chips lost: %+v", st.Followups[0].Chips)
	}
}

func TestFollowupsRejectBadGroups(t *testing.T) {
	cases := map[string]string{
		`{"when": [], "chips": [{"title":"a","prompt":"p"}]}`:        "when",
		`{"when": ["  "], "chips": [{"title":"a","prompt":"p"}]}`:    "when",
		`{"when": ["x"], "chips": []}`:                               "at least one chip",
		`{"when": ["x"], "chips": [{"prompt":"p"}]}`:                 "title is required",
		`{"when": ["x"], "chips": [{"title":"a","prompt":"/auto"}]}`: "slash command",
	}
	for body, want := range cases {
		_, diags := parseStarters([]byte(`{"cards":[{"title":"c","prompt":"p"}],"followups":[`+body+`]}`), "t.json")
		if len(diags) != 1 || !strings.Contains(diags[0].Message, want) {
			t.Errorf("for %s want a diagnostic mentioning %q, got %+v", body, want, diags)
		}
	}

	// Too many chips is a group-level cap, not a truncation: silently dropping
	// the fourth suggestion would hide the author's mistake.
	var chips []string
	for i := 0; i < MaxFollowupChips+1; i++ {
		chips = append(chips, `{"title":"c","prompt":"p"}`)
	}
	_, diags := parseStarters([]byte(`{"cards":[{"title":"c","prompt":"p"}],"followups":[{"when":["x"],"chips":[`+
		strings.Join(chips, ",")+`]}]}`), "t.json")
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "more than") {
		t.Errorf("chip cap not reported: %+v", diags)
	}
}

func TestFollowupsAloneAreEnough(t *testing.T) {
	// A deployment may want next-step chips without replacing the empty state.
	st, diags := parseStarters([]byte(`{"followups":[{"when":["x"],"chips":[{"title":"a","prompt":"p"}]}]}`), "t.json")
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	if len(st.Cards) != 0 || len(st.Followups) != 1 {
		t.Errorf("want followups only, got %+v", st)
	}
}
