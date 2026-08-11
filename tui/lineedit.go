// lineedit.go — the interactive line editor: cursor movement, history, tab
// completion and multi-line input. Completion candidates are shown live under
// the prompt as you type, with their descriptions attached, so the list
// doubles as the inline command explanation and Tab is discoverable on its
// own. Enter submits; a literal newline comes from a trailing backslash +
// Enter (shell style, works everywhere), Ctrl-J, or Alt/Option+Enter, and
// bracketed paste keeps pasted newlines as text instead of firing one submit
// per line.
//
// Raw mode comes from stty(1), not a third-party termios binding: stty exists
// on every macOS and Linux box this program targets, and exec'ing it twice per
// prompt keeps go.mod at zero dependencies — a trade this project makes on
// purpose (diff/ is the same call). When there is no tty or no stty, the
// editor quietly degrades to a plain line scanner.
package tui

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"unicode/utf8"

	"github.com/yosukeno/pi-go/config"
)

// Editor owns the input side of the prompt loop.
type Editor struct {
	in      *bufio.Reader
	scanner *bufio.Scanner // fallback path
	history []string
	probed  bool
	rawOK   bool
	// Skills are the loaded skill names, for completing /skill:<name>.
	Skills []string
	// Dock, when set, pins the editor between the transcript and the status
	// row: the input line and its candidate list become absolute-addressed
	// zone rows instead of relative cursor dances. Nil keeps the legacy
	// scroll-and-redraw behaviour (no measurable terminal, no pinning).
	Dock *Dock
}

func NewEditor() *Editor {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return &Editor{in: bufio.NewReader(os.Stdin), scanner: sc}
}

const maxHistory = 200

// readLine shows prompt and returns one edited line. io.EOF is reported for
// Ctrl-D at an empty line and for the end of a non-terminal stdin.
//
// prompt must be a single line with no newline: it is reprinted on every
// redraw. The blank line that separates the prompt from the previous output
// is printed here, once.
func (e *Editor) ReadLine(prompt string) (string, error) {
	if !e.probe() {
		fmt.Print("\n" + prompt)
		if !e.scanner.Scan() {
			if err := e.scanner.Err(); err != nil {
				return "", err
			}
			return "", io.EOF
		}
		return e.scanner.Text(), nil
	}
	return e.readRaw(prompt)
}

// probe decides once whether raw-mode editing is available and sticks with the
// answer, so the two input paths never interleave their buffers.
func (e *Editor) probe() bool {
	if !e.probed {
		e.probed = true
		_, err := exec.LookPath("stty")
		e.rawOK = err == nil && IsCharDevice(os.Stdin)
	}
	return e.rawOK
}

