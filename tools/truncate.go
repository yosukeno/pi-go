package tools

import (
	"fmt"
	"strings"
)

// Two independent limits, whichever is hit first wins. Same numbers pi uses:
// without them a single `cat` of a large file can swallow the context window.
const (
	MaxLines = 2000
	MaxBytes = 50 * 1024
)

type Truncation struct {
	Content     string
	Truncated   bool
	By          string // "lines" | "bytes"
	TotalLines  int
	OutputLines int
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if strings.HasSuffix(s, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// TruncateHead keeps the first lines, for file reads.
func TruncateHead(s string) Truncation {
	lines := splitLines(s)
	if len(lines) <= MaxLines && len(s) <= MaxBytes {
		return Truncation{Content: s, TotalLines: len(lines), OutputLines: len(lines)}
	}
	by := "lines"
	bytes := 0
	kept := make([]string, 0, MaxLines)
	for i, line := range lines {
		if i >= MaxLines {
			break
		}
		n := len(line)
		if i > 0 {
			n++
		}
		if bytes+n > MaxBytes {
			by = "bytes"
			break
		}
		kept = append(kept, line)
		bytes += n
	}
	return Truncation{
		Content:     strings.Join(kept, "\n"),
		Truncated:   true,
		By:          by,
		TotalLines:  len(lines),
		OutputLines: len(kept),
	}
}

// TruncateTail keeps the last lines, for shell output where the error is at the end.
func TruncateTail(s string) Truncation {
	lines := splitLines(s)
	if len(lines) <= MaxLines && len(s) <= MaxBytes {
		return Truncation{Content: s, TotalLines: len(lines), OutputLines: len(lines)}
	}
	by := "lines"
	bytes := 0
	var kept []string
	for i := len(lines) - 1; i >= 0 && len(kept) < MaxLines; i-- {
		line := lines[i]
		n := len(line)
		if len(kept) > 0 {
			n++
		}
		if bytes+n > MaxBytes {
			by = "bytes"
			if len(kept) == 0 {
				// A single line longer than the limit: keep its tail, snapped to
				// a rune boundary so we never emit invalid UTF-8.
				start := len(line) - MaxBytes
				for start < len(line) && !isRuneStart(line[start]) {
					start++
				}
				kept = append(kept, line[start:])
			}
			break
		}
		kept = append([]string{line}, kept...)
		bytes += n
	}
	return Truncation{
		Content:     strings.Join(kept, "\n"),
		Truncated:   true,
		By:          by,
		TotalLines:  len(lines),
		OutputLines: len(kept),
	}
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

func formatSize(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	}
}
