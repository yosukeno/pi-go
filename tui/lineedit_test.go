package tui

import (
	"testing"

	"github.com/yosukeno/pi-go/config"
)

func TestCompletionsCommandUnique(t *testing.T) {
	insert, list := completions("/usa")
	if insert != "ge " || len(list) != 1 || list[0].value != "/usage" {
		t.Fatalf(`/usa: got insert=%q list=%v`, insert, list)
	}
}

func TestCompletionsCommandAmbiguous(t *testing.T) {
	insert, list := completions("/m")
	if insert != "odel" {
		t.Fatalf(`/m should extend to the common prefix /model, got %q`, insert)
	}
	if len(list) != 2 {
		t.Fatalf(`/m should have 2 candidates (/model, /models), got %v`, list)
	}
	// The descriptions are the inline command help; they must not be empty.
	for _, c := range list {
		if c.desc == "" {
			t.Fatalf("candidate %s has no description", c.value)
		}
	}
}

func TestCompletionsListsWhenNoProgress(t *testing.T) {
	// "/model" already is the common prefix of /model and /models, so Tab can
	// only list.
	insert, list := completions("/model")
	if insert != "" || len(list) != 2 {
		t.Fatalf(`/model: got insert=%q list=%v`, insert, list)
	}
}

// Model completion reads the global catalog, which nothing populates in a test
// (no config file is loaded), so the fixture is installed rather than depending
// on whichever models a build happens to ship.
func TestCompletionsModelNames(t *testing.T) {
	saved := config.Catalog()
	config.SetCatalogForTest([]config.Model{
		{ID: "glm-5.2", Provider: "zhipu", Aliases: []string{"glm", "zhipu"}},
		{ID: "k3", Provider: "kimi", Aliases: []string{"kimi-k3", "kimi"}},
		{ID: "k3-256k", Provider: "kimi"},
		{ID: "kimi-for-coding", Provider: "kimi", Aliases: []string{"k2.7"}},
		{ID: "kimi-for-coding-highspeed", Provider: "kimi", Aliases: []string{"k2.7-fast"}},
	})
	t.Cleanup(func() { config.SetCatalogForTest(saved) })

	insert, list := completions("/model glm-")
	if insert != "5.2 " || len(list) != 1 || list[0].value != "glm-5.2" {
		t.Fatalf(`/model glm-: got insert=%q list=%v`, insert, list)
	}

	insert, list = completions("/model k3")
	if insert != "" || len(list) != 2 { // k3 and k3-256k
		t.Fatalf(`/model k3: got insert=%q list=%v`, insert, list)
	}

	// Aliases complete too, and say which model they belong to.
	_, list = completions("/model k2")
	if len(list) != 2 { // k2.7 and k2.7-fast
		t.Fatalf(`/model k2: got %v`, list)
	}
	for _, c := range list {
		if c.desc == "" {
			t.Fatalf("candidate %s has no description", c.value)
		}
	}
}

func TestCompletionsNoRegion(t *testing.T) {
	for _, before := range []string{"hello", "/xyz", "/model xyz", "/usage arg"} {
		if insert, list := completions(before); insert != "" || list != nil {
			t.Fatalf("%q: want no completion, got insert=%q list=%v", before, insert, list)
		}
	}
}

func TestRuneWidth(t *testing.T) {
	for r, want := range map[rune]int{
		'a': 1, '中': 2, 'Ａ': 2, '한': 2, 0x03: 0, 0x7f: 0,
	} {
		if got := runeWidth(r); got != want {
			t.Fatalf("runeWidth(%U) = %d, want %d", r, got, want)
		}
	}
}

func TestRuneBufferOps(t *testing.T) {
	buf := insertRunes([]rune("ac"), 1, []rune("b"))
	if string(buf) != "abc" {
		t.Fatalf("insertRunes: got %q", buf)
	}
	if got := string(deleteRune(buf, 1)); got != "ac" {
		t.Fatalf("deleteRune: got %q", got)
	}
}