func (e *Editor) readRaw(prompt string) (string, error) {
	restore, err := rawMode(os.Stdin)
	if err != nil {
		// stty existed at probe time but refuses now: degrade rather than die.
		e.rawOK = false
		return e.ReadLine(prompt)
	}
	// Cooked mode — and with it SIGINT on Ctrl-C — is restored before the line
	// comes back, so cancelling a run with ^C behaves exactly as before. While
	// editing, ^C is just a byte and only abandons the line.
	defer restore()
	// Bracketed paste: a paste arrives wrapped in "\x1b[200~" / "\x1b[201~", so
	// pasted newlines become text instead of a rapid-fire submit per line.
	// Terminals without support simply never send the markers.
	fmt.Print("\x1b[?2004h")
	defer fmt.Print("\x1b[?2004l")

	var buf []rune
	cursor := 0
	hist := len(e.history) // == len(history) means "the line being typed"
	var stash []rune       // that line, saved while browsing history

	// The legacy (dockless) redraw needs the grid width for its row math, and
	// it needs it once per prompt — a resize mid-edit is the Dock's problem
	// (SIGWINCH), not this path's. cols == 0 means unmeasurable: inputLayout
	// then counts hard lines only and the cursor column is best-effort.
	_, cols := TermSize()

	// redraw repaints the whole input. It runs on every change, which keeps the
	// editing code free of incremental-diff bookkeeping; at prompt length the
	// rewrite is invisible. With a Dock the input and its candidates are
	// absolute-addressed zone rows; without one, everything below the line is
	// cleared first (\x1b[J) so the live candidate list never leaves stale rows,
	// and the cursor is then walked back up to the edit point.
	redraw := func() {
		_, hints := completions(string(buf[:cursor]), e.Skills...)
		if e.Dock != nil {
			e.Dock.drawInput(prompt, buf, cursor, hints)
			return
		}
		rows, curRow, curCol, _ := inputLayout(prompt, buf, cursor, cols)
		// \r\n for hard lines: a lone \n keeps the column and would stair-step.
		fmt.Print("\r\x1b[K\x1b[J" + prompt + strings.ReplaceAll(string(buf), "\n", "\r\n"))
		up := rows - 1 - curRow
		if len(hints) > 0 {
			// The candidates for the text before the cursor are shown live, so
			// the user sees what Tab would offer without knowing Tab exists.
			fmt.Print("\r\n")
			printCandidates(hints)
			up += len(hints) + 1
		}
		if up > 0 {
			fmt.Printf("\x1b[%dA", up)
		}
		fmt.Print("\r")
		if curCol > 0 {
			fmt.Printf("\x1b[%dC", curCol)
		}
	}
	if e.Dock != nil {
		// The zone opens on the first draw. The hook lets the SIGWINCH watcher
		// repaint the editor while it sits idle.
		e.Dock.setRedrawHook(redraw)
		defer e.Dock.setRedrawHook(nil)
		redraw()
	} else {
		fmt.Print("\n" + prompt) // the one and only newline; redraws must not emit one
	}

	for {
		b, err := e.in.ReadByte()
		if err != nil {
			return "", io.EOF
		}
		switch {
		case b == '\r':
			// Shell-style continuation: a trailing backslash swallows the Enter
			// and the line goes on. Works in every terminal, no special keys.
			if n := len(buf); n > 0 && buf[n-1] == '\\' {
				buf[n-1] = '\n'
				cursor = n
				redraw()
				continue
			}
			if e.Dock != nil {
				e.Dock.sealInput(prompt, buf)
			} else {
				// Clear the live candidates below before the line scrolls.
				fmt.Print("\r\x1b[K" + prompt + strings.ReplaceAll(string(buf), "\n", "\r\n") + "\x1b[J\n")
			}
			e.pushHistory(string(buf))
			return string(buf), nil

		case b == '\n': // ^J — icrnl is off, so Enter is CR and this is distinct
			buf = insertRunes(buf, cursor, []rune{'\n'})
			cursor++
			redraw()

		case b == 0x03: // ^C: abandon the line, keep the session alive
			if e.Dock != nil {
				e.Dock.sealInput(prompt, append(append([]rune{}, buf...), '^', 'C'))
			} else {
				fmt.Print("^C\x1b[J\n")
			}
			buf, cursor, hist = nil, 0, len(e.history)
			redraw()

		case b == 0x04: // ^D: EOF on an empty line, delete-forward otherwise
			if len(buf) == 0 {
				if e.Dock == nil {
					fmt.Print("\n")
				}
				return "", io.EOF
			}
			if cursor < len(buf) {
				buf = deleteRune(buf, cursor)
				redraw()
			}

		case b == '\t':
			// The candidates are already on screen (redraw shows them live),
			// so Tab only has to insert what is unambiguous.
			insert, _ := completions(string(buf[:cursor]), e.Skills...)
			if insert != "" {
				rs := []rune(insert)
				buf = insertRunes(buf, cursor, rs)
				cursor += len(rs)
			}
			redraw()

		case b == 0x7f || b == 0x08:
			if cursor > 0 {
				buf = deleteRune(buf, cursor-1)
				cursor--
				redraw()
			}

		case b == 0x1b:
			e.escape(&buf, &cursor, &hist, &stash, redraw)

		case b < 0x20:
			// Other control bytes are ignored.

		default:
			r := e.readRune(b)
			if r == utf8.RuneError {
				continue
			}
			buf = insertRunes(buf, cursor, []rune{r})
			cursor++
			redraw()
		}
	}
}

