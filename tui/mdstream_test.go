package tui

import (
	"strings"
	"testing"
)

// mdRun feeds the deltas through a buffer-backed filter and returns what the
// terminal would have received. Note the colour vars are blank under `go
// test` (stdout is a pipe), so Dim/Reset vanish from expectations while the
// filter's literal style escapes (\x1b[1m etc.) remain.
func mdRun(width int, deltas ...string) string {
	var b strings.Builder
	m := newMDStream(&b, func() int { return width })
	for _, d := range deltas {
		m.write(d)
	}
	m.flush()
	return b.String()
}

func TestMDProsePassthrough(t *testing.T) {
	got := mdRun(80, "Hel", "lo\nwor", "ld\n")
	if got != "Hello\nworld\n" {
		t.Errorf("prose streams through untouched: %q", got)
	}
}

func TestMDBold(t *testing.T) {
	got := mdRun(80, "a **bo", "ld** b\n")
	if got != "a \x1b[1mbold\x1b[22m b\n" {
		t.Errorf("Bold toggles across deltas: %q", got)
	}
}

func TestMDItalicAndSpacing(t *testing.T) {
	got := mdRun(80, "2 * 3 and *it*\n")
	if got != "2 * 3 and \x1b[3mit\x1b[23m\n" {
		t.Errorf("spacing rule keeps math literal, styles italic: %q", got)
	}
}

func TestMDInlineCode(t *testing.T) {
	got := mdRun(80, "run `go build` now\n")
	if got != "run \x1b[7mgo build\x1b[27m now\n" {
		t.Errorf("inline code is reverse video: %q", got)
	}
}

func TestMDUnclosedStyleClosesAtNewline(t *testing.T) {
	got := mdRun(80, "**oops\nnext\n")
	if got != "\x1b[1moops\x1b[22m\nnext\n" {
		t.Errorf("an unclosed marker degrades at the newline: %q", got)
	}
	if got := mdRun(80, "2 ** 3\n"); got != "2 ** 3\n" {
		t.Errorf("space after ** is literal: %q", got)
	}
}

func TestMDHeader(t *testing.T) {
	got := mdRun(80, "## 标题\nbody\n")
	if got != "\x1b[1m标题\x1b[22m\nbody\n" {
		t.Errorf("header renders Bold without the ##: %q", got)
	}
	if got := mdRun(80, "#tag\n"); got != "#tag\n" {
		t.Errorf("#tag is prose: %q", got)
	}
}

func TestMDRule(t *testing.T) {
	got := mdRun(80, "a\n", "---\n", "b\n")
	want := "a\n" + Dim + strings.Repeat("─", 80) + Reset + "\nb\n"
	if got != want {
		t.Errorf("rule spans the width: %q", got)
	}
	if got := mdRun(80, "- item\n"); got != "- item\n" {
		t.Errorf("list item is prose: %q", got)
	}
}

func TestMDTable(t *testing.T) {
	got := mdRun(80,
		"before\n| 目录 | 职责 |\n|---",
		"|---|\n| main.go | 入口 |\n| agent/ | 核心循环 |\n",
		"after\n",
	)
	want := "before\n" +
		"┌─────────┬──────────┐\n" +
		"│ 目录    │ 职责     │\n" +
		"├─────────┼──────────┤\n" +
		"│ main.go │ 入口     │\n" +
		"│ agent/  │ 核心循环 │\n" +
		"└─────────┴──────────┘\n" +
		"after\n"
	if got != want {
		t.Errorf("table frame:\n%s\nwant:\n%s", got, want)
	}
}

func TestMDTableFlushWithoutNewline(t *testing.T) {
	// The run ended right after the last row: flush still renders the table.
	got := mdRun(80, "| a | b |\n|---|---|\n| 1 | 2 |")
	if !strings.Contains(got, "┌") || !strings.HasSuffix(got, "┘\n") {
		t.Errorf("flush renders the buffered table: %q", got)
	}
	if strings.Contains(got, "---") {
		t.Errorf("the separator row is absorbed: %q", got)
	}
}

func TestMDNotATable(t *testing.T) {
	got := mdRun(80, "| just a pipe\n")
	if got != "| just a pipe\n" {
		t.Errorf("a lone pipe is prose: %q", got)
	}
}

func TestMDTableShrinksToWidth(t *testing.T) {
	got := mdRun(30, "| 一段很长很长很长的中文内容 | b |\n|---|---|\n| x | y |\n")
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if w := cellsWidth([]rune(line)); w > 30 {
			t.Errorf("line exceeds width: %d > 30: %q", w, line)
		}
	}
	if !strings.Contains(got, "…") {
		t.Errorf("shrunk cells carry an ellipsis: %q", got)
	}
}

func TestMDFence(t *testing.T) {
	got := mdRun(80, "code:\n```go\nfmt.Println(`x`)\n```\ndone\n")
	want := "code:\n" + Dim + "│ go" + Reset + "\n" +
		Dim + "│ " + Reset + "fmt.Println(`x`)\n" +
		"done\n"
	if got != want {
		t.Errorf("fence renders as barred lines, markers suppressed: %q", got)
	}
}