// "/skill:" is not a prefix of any command name, so without its own region Tab
// would go dead exactly where the names are worth completing.
func TestCompletionsSkillNames(t *testing.T) {
	names := []string{"pdf-tools", "pdf-forms", "brave-search"}

	insert, list := completions("/skill:br", names...)
	if insert != "ave-search " || len(list) != 1 {
		t.Fatalf(`/skill:br: got insert=%q list=%v`, insert, list)
	}

	insert, list = completions("/skill:pdf-", names...)
	if insert != "" || len(list) != 2 {
		t.Fatalf(`/skill:pdf- should only list: got insert=%q list=%v`, insert, list)
	}

	// A bare "/skill:" lists everything rather than nothing.
	if _, list = completions("/skill:", names...); len(list) != 3 {
		t.Fatalf(`/skill:: got %v`, list)
	}

	// With no skills loaded there is nothing to offer, and the command branch
	// must not step in and complete "/skills" over the colon the user typed.
	if insert, list = completions("/skill:"); insert != "" || list != nil {
		t.Fatalf(`no skills: got insert=%q list=%v`, insert, list)
	}
}

// inputLayout drives both redraw paths, so its row math is pinned down here:
// hard lines, soft wraps, the cursor's row and column, and the +1 row a line
// owns when it ends exactly at the right edge (its cursor rests there).
func TestInputLayout(t *testing.T) {
	prompt := "❯ " // 2 cells
	cases := []struct {
		name                 string
		buf                  string
		cursor, cols         int
		rows, curRow, curCol int
		lineRows             []int
	}{
		{"empty", "", 0, 10, 1, 0, 2, []int{1}},
		{"plain", "abc", 3, 10, 1, 0, 5, []int{1}},
		{"cursor mid-line", "abc", 1, 10, 1, 0, 3, []int{1}},
		{"soft wrap", "abcdefgh", 8, 8, 2, 1, 2, []int{2}},                // 2+8 cells over 8 cols
		{"exact fit owns next row", "abcdefgh", 8, 10, 2, 1, 0, []int{2}}, // 2+8 == 10
		{"hard lines", "ab\ncd", 4, 10, 2, 1, 1, []int{1, 1}},
		{"trailing newline", "ab\n", 3, 10, 2, 1, 0, []int{1, 1}},
		{"cursor at newline belongs to its line", "ab\ncd", 2, 10, 2, 0, 4, []int{1, 1}},
		{"wide runes", "中a", 2, 10, 1, 0, 5, []int{1}}, // 2+2+1 cells
		{"unmeasurable grid", "a\nb", 3, 0, 2, 1, 1, []int{1, 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows, curRow, curCol, lineRows := inputLayout(prompt, []rune(c.buf), c.cursor, c.cols)
			if rows != c.rows || curRow != c.curRow || curCol != c.curCol {
				t.Errorf("%q cursor %d cols %d: got rows=%d cur=%d,%d, want %d / %d,%d",
					c.buf, c.cursor, c.cols, rows, curRow, curCol, c.rows, c.curRow, c.curCol)
			}
			if len(lineRows) != len(c.lineRows) {
				t.Fatalf("lineRows = %v, want %v", lineRows, c.lineRows)
			}
			for i := range c.lineRows {
				if lineRows[i] != c.lineRows[i] {
					t.Fatalf("lineRows = %v, want %v", lineRows, c.lineRows)
				}
			}
		})
	}
}

func TestNormalizePaste(t *testing.T) {
	if got := normalizePaste("a\r\nb\rc\nd"); got != "a\nb\nc\nd" {
		t.Fatalf("CRLF and CR fold to LF: got %q", got)
	}
	if got := normalizePaste("clean"); got != "clean" {
		t.Fatalf("clean text passes through: got %q", got)
	}
}

// Commands that Enter must not expand from a prefix still complete on Tab, and the
// asymmetry is the whole design: Tab puts the full name on screen before anything
// runs, while Enter on a prefix acts on a guess. Restricting both would make the
// safe path the inconvenient one and push people back to typing the guess.
func TestDestructiveCommandsStillTabComplete(t *testing.T) {
	found := 0
	for _, c := range Commands {
		if !c.NoAbbrev {
			continue
		}
		found++
		insert, list := completions(c.Name[:3])
		if insert == "" && len(list) == 0 {
			t.Errorf("%s: Tab offers nothing for %q", c.Name, c.Name[:3])
		}
	}
	if found == 0 {
		t.Skip("no command is marked NoAbbrev")
	}
}