// escape consumes one escape sequence (arrows, Home/End, Delete) and applies
// it. A bare ESC blocks until the next byte arrives — the standard price of
// parsing escape sequences without a read timeout.
func (e *Editor) escape(buf *[]rune, cursor *int, hist *int, stash *[]rune, redraw func()) {
	b, err := e.in.ReadByte()
	if err != nil {
		return
	}
	var params string
	switch b {
	case '[':
		for {
			if b, err = e.in.ReadByte(); err != nil {
				return
			}
			if b >= 0x40 && b <= 0x7e { // final byte
				break
			}
			params += string(b)
		}
	case 'O':
		if b, err = e.in.ReadByte(); err != nil {
			return
		}
	case '\r', '\n': // Alt/Option+Enter: a literal newline, no submit
		*buf = insertRunes(*buf, *cursor, []rune{'\n'})
		*cursor++
		redraw()
		return
	default:
		return
	}

	switch b {
	case 'A': // up: older history
		if *hist > 0 {
			if *hist == len(e.history) {
				*stash = append((*stash)[:0], (*buf)...)
			}
			*hist--
			*buf = []rune(e.history[*hist])
			*cursor = len(*buf)
			redraw()
		}
	case 'B': // down: newer history, then the stashed line
		if *hist < len(e.history) {
			*hist++
			if *hist == len(e.history) {
				*buf = *stash
			} else {
				*buf = []rune(e.history[*hist])
			}
			*cursor = len(*buf)
			redraw()
		}
	case 'C': // right one rune; wide runes cost two cells
		if *cursor < len(*buf) {
			fmt.Printf("\x1b[%dC", runeWidth((*buf)[*cursor]))
			*cursor++
		}
	case 'D': // left one rune
		if *cursor > 0 {
			*cursor--
			fmt.Printf("\x1b[%dD", runeWidth((*buf)[*cursor]))
		}
	case 'H':
		*cursor = 0
		redraw()
	case 'F':
		*cursor = len(*buf)
		redraw()
	case '~':
		if params == "3" && *cursor < len(*buf) { // Delete key
			*buf = deleteRune(*buf, *cursor)
			redraw()
		}
		if params == "200" { // bracketed paste start: read to the terminator
			rs := []rune(normalizePaste(e.readPaste()))
			*buf = insertRunes(*buf, *cursor, rs)
			*cursor += len(rs)
			redraw()
		}
	}
}

// rawMode switches the tty to byte-at-a-time reads with echo, signal
// generation and CR-to-NL mapping off, and returns the restore function.
// -icrnl is what makes Ctrl-J distinguishable from Enter: Enter arrives as CR,
// Ctrl-J as LF, so LF can insert a literal newline while CR submits.
func rawMode(f *os.File) (func(), error) {
	saved, err := stty(f, "-g")
	if err != nil {
		return nil, err
	}
	if _, err := stty(f, "-icanon", "-echo", "-isig", "-icrnl", "min", "1", "time", "0"); err != nil {
		return nil, err
	}
	return func() { _, _ = stty(f, strings.TrimSpace(saved)) }, nil
}

// stty runs stty(1) against the given terminal.
func stty(f *os.File, args ...string) (string, error) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = f
	out, err := cmd.Output()
	return string(out), err
}

// pushHistory appends a submitted line, skipping empties and immediate repeats.
func (e *Editor) pushHistory(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if n := len(e.history); n > 0 && e.history[n-1] == line {
		return
	}
	e.history = append(e.history, line)
	if len(e.history) > maxHistory {
		e.history = e.history[len(e.history)-maxHistory:]
	}
}

// readRune decodes one rune whose first byte is b, pulling continuation bytes
// as needed. Garbage comes back as RuneError and is dropped by the caller.
func (e *Editor) readRune(b byte) rune {
	if b < utf8.RuneSelf {
		return rune(b)
	}
	if b < 0xC2 { // stray continuation byte or overlong lead
		return utf8.RuneError
	}
	bs := []byte{b}
	for !utf8.FullRune(bs) && len(bs) < 4 {
		nb, err := e.in.ReadByte()
		if err != nil {
			return utf8.RuneError
		}
		bs = append(bs, nb)
	}
	r, _ := utf8.DecodeRune(bs)
	return r
}

// readPaste consumes one bracketed-paste body: every byte up to the
// terminator "\x1b[201~". The body is returned raw — newlines and all — which
// is the point of the protocol: a paste is text, never keystrokes.
func (e *Editor) readPaste() string {
	var b []byte
	for {
		c, err := e.in.ReadByte()
		if err != nil {
			break
		}
		b = append(b, c)
		if bytes.HasSuffix(b, []byte("\x1b[201~")) {
			b = b[:len(b)-len("\x1b[201~")]
			break
		}
	}
	return string(b)
}

