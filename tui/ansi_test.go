package tui

import (
	"fmt"
	"strings"
	"testing"
)

func TestSgrOnlyKeepsColourDropsMotion(t *testing.T) {
	in := "ok\x1b[2J\x1b[1;1H\x1b[31mred\x1b[0m plain"
	want := "ok\x1b[31mred\x1b[0m plain"
	if got := sgrOnly(in); got != want {
		t.Errorf("sgrOnly(%q) = %q, want %q", in, got, want)
	}
}

func TestSgrOnlyDropsOSC(t *testing.T) {
	in := "\x1b]8;;https://evil.example\x07link\x1b]8;;\x1b\\x"
	if got := sgrOnly(in); got != "linkx" {
		t.Errorf("sgrOnly(%q) = %q, want %q", in, got, "linkx")
	}
}

func TestSgrOnlyDropsUnterminated(t *testing.T) {
	for _, in := range []string{"tail\x1b[3", "tail\x1b]", "tail\x1b"} {
		if got := sgrOnly(in); got != "tail" {
			t.Errorf("sgrOnly(%q) = %q, want %q", in, got, "tail")
		}
	}
}

func TestSgrOnlyPassesPlainTextThrough(t *testing.T) {
	in := "no escapes here, 中文也行"
	if got := sgrOnly(in); got != in {
		t.Errorf("sgrOnly(%q) = %q, want it unchanged", in, got)
	}
}

func TestSummarizeSanitizesAfterTruncating(t *testing.T) {
	// The cut lands mid-sequence; the partial escape must not reach the
	// terminal, while a complete one survives.
	in := "\x1b[32m" + strings.Repeat("a", 300) + "\x1b[0m"
	got := Summarize(in, 240)
	if strings.Contains(got, "\x1b[0m") {
		t.Errorf("truncated away the Reset but summarize kept it: %q", got[len(got)-20:])
	}
	if !strings.HasPrefix(got, "\x1b[32m") {
		t.Errorf("complete leading SGR should survive: %q", got[:12])
	}
}

// Colour variables are blank when tests run with piped stdout, so the
// expectations are built from the same variables instead of literal escapes.
func TestLsSummaryColoursDirectories(t *testing.T) {
	got := lsSummary("agent/\nmain.go\nREADME.md", 100)
	want := DirBlue + "agent/" + Reset + " " + Dim + "main.go" + Reset + " " + Dim + "README.md" + Reset
	if got != want {
		t.Errorf("lsSummary = %q, want %q", got, want)
	}
}

func TestLsSummaryBoundsWidthAndCountsTheRest(t *testing.T) {
	got := lsSummary("aaaa\nbbbb\ncccc", 7)
	want := Dim + "aaaa" + Reset + " " + Dim + fmt.Sprintf("… (+%d)", 2) + Reset
	if got != want {
		t.Errorf("lsSummary = %q, want %q", got, want)
	}
}

func TestLsSummarySkipsTheTruncationNote(t *testing.T) {
	got := lsSummary("agent/\nmain.go\n\n[500 entries limit reached, use limit=1000 for more. 32KB limit reached]", 100)
	if strings.Contains(got, "limit") {
		t.Errorf("note line should not be an entry: %q", got)
	}
}

// Colour variables are blank when tests run with piped stdout, so the
// expectations are built from the same variables instead of literal escapes.
func TestColorLongLineColoursDirectoryNames(t *testing.T) {
	in := "drwxr-xr-x@ 31 wangy staff 992 Aug  6 19:18 tools"
	got := colorLongLine(in)
	want := "drwxr-xr-x@ 31 wangy staff 992 Aug  6 19:18 " + DirBlue + "tools" + Reset
	if got != want {
		t.Errorf("colorLongLine(%q) = %q, want %q", in, got, want)
	}
}

func TestColorLongLineColoursSymlinksAndExecutables(t *testing.T) {
	Link := colorLongLine("lrwxrwxrwx 1 wangy staff 10 Aug 6 19:18 bin -> /usr/bin")
	if !strings.Contains(Link, Cyan+"bin -> /usr/bin"+Reset) {
		t.Errorf("symlink name should be Cyan: %q", Link)
	}
	exe := colorLongLine("-rwxr-xr-x 1 wangy staff 10 Aug 6 19:18 run.sh")
	if !strings.Contains(exe, Green+"run.sh"+Reset) {
		t.Errorf("executable name should be Green: %q", exe)
	}
}

func TestColorLongLineLeavesAloneWhatIsNotAListing(t *testing.T) {
	for _, in := range []string{
		"total 56",
		"-rw-r--r-- 1 wangy staff 10 Aug 6 19:18 note.txt", // plain file: no colour
		"\x1b[31mdrwxr-xr-x 1 a b 1 Aug 6 19:18 x\x1b[0m",  // already coloured
	} {
		if got := colorLongLine(in); got != in {
			t.Errorf("colorLongLine(%q) = %q, want it unchanged", in, got)
		}
	}
}

func TestLink(t *testing.T) {
	// go test's stdout is a pipe, so init blanked interactive: pass-through.
	if got := Link("file:///tmp/x", "x"); got != "x" {
		t.Errorf("non-terminal must pass the text through, got %q", got)
	}
	interactive = true
	defer func() { interactive = false }()
	got := Link("file:///tmp/x", "x")
	want := "\x1b]8;;file:///tmp/x\x1b\\x\x1b]8;;\x1b\\"
	if got != want {
		t.Errorf("OSC 8 wrap: got %q, want %q", got, want)
	}
	if got := Link("", "x"); got != "x" {
		t.Errorf("empty URI links nothing: got %q", got)
	}
}

func TestFileURL(t *testing.T) {
	if got := FileURL("/tmp/a b/100%.md"); got != "file:///tmp/a%20b/100%25.md" {
		t.Errorf("space and percent are encoded: %q", got)
	}
	if got := FileURL("/tmp/中文.md"); got != "file:///tmp/中文.md" {
		t.Errorf("UTF-8 passes through: %q", got)
	}
}
