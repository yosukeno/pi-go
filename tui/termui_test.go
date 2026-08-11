package tui

import (
	"bytes"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// lineCells measures what the terminal would: escape-free test output (go
// test blanks the colour vars via init) counted with the wide-char table.
func lineCells(s string) int { return cellsWidth([]rune(s)) }

func TestStatusLineLayout(t *testing.T) {
	got := StatusLine("glm-5.2", "~/proj", "", 48000, 200000, 60)
	if lineCells(got) != 60 {
		t.Errorf("width = %d, want exactly 60: %q", lineCells(got), got)
	}
	if !strings.HasPrefix(got, "glm-5.2 · ~/proj") {
		t.Errorf("left side wrong: %q", got)
	}
	if !strings.HasSuffix(got, "ctx 24% (48K/200K)") {
		t.Errorf("gauge wrong: %q", got)
	}
}

func TestStatusLineGaugeHides(t *testing.T) {
	// No window known, or no turn reported yet: no gauge, no stray separator.
	if got := StatusLine("glm-5.2", "~/proj", "", 48000, 0, 60); strings.Contains(got, "ctx") {
		t.Errorf("window 0 must hide the gauge: %q", got)
	}
	if got := StatusLine("glm-5.2", "~/proj", "", 0, 200000, 60); strings.Contains(got, "ctx") {
		t.Errorf("no usage yet must hide the gauge: %q", got)
	}
}

func TestStatusLineSpinner(t *testing.T) {
	got := StatusLine("glm-5.2", "~/proj", "⠋ 1.2s", 48000, 200000, 80)
	if !strings.HasSuffix(got, "⠋ 1.2s · ctx 24% (48K/200K)") {
		t.Errorf("spinner joins the gauge on the right: %q", got)
	}
}

func TestStatusLineSmallPercentage(t *testing.T) {
	// Under 10% the gauge keeps a decimal so a fresh session does not read "0%".
	got := StatusLine("glm-5.2", "~/proj", "", 1000, 200000, 60)
	if !strings.HasSuffix(got, "ctx 0.5% (1K/200K)") {
		t.Errorf("decimal percentage: %q", got)
	}
}

func TestStatusLineTruncates(t *testing.T) {
	// cwd yields first: the model and the gauge survive, the line never wraps.
	got := StatusLine("glm-5.2", "~/a/rather/long/path/that/keeps/going", "", 48000, 200000, 40)
	if lineCells(got) > 40 {
		t.Errorf("exceeds width: %d > 40: %q", lineCells(got), got)
	}
	if !strings.HasPrefix(got, "glm-5.2 · …") || !strings.HasSuffix(got, "ctx 24% (48K/200K)") {
		t.Errorf("truncation keeps model and gauge: %q", got)
	}
	// Absurdly narrow: the left side drops entirely, the right still fits.
	got = StatusLine("glm-5.2", "~/proj", "⠋ 1.2s", 48000, 200000, 12)
	if lineCells(got) > 12 {
		t.Errorf("exceeds width: %d > 12: %q", lineCells(got), got)
	}
}

func TestStatusLineUnknownWidth(t *testing.T) {
	// stty could not measure: no right-alignment, just a separator, no panic.
	got := StatusLine("glm-5.2", "~/proj", "", 48000, 200000, 0)
	if !strings.Contains(got, "glm-5.2 · ~/proj   ctx 24%") {
		t.Errorf("unknown width falls back to a plain join: %q", got)
	}
}

func TestTruncLeft(t *testing.T) {
	if got := truncLeft("short", 10); got != "short" {
		t.Errorf("fits: %q", got)
	}
	got := truncLeft("~/Documents/DiscoverAgent", 10)
	if lineCells(got) > 10 || !strings.HasPrefix(got, "…") {
		t.Errorf("truncated to %d cells with ellipsis: %q", lineCells(got), got)
	}
	if got := truncLeft("anything", 1); got != "…" {
		t.Errorf("degenerate width: %q", got)
	}
}

// TestDockLiveZone drives the region re-split: each tool start shrinks the
// scroll region by one row, each end gives it back, and unpin erases every
// pinned row.
func TestDockLiveZone(t *testing.T) {
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()

	d := &Dock{Viable: true, rows: 24, cols: 100, stopSig: make(chan struct{})}
	d.SetStatus("glm-5.2", "~/proj", 0, 200000)
	d.Pin()
	d.mu.Lock()
	d.toolStarted("1", "bash", "go build ./...")
	d.toolStarted("2", "read", "main.go")
	d.toolEnded("1")
	d.toolEnded("2")
	d.mu.Unlock()
	d.Unpin()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	out := buf.String()

	// Region bottoms: 23 at pin, 22 and 21 as the two rows go live, back to 23.
	seq := []string{"\x1b[1;23r", "\x1b[1;22r", "\x1b[1;21r", "\x1b[1;22r", "\x1b[1;23r", "\x1b[r"}
	at := -1
	for _, want := range seq {
		next := strings.Index(out[at+1:], want)
		if next < 0 {
			t.Fatalf("sequence %q missing after %d in %q", want, at, out)
		}
		at += 1 + next
	}
	if !strings.Contains(out, "bash go build ./...") {
		t.Errorf("live row carries the label: %q", out)
	}
	if d.bottom != 0 || len(d.live) != 0 {
		t.Errorf("unpin must Reset layout state, got bottom=%d live=%d", d.bottom, len(d.live))
	}
}

func TestToolLabel(t *testing.T) {
	cases := []struct{ name, args, want string }{
		{"bash", `{"command":"go test ./..."}`, "go test ./..."},
		{"read", `{"path":"main.go"}`, "main.go"},
		{"subagent", `{"task":"find the bug","mode":"explore"}`, "explore: find the bug"},
		{"grep", `{"pattern":"TODO"}`, "TODO"},
		{"bash", `not json`, "not json"},
		{"write", `{"path":"a.go","content":"..."}`, "a.go"},
		// A todo write labels itself with the item being started, not with its
		// arguments: the row has space for one line, and of a whole list exactly
		// one line answers "what is it doing".
		{"todo", `{"todos":[{"task":"read config","status":"completed"},{"task":"raise the timeout","status":"in_progress"}]}`,
			"raise the timeout"},
		// Nothing in progress — a list written before starting, or one that is
		// finished — has no such line, so the count is the honest fallback.
		{"todo", `{"todos":[{"task":"a","status":"pending"},{"task":"b","status":"pending"}]}`, "2 item(s)"},
	}
	for _, c := range cases {
		if got := toolLabel(c.name, c.args); got != c.want {
			t.Errorf("toolLabel(%s, %s) = %q, want %q", c.name, c.args, got, c.want)
		}
	}
}

func TestLiveRowText(t *testing.T) {
	lt := liveTool{name: "bash", label: "go build ./...", start: time.Now()}
	got := liveRowText(lt, time.Now(), 100)
	if lineCells(got) > 100 {
		t.Errorf("exceeds width: %q", got)
	}
	// Model-produced text with control bytes is flattened, never emitted raw.
	evil := liveTool{name: "bash", label: "rm \x1b[2J\n-rf", start: time.Now()}
	got = liveRowText(evil, time.Now(), 100)
	if strings.ContainsAny(got, "\x1b\n") {
		t.Errorf("control bytes must be stripped: %q", got)
	}
	if !strings.Contains(got, "rm") {
		t.Errorf("content survives sanitizing: %q", got)
	}
}

func TestLiveZoneOverflow(t *testing.T) {
	d := &Dock{cols: 100}
	for i := 0; i < 8; i++ {
		d.live = append(d.live, liveTool{id: strconv.Itoa(i), name: "bash", label: "x", start: time.Now()})
	}
	texts := d.liveRowTexts(time.Now())
	if len(texts) != maxLiveRows {
		t.Fatalf("rows capped at %d, got %d", maxLiveRows, len(texts))
	}
	if !strings.Contains(texts[len(texts)-1], "+4 more") {
		t.Errorf("overflow row counts the hidden calls: %q", texts[len(texts)-1])
	}
}

// TestDockSpinTicks is the Dock's heartbeat in isolation: pin, beginRun, let
// the spinner tick, endRun, unpin, and count the redraws on a captured stdout.
// It exists because a pty-level check once "proved" the spinner dead when it
// was alive — the byte stream is the only reliable witness.
func TestDockSpinTicks(t *testing.T) {
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()

	d := &Dock{Viable: true, rows: 24, cols: 100, stopSig: make(chan struct{})}
	d.SetStatus("glm-5.2", "~/proj", 0, 200000)
	d.Pin()
	d.BeginRun()
	time.Sleep(450 * time.Millisecond)
	d.EndRun()
	d.Unpin()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	out := buf.String()
	if draws := strings.Count(out, "\x1b[24;1H"); draws < 4 {
		t.Errorf("expected pin + several tick draws + unpin, got %d", draws)
	}
	if !strings.Contains(out, "0.0s") {
		t.Errorf("spinner content missing from Dock draws")
	}
	if !strings.Contains(out, "\x1b[1;23r") || !strings.Contains(out, "\x1b[r") {
		t.Errorf("scroll region must be set on pin and Reset on unpin")
	}
	if !strings.Contains(out, "\x1b[?25l") || !strings.Contains(out, "\x1b[?25h") {
		t.Errorf("beginRun hides the cursor, endRun/unpin must bring it back")
	}
}

// TestDockInputZone drives the editor's zone: opening it shrinks the scroll
// region, candidates shrink it further, and sealing hands the rows back with
// the submitted line reprinted into the transcript.
func TestDockInputZone(t *testing.T) {
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()

	prompt := "\x1b[1m❯\x1b[0m " // what the REPL passes: bold ❯ + space
	d := &Dock{Viable: true, rows: 24, cols: 100, stopSig: make(chan struct{})}
	d.SetStatus("glm-5.2", "~/proj", 0, 200000)
	d.Pin()
	d.drawInput(prompt, []rune("he"), 2, nil)
	d.drawInput(prompt, []rune("/ex"), 3, []candidate{{"/exit", "退出"}, {"/example", "示例"}})
	d.sealInput(prompt, []rune("/exit"))
	d.Unpin()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	out := buf.String()

	// Region bottoms: 23 at pin, 22 with one input row, 20 with two candidate
	// rows added, back to 23 after the seal.
	seq := []string{"\x1b[1;23r", "\x1b[1;22r", "\x1b[1;20r", "\x1b[1;23r", "\x1b[r"}
	at := -1
	for _, want := range seq {
		next := strings.Index(out[at+1:], want)
		if next < 0 {
			t.Fatalf("sequence %q missing after offset %d", want, at)
		}
		at += 1 + next
	}
	if !strings.Contains(out, "\x1b[0m /exit\n") {
		t.Errorf("the sealed line is reprinted into the transcript: %q", out)
	}
	if !strings.Contains(out, "/example") {
		t.Errorf("candidates render in the zone: %q", out)
	}
	if d.zoneRows != 0 || d.bottom != 0 {
		t.Errorf("unpin must Reset zone state, got zoneRows=%d bottom=%d", d.zoneRows, d.bottom)
	}
}

func TestVisibleWidth(t *testing.T) {
	if got := visibleWidth("\x1b[1m❯\x1b[0m "); got != 2 {
		t.Errorf("bold-wrapped prompt counts as ❯ + space = 2 cells, got %d", got)
	}
	if got := visibleWidth("plain"); got != 5 {
		t.Errorf("plain text: got %d", got)
	}
	if got := visibleWidth("\x1b[2m暗色\x1b[0m"); got != 4 {
		t.Errorf("wide runes with escapes: got %d", got)
	}
}