// normalizePaste converts pasted line endings to "\n": the clipboard's bytes
// from a Windows-leaning source arrive as CRLF, and a lone CR is old-Mac style.
func normalizePaste(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func insertRunes(buf []rune, at int, rs []rune) []rune {
	buf = append(buf, rs...)
	copy(buf[at+len(rs):], buf[at:]) // shift the tail right, then overwrite
	copy(buf[at:], rs)
	return buf
}

func deleteRune(buf []rune, at int) []rune {
	copy(buf[at:], buf[at+1:])
	return buf[:len(buf)-1]
}

// candidate is one possible completion. desc is shown next to the value when
// several candidates are listed — that list doubles as the inline command
// explanation.
type candidate struct {
	value, desc string
}

// Command describes one interactive slash command. Commands is the single
// source of truth for the interactive commands: /help renders from it and Tab
// completion matches against it, so the two can never disagree. Usage carries
// the argument hint for the help listing; Name alone is what completion
// inserts.
type Command struct {
	Name, Usage, En, Zh string
	// NoAbbrev keeps this command out of prefix expansion on Enter. Tab still
	// completes it, and the difference is the point: Tab puts the full name on
	// screen before anything runs, while Enter on a prefix acts on a guess. Set for
	// commands that cannot be undone — /compact is the only one today, and without
	// this "/c" plus Enter would replace the whole conversation, since no other
	// command starts with those two characters.
	NoAbbrev bool
}

// Commands lists the interactive commands, in help order.
var Commands = []Command{
	{Name: "/model", Usage: "/model [name]", En: "Show the current model, or switch to the named one. History is carried over.", Zh: "显示当前模型，或切换模型；对话历史原样保留"},
	{Name: "/models", Usage: "/models", En: "List known models.", Zh: "列出已知模型"},
	{Name: "/usage", Usage: "/usage", En: "Token totals for this session.", Zh: "本次会话的 token 累计"},
	{Name: "/compact", Usage: "/compact", NoAbbrev: true, En: "Replace the conversation with a summary of it. Costs one model call; the full transcript stays in the session file.", Zh: "用一份摘要替换当前对话，花一次模型调用；完整记录仍留在会话文件里"},
	{Name: "/skills", Usage: "/skills", En: "List the loaded skills.", Zh: "列出已加载的 skills"},
	{Name: "/skill:", Usage: "/skill:<name> [args]", En: "Load a skill's full instructions into this turn.", Zh: "把某个 skill 的完整说明注入本轮对话"},
	{Name: "/auto", Usage: "/auto", En: "Approval mode. Only meaningful with -web: the terminal has no gate.", Zh: "审批模式，只在 -web 下有意义；终端没有闸门"},
	{Name: "/strict", Usage: "/strict", En: "Approval mode. Only meaningful with -web: the terminal has no gate.", Zh: "审批模式，只在 -web 下有意义；终端没有闸门"},
	{Name: "/standard", Usage: "/standard", En: "Approval mode. Only meaningful with -web: the terminal has no gate.", Zh: "审批模式，只在 -web 下有意义；终端没有闸门"},
	{Name: "/help", Usage: "/help", En: "Show this command list.", Zh: "显示本命令列表"},
	{Name: "/exit", Usage: "/exit", En: "Leave.", Zh: "退出"},
	{Name: "/quit", Usage: "/quit", En: "Leave.", Zh: "退出"},
}

// completions looks at the line before the cursor and returns the two things
// the editor needs: the text Tab should insert at the cursor, and the
// candidate list redraw shows live under the prompt. Three regions complete:
// a slash command at the start of the line, a model name after "/model ", and
// a skill name after "/skill:". Anything else — prompts, paths — is left alone.
//
// skillNames is variadic so that the tests, which care about commands and models,
// do not have to invent a skill set to call this.
func completions(before string, skillNames ...string) (insert string, list []candidate) {
	if !strings.HasPrefix(before, "/") {
		return "", nil
	}
	token := before
	var cands []candidate
	// This branch comes first because "/skill:" is not a prefix of any command
	// name, so the command branch would find nothing and Tab would go dead
	// exactly where the names are worth completing.
	if _, ok := strings.CutPrefix(before, "/skill:"); ok {
		cands = skillCandidates(before, skillNames)
	} else if rest, ok := strings.CutPrefix(before, "/model "); ok {
		token = rest
		cands = modelCandidates(rest)
	} else if !strings.Contains(before, " ") {
		for _, c := range Commands {
			if strings.HasPrefix(c.Name, before) {
				cands = append(cands, candidate{c.Name, c.Zh})
			}
		}
	} else {
		return "", nil
	}
	if len(cands) == 0 {
		return "", nil
	}
	if len(cands) == 1 {
		return cands[0].value[len(token):] + " ", cands
	}
	values := make([]string, len(cands))
	for i, c := range cands {
		values[i] = c.value
	}
	if cp := commonPrefix(values); len(cp) > len(token) {
		return cp[len(token):], cands
	}
	return "", cands
}

// skillCandidates completes "/skill:<name>". Candidate values carry the whole
// prefix so the insert arithmetic in completions stays uniform across regions.
func skillCandidates(before string, names []string) []candidate {
	var out []candidate
	for _, n := range names {
		full := "/skill:" + n
		if strings.HasPrefix(full, before) {
			out = append(out, candidate{full, "skill"})
		}
	}
	return out
}

// modelCandidates matches a token against every model id and alias. Matching
// an alias completes the alias: it resolves to the same model and mirrors what
// the user typed.
func modelCandidates(token string) []candidate {
	lower := strings.ToLower(token)
	var out []candidate
	for _, m := range config.Catalog() {
		for _, name := range append([]string{m.ID}, m.Aliases...) {
			if !strings.HasPrefix(strings.ToLower(name), lower) {
				continue
			}
			desc := m.Provider
			if name != m.ID {
				desc += ", alias of " + m.ID
			}
			out = append(out, candidate{name, desc})
		}
	}
	return out
}

// commonPrefix shrinks byte-wise, which is safe here because every completion
// value (command names, model ids, aliases) is ASCII.
func commonPrefix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	cp := ss[0]
	for _, s := range ss[1:] {
		for !strings.HasPrefix(s, cp) {
			cp = cp[:len(cp)-1]
		}
	}
	return cp
}

