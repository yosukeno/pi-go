// mdstream.go — streaming markdown styling for the terminal transcript.
//
// The transcript is append-only: once a line scrolls it can never be restyled,
// so the filter streams prose at full speed and holds text back only where a
// construct genuinely needs it:
//
//   - **bold**, *italic* and `code` are ANSI toggles decided with a rune or two
//     of lookahead. Markdown's spacing rule is applied ("2 * 3" is not
//     italic), and any style still open at a newline is closed there — an
//     unbalanced marker degrades to literal text instead of leaking style into
//     the rest of the session.
//   - headers, rules and fence markers are line constructs: the first runes of
//     a line are held just long enough to classify them, never longer.
//   - a table is the one true block: its lines are buffered until a non-table
//     line arrives, because the borders need every column's width. While a
//     table streams in, its raw rows are suppressed entirely — no blank lines
//     stand in for them — and the rendered frame appears where they were.
//
// Monochrome by design: dim rules and bars, reverse video for inline code.
package tui

import (
	"fmt"
	"io"
	"strings"
)

// mdStream is the stateful filter. Text arrives in arbitrary chunks (SSE
// deltas), so all state lives across write() calls; flush() settles whatever
// is held, and is called before any non-text output.
type mdStream struct {
	w     io.Writer
	width func() int // terminal columns, read at table-render time

	atHome  bool   // emitted output sits at line start
	line    []rune // held runes of the current line (classification buffer)
	holdAll bool   // the held line is buffered to its newline (block construct)
	inFence bool
	table   [][]string // buffered rows while a table block is open

	Bold, italic, code bool
	hold               []rune // marker runes ('*', '**', '`') awaiting lookahead
}

func newMDStream(w io.Writer, width func() int) *mdStream {
	return &mdStream{w: w, width: width, atHome: true}
}

// write feeds one text delta through the filter.
func (m *mdStream) write(s string) {
	for _, r := range s {
		m.feedRune(r)
	}
}

// flush settles everything held: pending markers go out literally, a buffered
// partial line goes out raw (or joins its table), a buffered table renders.
// Called before tool lines, thinking headers and the run summary, so their
// line math stays exact.
func (m *mdStream) flush() {
	m.flushHold()
	switch {
	case m.inFence:
		if len(m.line) > 0 {
			m.emitFenceLine(m.line)
		}
		m.inFence = false // a fence spans one message; forget strays
	default:
		if len(m.line) > 0 {
			if s := string(m.line); m.atTableRow(s) {
				m.table = append(m.table, parseTableRow(s))
			} else {
				if len(m.table) > 0 {
					m.renderTable()
				}
				m.emitRawLine(m.line)
			}
		}
		if len(m.table) > 0 {
			m.renderTable()
		}
	}
	m.line = nil
	m.holdAll = false
	m.closeStyles()
}

// feedRune routes one rune: into the line buffer while a construct is being
// classified or buffered, otherwise straight into the inline-span machine.
func (m *mdStream) feedRune(r rune) {
	if m.inFence {
		if r != '\n' {
			m.line = append(m.line, r)
			return
		}
		if strings.TrimSpace(string(m.line)) == "```" {
			m.inFence = false // the closing marker and its newline are absorbed
			m.line = nil
			return
		}
		m.emitFenceLine(m.line)
		m.line = nil
		return
	}
	if m.holdAll {
		if r != '\n' {
			m.line = append(m.line, r)
			return
		}
		line := m.line
		m.line = nil
		m.holdAll = false
		m.blockLine(line)
		return
	}
	if m.atHome || len(m.line) > 0 {
		// A new line that cannot be a row closes the table block before it
		// streams — including the empty line, per markdown.
		if m.atHome && len(m.table) > 0 && r != '|' {
			m.renderTable()
		}
		// Line-start classification: hold runes until the prefix decides.
		m.line = append(m.line, r)
		switch classifyPrefix(m.line) {
		case classProse:
			held := m.line
			m.line = nil
			for _, c := range held {
				m.spanRune(c)
			}
		case classBlock:
			m.holdAll = true
		}
		return // classUndecided keeps buffering
	}
	m.spanRune(r)
}

// blockLine handles one fully buffered line that starts with a block marker.
func (m *mdStream) blockLine(line []rune) {
	s := string(line)
	switch {
	case m.atTableRow(s):
		// The row and its newline are both absorbed; the rendered frame takes
		// their place when the block closes.
		m.table = append(m.table, parseTableRow(s))
	case len(m.table) > 0:
		// The first non-table line closes the block; render, then treat this
		// line as a brand new one.
		m.renderTable()
		m.feedLineStart(line)
	case isFenceLine(s):
		m.inFence = true
		if lang := strings.TrimSpace(strings.TrimPrefix(s, "```")); lang != "" {
			m.out(Dim + "│ " + lang + Reset + "\n")
		}
	case isHeader(s):
		m.out("\x1b[1m")
		m.spanString(strings.TrimSpace(strings.TrimLeft(s, "#")))
		m.out("\x1b[22m\n")
	case isRule(s):
		w := m.width()
		if w <= 0 || w > 120 {
			w = 120
		}
		m.out(Dim + strings.Repeat("─", w) + Reset + "\n")
	default:
		m.emitRawLine(line)
	}
}

