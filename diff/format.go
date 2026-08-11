package diff

import (
	"fmt"
	"strconv"
	"strings"
)

// DefaultContext is how many unchanged lines surround each change. Four matches
// pi's default.
const DefaultContext = 4

// splitLines splits content into lines, dropping the empty element a trailing
// newline produces so that "a\n" and "a" both yield exactly one line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// Display renders a diff for humans: one line per change, prefixed with +/-/space
// and the line number, with unchanged runs collapsed to DefaultContext lines on
// each side. It returns the rendered text and the first changed line number in
// the new file (0 when nothing changed).
//
// This is the format the Web UI's DiffView consumes and the CLI prints.
func Display(oldContent, newContent string, context int) (string, int) {
	if context <= 0 {
		context = DefaultContext
	}
	oldLines := splitLines(oldContent)
	newLines := splitLines(newContent)
	chunks := Lines(oldLines, newLines)

	width := len(strconv.Itoa(max(len(oldLines), len(newLines))))
	num := func(n int) string { return fmt.Sprintf("%*d", width, n) }

	var (
		out               []string
		oldNum, newNum    = 1, 1
		firstChanged      int
		previousWasChange bool
	)

	for i, c := range chunks {
		switch c.Kind {
		case Insert:
			if firstChanged == 0 {
				firstChanged = newNum
			}
			for _, line := range c.Lines {
				out = append(out, "+"+num(newNum)+" "+line)
				newNum++
			}
			previousWasChange = true

		case Delete:
			if firstChanged == 0 {
				firstChanged = newNum
			}
			for _, line := range c.Lines {
				out = append(out, "-"+num(oldNum)+" "+line)
				oldNum++
			}
			previousWasChange = true

		case Equal:
			nextIsChange := i+1 < len(chunks) && chunks[i+1].Kind != Equal
			emit := func(from, to int) {
				for j := from; j < to; j++ {
					out = append(out, " "+num(oldNum)+" "+c.Lines[j])
					oldNum++
					newNum++
				}
			}
			skip := func(n int) {
				out = append(out, fmt.Sprintf("%s… %d unchanged line(s)",
					strings.Repeat(" ", width+1), n))
				oldNum += n
				newNum += n
			}

			switch {
			case previousWasChange && nextIsChange:
				// Between two changes: keep both edges, elide the middle.
				if len(c.Lines) <= context*2 {
					emit(0, len(c.Lines))
				} else {
					emit(0, context)
					skip(len(c.Lines) - context*2)
					emit(len(c.Lines)-context, len(c.Lines))
				}
			case previousWasChange:
				// Trailing context after the last change.
				if len(c.Lines) <= context {
					emit(0, len(c.Lines))
				} else {
					emit(0, context)
					oldNum += len(c.Lines) - context
					newNum += len(c.Lines) - context
				}
			case nextIsChange:
				// Leading context before the next change.
				if len(c.Lines) <= context {
					emit(0, len(c.Lines))
				} else {
					oldNum += len(c.Lines) - context
					newNum += len(c.Lines) - context
					emit(len(c.Lines)-context, len(c.Lines))
				}
			default:
				// No change anywhere near: skip entirely.
				oldNum += len(c.Lines)
				newNum += len(c.Lines)
			}
			previousWasChange = false
		}
	}
	return strings.Join(out, "\n"), firstChanged
}

// Unified renders a standard unified patch, the kind `git apply` accepts.
// Display is for reading; this is for machines and for copying out.
func Unified(path, oldContent, newContent string, context int) string {
	if context <= 0 {
		context = DefaultContext
	}
	oldLines := splitLines(oldContent)
	newLines := splitLines(newContent)

	// Flatten to per-line ops so hunk boundaries are easy to find.
	type op struct {
		kind Kind
		line string
	}
	var ops []op
	for _, c := range Lines(oldLines, newLines) {
		for _, l := range c.Lines {
			ops = append(ops, op{c.Kind, l})
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", path, path)

	oldNum, newNum := 1, 1
	for i := 0; i < len(ops); {
		if ops[i].kind == Equal {
			oldNum++
			newNum++
			i++
			continue
		}

		// Walk back over up to `context` equal lines to open the hunk.
		start := i
		for start > 0 && ops[start-1].kind == Equal && i-start < context {
			start--
		}
		// Extend forward while changes keep appearing within 2*context of each
		// other, so nearby edits share one hunk instead of fragmenting.
		end := i
		for end < len(ops) {
			if ops[end].kind != Equal {
				end++
				continue
			}
			run := 0
			for end+run < len(ops) && ops[end+run].kind == Equal {
				run++
			}
			if run > context*2 && end+run < len(ops) {
				end += context
				break
			}
			if end+run >= len(ops) {
				if run > context {
					end += context
				} else {
					end += run
				}
				break
			}
			end += run
		}

		hunkOldStart := oldNum - (i - start)
		hunkNewStart := newNum - (i - start)
		var oldCount, newCount int
		var body []string
		for j := start; j < end; j++ {
			switch ops[j].kind {
			case Equal:
				body = append(body, " "+ops[j].line)
				oldCount++
				newCount++
			case Delete:
				body = append(body, "-"+ops[j].line)
				oldCount++
			case Insert:
				body = append(body, "+"+ops[j].line)
				newCount++
			}
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", hunkOldStart, oldCount, hunkNewStart, newCount)
		for _, l := range body {
			b.WriteString(l)
			b.WriteByte('\n')
		}

		// Advance the counters past the emitted hunk.
		for j := i; j < end; j++ {
			switch ops[j].kind {
			case Equal:
				oldNum++
				newNum++
			case Delete:
				oldNum++
			case Insert:
				newNum++
			}
		}
		i = end
	}
	return b.String()
}

// Stat counts added and removed lines, for the "+3 -1" badge in the UI.
func Stat(oldContent, newContent string) (added, removed int) {
	for _, c := range Lines(splitLines(oldContent), splitLines(newContent)) {
		switch c.Kind {
		case Insert:
			added += len(c.Lines)
		case Delete:
			removed += len(c.Lines)
		}
	}
	return added, removed
}