// printCandidates renders the candidate list below the prompt, names padded
// to a column. The description column is what makes the list double as the
// inline command help.
func printCandidates(list []candidate) {
	w := 0
	for _, c := range list {
		w = max(w, len(c.value)) // values are ASCII, len is the cell count
	}
	for _, c := range list {
		fmt.Printf("  %-*s  %s%s%s\n", w, c.value, Dim, c.desc, Reset)
	}
}

// runeWidth approximates wcwidth: 0 for control bytes, 2 for the wide ranges
// (CJK, fullwidth forms, Hangul), 1 otherwise. Combining marks count as 1 —
// wrong on a cell grid, but rare in what gets typed here.
func runeWidth(r rune) int {
	switch {
	case r < 0x20 || r == 0x7f:
		return 0
	case r >= 0x1100 && (r <= 0x115F || // Hangul Jamo
		r == 0x2329 || r == 0x232A || // angle brackets
		r >= 0x2E80 && r <= 0xA4CF && r != 0x303F || // CJK radicals through Yi
		r >= 0xAC00 && r <= 0xD7A3 || // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF || // CJK compatibility ideographs
		r >= 0xFE30 && r <= 0xFE4F || // CJK compatibility forms
		r >= 0xFF00 && r <= 0xFF60 || // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6 || // fullwidth signs
		r >= 0x20000 && r <= 0x3FFFD): // CJK extension B and beyond
		return 2
	}
	return 1
}

func cellsWidth(rs []rune) int {
	n := 0
	for _, r := range rs {
		n += runeWidth(r)
	}
	return n
}

// inputLayout measures how the editor's prompt + buffer occupy the grid: the
// buffer is split into hard lines at each newline, and each hard line
// soft-wraps at cols. It reports the total row count, the cursor's row and
// column, and each hard line's row count (so the Dock can advance absolute
// rows without re-walking the text). The prompt prefixes only the first hard
// line; continuation lines start at column 0. cols <= 0 means the grid is
// unmeasurable: every hard line then claims one row and the cursor column is
// the raw cell count — a best-effort answer for a terminal that cannot report
// its size.
func inputLayout(prompt string, buf []rune, cursor, cols int) (rows, curRow, curCol int, lineRows []int) {
	if cursor > len(buf) {
		cursor = len(buf)
	}
	pw := visibleWidth(prompt)
	found := false
	off := 0 // rune index where the current hard line starts
	for i, seg := range strings.Split(string(buf), "\n") {
		rs := []rune(seg)
		lead := 0
		if i == 0 {
			lead = pw
		}
		lr := 1
		if cols > 0 {
			// floor + 1: a line that ends exactly at the right edge still owns
			// the next row, because that is where its cursor rests.
			lr = (lead+cellsWidth(rs))/cols + 1
		}
		lineRows = append(lineRows, lr)
		if !found && cursor <= off+len(rs) {
			found = true
			cw := lead + cellsWidth(buf[off:cursor])
			curRow, curCol = rows, cw
			if cols > 0 {
				curRow += cw / cols
				curCol = cw % cols
			}
		}
		rows += lr
		off += len(rs) + 1 // the newline itself
	}
	return
}