// feedLineStart re-enters the runes of a line that closed a table, as if the
// line had just begun.
func (m *mdStream) feedLineStart(line []rune) {
	for _, r := range line {
		m.feedRune(r)
	}
	m.feedRune('\n')
}

// atTableRow reports whether the line parses as a table row: pipe-delimited
// with at least two cells. A lone leading pipe is prose, not a table.
func (m *mdStream) atTableRow(s string) bool {
	if !strings.HasPrefix(s, "|") {
		return false
	}
	return len(parseTableRow(s)) >= 2
}

// parseTableRow splits "| a | b |" into trimmed cells. Escaped pipes are not
// special-cased: rare in model output, and a split cell only looks odd.
func parseTableRow(s string) []string {
	parts := strings.Split(s, "|")
	if len(parts) > 0 && strings.TrimSpace(parts[0]) == "" {
		parts = parts[1:]
	}
	if len(parts) > 0 && strings.TrimSpace(parts[len(parts)-1]) == "" {
		parts = parts[:len(parts)-1]
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// isSepRow reports the all-dashes separator row of a markdown table.
func isSepRow(cells []string) bool {
	for _, c := range cells {
		c = strings.Trim(c, ":")
		if c == "" || strings.Trim(c, "-") != "" {
			return false
		}
	}
	return len(cells) > 0
}

// renderTable draws the buffered rows with box-drawing borders. Column widths
// are cell widths (wide-char aware); too wide for the terminal, the widest
// column halves until the frame fits.
func (m *mdStream) renderTable() {
	rows := m.table
	m.table = nil
	body := rows[:0]
	for _, r := range rows {
		if !isSepRow(r) {
			body = append(body, r)
		}
	}
	if len(body) == 0 {
		return
	}
	ncols := 0
	for _, r := range body {
		ncols = max(ncols, len(r))
	}
	for i := range body {
		for len(body[i]) < ncols {
			body[i] = append(body[i], "")
		}
	}
	widths := make([]int, ncols)
	for _, r := range body {
		for i, c := range r {
			if w := cellsWidth([]rune(c)); w > widths[i] {
				widths[i] = w
			}
		}
	}
	avail := m.width()
	if avail <= 0 {
		avail = 80
	}
	total := 1 // leading bar
	for _, w := range widths {
		total += w + 3 // space, cell, space, bar
	}
	for total > avail {
		widest, w := -1, 3 // columns never shrink below 3 cells
		for i, cw := range widths {
			if cw > w {
				widest, w = i, cw
			}
		}
		if widest < 0 {
			break
		}
		nw := widths[widest] / 2
		if nw < 3 {
			nw = 3
		}
		total -= widths[widest] - nw
		widths[widest] = nw
	}

	border := func(left, mid, right string) string {
		var b strings.Builder
		b.WriteString(Dim + left)
		for i, w := range widths {
			if i > 0 {
				b.WriteString(mid)
			}
			b.WriteString(strings.Repeat("─", w+2))
		}
		b.WriteString(right + Reset + "\n")
		return b.String()
	}
	row := func(cells []string) string {
		var b strings.Builder
		b.WriteString(Dim + "│" + Reset)
		for i, c := range cells {
			c = truncEnd(c, widths[i])
			pad := widths[i] - cellsWidth([]rune(c))
			b.WriteString(" ")
			b.WriteString(m.styledString(c))
			b.WriteString(strings.Repeat(" ", pad) + " " + Dim + "│" + Reset)
		}
		b.WriteString("\n")
		return b.String()
	}

	var out strings.Builder
	out.WriteString(border("┌", "┬", "┐"))
	out.WriteString(row(body[0]))
	out.WriteString(border("├", "┼", "┤"))
	for _, r := range body[1:] {
		out.WriteString(row(r))
	}
	out.WriteString(border("└", "┴", "┘"))
	m.out(out.String())
}

// emitFenceLine prints one code line inside a fence: dim bar, raw content —
// no span styling, code is literal.
func (m *mdStream) emitFenceLine(line []rune) {
	m.out(Dim + "│ " + Reset + string(line) + "\n")
	m.atHome = true
}

// emitRawLine emits a buffered line through the span machine (inline markup
// inside still styles) plus its newline.
func (m *mdStream) emitRawLine(line []rune) {
	for _, r := range line {
		m.spanRune(r)
	}
	m.spanRune('\n')
}

// --- inline spans ---

// spanRune runs the inline-style machine over one prose rune. Markers are
// held for a rune or two of lookahead; the markdown spacing rule keeps "2 * 3"
// from going italic.
func (m *mdStream) spanRune(r rune) {
	if len(m.hold) > 0 {
		held := m.hold
		m.hold = nil
		if string(held) == "*" && r == '*' {
			m.hold = []rune{'*', '*'}
			return
		}
		m.resolveMarker(held, r)
		return
	}
	switch r {
	case '*', '`':
		m.hold = []rune{r}
	case '\n':
		m.closeStyles()
		m.out("\n")
	default:
		m.outR(r)
	}
}

// resolveMarker settles held marker runes once the lookahead rune arrives,
// then feeds the lookahead onwards (it may itself open a new marker).
func (m *mdStream) resolveMarker(held []rune, next rune) {
	switch string(held) {
	case "**":
		switch {
		case m.Bold:
			m.out("\x1b[22m")
			m.Bold = false
		case next == ' ':
			m.out("**")
		default:
			m.out("\x1b[1m")
			m.Bold = true
		}
	case "*":
		switch {
		case m.italic:
			m.out("\x1b[23m")
			m.italic = false
		case next == ' ':
			m.out("*")
		default:
			m.out("\x1b[3m")
			m.italic = true
		}
	case "`":
		if m.code {
			m.out("\x1b[27m")
			m.code = false
		} else {
			m.out("\x1b[7m")
			m.code = true
		}
	}
	m.spanRune(next)
}

// flushHold emits any held marker literally — end of a line, end of the run.
func (m *mdStream) flushHold() {
	if len(m.hold) > 0 {
		m.out(string(m.hold))
		m.hold = nil
	}
}

// closeStyles turns every open style off. Called at newlines: a span left open
// degrades to plain text rather than leaking into whatever comes next.
func (m *mdStream) closeStyles() {
	m.flushHold()
	if m.Bold {
		m.out("\x1b[22m")
		m.Bold = false
	}
	if m.italic {
		m.out("\x1b[23m")
		m.italic = false
	}
	if m.code {
		m.out("\x1b[27m")
		m.code = false
	}
}

// spanString styles a complete string (a table cell, a header) with the same
// machine the stream uses.
func (m *mdStream) spanString(s string) {
	for _, r := range s {
		m.spanRune(r)
	}
	m.flushHold()
	m.closeStyles()
}

// styledString renders a complete string into a fresh buffer — the cell path
// of renderTable, where the frame is still being assembled.
func (m *mdStream) styledString(s string) string {
	var b strings.Builder
	sub := &mdStream{w: &b, width: m.width, atHome: true}
	sub.spanString(s)
	return b.String()
}

func (m *mdStream) out(s string) {
	fmt.Fprint(m.w, s)
	if len(s) > 0 {
		m.atHome = strings.HasSuffix(s, "\n")
	}
}

func (m *mdStream) outR(r rune) {
	fmt.Fprint(m.w, string(r))
	m.atHome = r == '\n'
}

// --- line classification ---

type lineClass int

const (
	classUndecided lineClass = iota // keep buffering; the prefix is ambiguous
	classProse                      // stream the line, spans included
	classBlock                      // buffer to the newline, then decide
)

// classifyPrefix looks at the runes held at the start of a line. Only a
// handful of openers can begin a block construct; anything else is prose and
// streams immediately.
func classifyPrefix(line []rune) lineClass {
	switch line[0] {
	case '#':
		// A header is 1-6 '#' then a space; "#tag" is prose.
		for i, r := range line {
			if r == ' ' {
				if i <= 6 {
					return classBlock
				}
				return classProse
			}
			if r != '#' {
				return classProse
			}
		}
		if len(line) > 6 {
			return classProse
		}
		return classUndecided
	case '|':
		// A table candidate always buffers to the newline: only the complete
		// line shows whether it really is a row.
		return classBlock
	case '`':
		for _, r := range line {
			if r != '`' {
				return classProse
			}
		}
		if len(line) >= 3 {
			return classBlock // fence marker, possibly with a language
		}
		return classUndecided
	case '-':
		for _, r := range line {
			if r != '-' {
				return classProse // a list item ("- x") or prose dash
			}
		}
		if len(line) >= 3 {
			return classBlock // maybe a rule; the complete line decides
		}
		return classUndecided
	default:
		return classProse
	}
}

// isHeader, isFenceLine, isRule inspect fully buffered lines.
func isHeader(s string) bool {
	i := 0
	for i < len(s) && s[i] == '#' {
		i++
	}
	return i >= 1 && i <= 6 && i < len(s) && s[i] == ' '
}

func isFenceLine(s string) bool { return strings.HasPrefix(s, "```") }

func isRule(s string) bool {
	return len(s) >= 3 && strings.Trim(s, "-") == ""
}
