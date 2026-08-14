package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pi-go/tools"
)

// termui.go is the richer terminal UI, built in two stages:
//
// Stage 1 is the status Dock: a pinned bottom row while a run is in flight
// (spinner + elapsed + context gauge), plus a status-line formatter reused by
// the prompt.
//
// Stage 2 is the live zone: one ephemeral row per in-flight tool call, pinned
// between the transcript and the status row, so parallel tool and subagent
// work is visible while it happens (name, key argument, elapsed time). Live
// rows are never sealed into the transcript — the append-only tool lines are
// unchanged — so nothing ever needs re-wrapping, which is what a full
// pi-style document renderer would have to own. Mutating *sealed* history
// (collapsible finished boxes) remains out of scope and would need that
// renderer.
//
// The pin mechanism is a DECSTBM scroll region ("\x1b[1;{bottom}r"): the
// terminal itself keeps streamed output inside rows 1..bottom however it
// soft-wraps, so the rows below are untouchable and redraw by absolute
// addressing. The inline diff-render alternative (redraw by counting lines up
// from the cursor) breaks on soft-wrapped streaming text: the row offset
// drifts with every wrap. While a run is pinned the cursor stays at the
// region bottom (pin() parks it there, scrolling keeps it there), which is
// what makes re-splitting the region on tool start/end safe.
//
// The cost is discipline: the region must be torn down on every exit path or
// the user's shell inherits a shrunken scroll area. close() covers it and is
// deferred right after construction; process exit before the REPL starts never
// sees a region (the os.Exit paths all live in flag parsing).

// maxLiveRows caps the live zone; excess in-flight calls fold into a
// "… +N more running" row so a wide fan-out cannot eat the screen.
const maxLiveRows = 5

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// DEC private mode 2026 (synchronized output) brackets a redraw batch: a
// supporting terminal holds the frame until the batch is complete, so a
// multi-row repaint can never tear or show a half-drawn state. Terminals that
// do not know the mode ignore the unknown private sequence.
const (
	syncBegin = "\x1b[?2026h"
	syncEnd   = "\x1b[?2026l"
)

// liveTool is one in-flight tool call: one live-zone row.
type liveTool struct {
	id    string
	name  string
	label string // the one argument worth watching (command, path, task…)
	start time.Time
}

// Dock owns every pinned row at the bottom of the screen: the live zone and
// the status row. All terminal writes during a run serialize on mu: the event
// consumer holds it per event (dispatch), the spinner tick takes it per
// redraw, and diagOut is wrapped so retry notices join the same order.
type Dock struct {
	mu         sync.Mutex
	on         bool // region established, pinned rows live
	Viable     bool // terminal answered "stty size"; false disables everything
	rows       int  // terminal rows, refreshed on SIGWINCH
	cols       int
	bottom     int // current scroll-region bottom row; 0 when unpinned
	live       []liveTool
	zoneRows   int    // editor rows pinned below the region (input + candidates)
	running    bool   // a run is in flight: spinner on, cursor hidden
	lastDraw   string // last payload written by drawLocked, for tick no-op skip
	redrawHook func() // the editor's repaint entry, for SIGWINCH; mu NOT held when called
	model      string
	cwd        string
	ctxUsed    int64 // last turn's prompt tokens; 0 hides the gauge
	ctxWindow  int   // model's window; 0 hides the gauge
	runStart   time.Time
	stopSpin   chan struct{}
	spinDone   chan struct{}
	stopSig    chan struct{}
}

// NewDock probes the terminal and starts the SIGWINCH watcher. A nil-safe,
// never-nil Dock is returned; when the terminal cannot report its size the
// dock is not viable and every operation is a no-op (the run simply shows no
// spinner, matching the pre-Dock behaviour on a non-terminal).
func NewDock() *Dock {
	d := &Dock{stopSig: make(chan struct{})}
	d.rows, d.cols = TermSize()
	d.Viable = d.rows >= 4 && d.cols >= 20
	if !d.Viable {
		return d
	}
	sigc := make(chan os.Signal, 1)
	// Per-platform; see winch_unix.go and winch_other.go. On a platform with no
	// SIGWINCH the channel simply never fires.
	stopResize := notifyResize(sigc)
	go func() {
		for {
			select {
			case <-d.stopSig:
				stopResize()
				return
			case <-sigc:
				rows, cols := TermSize()
				d.mu.Lock()
				d.rows, d.cols = rows, cols
				var hook func()
				if d.on {
					// Most terminals drop the region on resize; re-establish
					// it. The stream's cursor is lost to the reflow, so clamp
					// it to the region bottom — a transient glitch that heals
					// with the next prints.
					d.bottom = d.rows - 1 - d.zoneRows - d.liveRowCount()
					d.establishLocked()
					fmt.Printf("\x1b[%d;1H", d.bottom)
					d.lastDraw = "" // the resize may have garbled rows; force
					d.drawLocked()
					hook = d.redrawHook
				}
				d.mu.Unlock()
				if hook != nil {
					hook() // the editor's drawInput locks mu itself
				}
			}
		}
	}()
	return d
}

// setStatus refreshes the data the status row shows. Called by the REPL before
// each run, so a /model switch is reflected on the very next pin.
func (d *Dock) SetStatus(model, cwd string, ctxUsed int64, ctxWindow int) {
	d.mu.Lock()
	d.model, d.cwd, d.ctxUsed, d.ctxWindow = model, cwd, ctxUsed, ctxWindow
	if d.on {
		d.drawLocked()
	}
	d.mu.Unlock()
}

// setRedrawHook registers (or clears, with nil) the editor's repaint entry.
// The SIGWINCH watcher calls it after re-laying out, so the input line and its
// candidates survive a terminal resize.
func (d *Dock) setRedrawHook(f func()) {
	d.mu.Lock()
	d.redrawHook = f
	d.mu.Unlock()
}

// beginRun switches the Dock to run mode: the spinner starts from zero and the
// cursor hides — the stream has no use for it.
func (d *Dock) BeginRun() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.on {
		return
	}
	d.running = true
	d.runStart = time.Now()
	fmt.Print("\x1b[?25l")
	d.drawLocked()
}

// endRun releases whatever rows the run pinned and parks the cursor at the
// region bottom, ready for the next prompt.
func (d *Dock) EndRun() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.on {
		return
	}
	d.running = false
	d.live = nil
	d.relayoutLocked() // shrinks away any live rows
	fmt.Printf("\x1b[%d;1H", d.bottom)
	fmt.Print("\x1b[?25h")
	d.drawLocked()
}

// drawInput renders the editor into its zone: the input (hard lines from
// newlines, each soft-wrapped across as many rows as it needs) plus the live
// completion candidates, pinned between the transcript and the status row.
// The cursor is left at the edit point — the spinner tick's save/restore
// makes that safe.
func (d *Dock) drawInput(prompt string, buf []rune, cursor int, hints []candidate) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.on {
		return
	}
	inRows, curRow, curCol, lineRows := inputLayout(prompt, buf, cursor, d.cols)
	if need := inRows + len(hints); need != d.zoneRows {
		d.setZoneRowsLocked(need)
	}
	top := d.bottom + 1
	fmt.Print(syncBegin)
	for r := top; r < top+d.zoneRows; r++ {
		fmt.Printf("\x1b[%d;1H\x1b[2K", r)
	}
	// Hard lines are drawn at absolute rows: a bare newline moves down without
	// returning to column 0, which would stair-step the continuation lines.
	row := top
	for i, seg := range strings.Split(string(buf), "\n") {
		if i == 0 {
			fmt.Printf("\x1b[%d;1H%s%s", row, prompt, seg)
		} else {
			fmt.Printf("\x1b[%d;1H%s", row, seg)
		}
		row += lineRows[i]
	}
	for i, line := range candidateLines(hints, d.cols) {
		fmt.Printf("\x1b[%d;1H%s", top+inRows+i, line)
	}
	fmt.Printf("\x1b[%d;%dH", top+curRow, curCol+1)
	fmt.Print(syncEnd)
}

// sealInput moves the submitted line into the transcript: it is reprinted at
// the region bottom (the region scrolls it into the record, hard lines, wraps
// and all), the zone rows are erased, and the region extends back over them.
func (d *Dock) sealInput(prompt string, buf []rune) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.on {
		return
	}
	fmt.Print(syncBegin)
	// \r\n: a lone \n keeps the column, and transcript lines start at column 0.
	fmt.Printf("\x1b[%d;1H\x1b[2K%s%s\n", d.bottom, prompt, strings.ReplaceAll(string(buf), "\n", "\r\n"))
	fmt.Print("\x1b7")
	for r := d.bottom + 1; r <= d.bottom+d.zoneRows; r++ {
		fmt.Printf("\x1b[%d;1H\x1b[2K", r)
	}
	d.bottom += d.zoneRows
	d.zoneRows = 0
	fmt.Printf("\x1b[1;%dr", d.bottom)
	fmt.Print("\x1b8")
	fmt.Print(syncEnd)
	// The cursor is restored to the old region bottom, inside the new region.
}

// setZoneRowsLocked re-splits the region when the editor's height changes.
// Growing scrolls the transcript up first so the new zone rows start blank;
// shrinking erases the released rows so no stale candidate text joins the
// transcript. The caller redraws the zone and places the cursor afterwards,
// so DECSTBM's cursor-homing needs no bracket here.
func (d *Dock) setZoneRowsLocked(rows int) {
	switch {
	case rows > d.zoneRows:
		g := rows - d.zoneRows
		fmt.Printf("\x1b[%d;1H", d.bottom) // the parked blank row
		for i := 0; i < g; i++ {
			fmt.Print("\n")
		}
		d.bottom -= g
		fmt.Printf("\x1b[1;%dr", d.bottom)
	case rows < d.zoneRows:
		g := d.zoneRows - rows
		fmt.Print("\x1b7")
		for r := d.bottom + 1; r <= d.bottom+g; r++ {
			fmt.Printf("\x1b[%d;1H\x1b[2K", r)
		}
		d.bottom += g
		fmt.Printf("\x1b[1;%dr", d.bottom)
		fmt.Print("\x1b8")
	}
	d.zoneRows = rows
}

// pin establishes the scroll region and starts the spinner. Idempotent per
// run: consume() pins on entry and unpins on exit.
//
// The sequence seals the prompt line into the transcript first ("\n" while the
// full screen still scrolls), because DECSTBM homes the cursor and the status
// row claims the last row — anything left there would be overwritten.
func (d *Dock) Pin() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.Viable || d.on {
		return
	}
	d.on = true
	d.runStart = time.Now()
	fmt.Print("\n")
	d.bottom = d.rows - 1
	d.establishLocked()
	// Park the cursor at the region's bottom row; the scroll this forces is
	// what moves the sealed prompt up into the region interior.
	fmt.Printf("\x1b[%d;1H\n", d.bottom)
	d.drawLocked()
	d.stopSpin = make(chan struct{})
	d.spinDone = make(chan struct{})
	go d.spin(d.stopSpin, d.spinDone)
}

// unpin tears the region down and erases every pinned row so no stale spinner
// is left behind in the scrollback (same reason the old waiting counter erased
// itself: the number is already on the usage line).
func (d *Dock) Unpin() {
	d.mu.Lock()
	if !d.on {
		d.mu.Unlock()
		return
	}
	close(d.stopSpin)
	<-d.spinDone
	d.on = false
	fmt.Print(syncBegin)
	fmt.Print("\x1b7")
	for r := d.bottom + 1; r <= d.rows; r++ {
		fmt.Printf("\x1b[%d;1H\x1b[2K", r)
	}
	fmt.Print("\x1b8")
	fmt.Print("\x1b[r\x1b[?25h") // full scroll region, cursor back
	fmt.Print(syncEnd)
	d.live = nil
	d.bottom = 0
	d.zoneRows = 0
	d.running = false
	d.lastDraw = ""
	d.mu.Unlock()
}

// close restores everything on process exit. Safe to call twice.
func (d *Dock) Close() {
	d.Unpin()
	close(d.stopSig)
}

// lockWriter returns an io.Writer that serializes with stream prints and Dock
// redraws, so a retry notice on stderr cannot interleave bytes with a redraw.
func (d *Dock) LockWriter(w io.Writer) io.Writer {
	return &lockedWriter{mu: &d.mu, w: w}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// toolStarted adds the call's live row. Called by dispatch with mu held.
func (d *Dock) toolStarted(id, name, label string) {
	d.live = append(d.live, liveTool{id: id, name: name, label: label, start: time.Now()})
	d.relayoutLocked()
}

// toolEnded drops the call's live row. An unknown id (an event that arrived
// with no matching start) must not corrupt the layout. Called by dispatch with
// mu held.
func (d *Dock) toolEnded(id string) {
	for i, t := range d.live {
		if t.id == id {
			d.live = append(d.live[:i], d.live[i+1:]...)
			break
		}
	}
	d.relayoutLocked()
}

// liveRowCount is the number of rows the live zone currently occupies,
// including the overflow row when the fan-out exceeds the cap.
func (d *Dock) liveRowCount() int {
	if len(d.live) > maxLiveRows {
		return maxLiveRows
	}
	return len(d.live)
}

// relayoutLocked re-splits the screen after the live-zone height changed.
// Growing scrolls the transcript up first so the rows the zone moves onto are
// blank; shrinking erases the released rows so no stale spinner text joins the
// transcript. DECSTBM homes the cursor, hence the save/restore brackets.
func (d *Dock) relayoutLocked() {
	if !d.on {
		return
	}
	bottom := d.rows - 1 - d.liveRowCount()
	if bottom == d.bottom {
		return // only overflow bookkeeping changed; the next tick redraws
	}
	if bottom < d.bottom { // growing
		fmt.Print(syncBegin)
		for i := 0; i < d.bottom-bottom; i++ {
			fmt.Print("\n")
		}
		fmt.Print("\x1b7")
		fmt.Printf("\x1b[1;%dr", bottom)
		fmt.Print("\x1b8")
		// The newline run left the cursor on what is now the top live row;
		// park it at the new region bottom, which that run left blank.
		fmt.Printf("\x1b[%d;1H", bottom)
		fmt.Print(syncEnd)
	} else { // shrinking
		fmt.Print(syncBegin)
		fmt.Print("\x1b7")
		for r := bottom + 1; r <= d.bottom; r++ {
			fmt.Printf("\x1b[%d;1H\x1b[2K", r)
		}
		fmt.Printf("\x1b[1;%dr", bottom)
		fmt.Print("\x1b8")
		fmt.Print(syncEnd)
		// The cursor sat at the old bottom, which is inside the new region.
	}
	d.bottom = bottom
	d.drawLocked()
}

// establishLocked (re)sets the scroll region to rows 1..d.bottom. DECSTBM
// homes the cursor; callers reposition as needed.
func (d *Dock) establishLocked() {
	fmt.Printf("\x1b[1;%dr", d.bottom)
}

// drawLocked repaints the Dock-owned rows: live zone top-down, then the
// status row. (The editor's zone rows are not its business.) Save/restore
// cursor brackets the batch so whatever the stream or the editor is doing is
// undisturbed, and every line is width-truncated so none can wrap and scroll
// the region. The payload is compared against the last draw: at an idle
// prompt nothing changes, and the 10 Hz tick becomes a no-op.
func (d *Dock) drawLocked() {
	var b strings.Builder
	b.WriteString("\x1b7")
	now := time.Now()
	for i, text := range d.liveRowTexts(now) {
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K%s", d.bottom+1+i, text)
	}
	spin := ""
	if d.running {
		frame := spinner[int(now.Sub(d.runStart)/(100*time.Millisecond))%len(spinner)]
		spin = fmt.Sprintf("%s %.1fs", frame, now.Sub(d.runStart).Seconds())
	}
	line := StatusLine(d.model, d.cwd, spin, d.ctxUsed, d.ctxWindow, d.cols)
	fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K%s", d.rows, line)
	b.WriteString("\x1b8")
	out := b.String()
	if out == d.lastDraw {
		return
	}
	d.lastDraw = out
	fmt.Print(syncBegin + out + syncEnd)
}

// liveRowTexts renders the zone's rows: one per in-flight call, plus the
// overflow row when capped.
func (d *Dock) liveRowTexts(now time.Time) []string {
	n := len(d.live)
	if n == 0 {
		return nil
	}
	shown := n
	if shown > maxLiveRows-1 {
		shown = maxLiveRows - 1
	}
	texts := make([]string, 0, d.liveRowCount())
	for i := 0; i < shown; i++ {
		texts = append(texts, liveRowText(d.live[i], now, d.cols))
	}
	if n > shown {
		texts = append(texts, Dim+fmt.Sprintf("… +%d more running", n-shown)+Reset)
	}
	return texts
}

// liveRowText renders one in-flight call: "⠋ bash go build ./… · 12.3s".
// oneLine strips control bytes first — the label is model-produced text, and
// a stray escape sequence would hijack the row it is drawn on.
func liveRowText(t liveTool, now time.Time, width int) string {
	frame := spinner[int(now.Sub(t.start)/(100*time.Millisecond))%len(spinner)]
	plain := oneLine(fmt.Sprintf("%s %s %s · %.1fs",
		frame, t.name, t.label, now.Sub(t.start).Seconds()))
	return Dim + truncEnd(plain, width) + Reset
}

// spin repaints the pinned rows at 10 Hz until stopped. It is the only writer
// besides the consumer, and mu is what keeps the two from interleaving escape
// bytes.
func (d *Dock) spin(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			d.mu.Lock()
			if d.on {
				d.drawLocked()
			}
			d.mu.Unlock()
		}
	}
}

// termSize reports the terminal's rows and columns via stty(1), the same tool
// raw mode already depends on. (0, 0) means "not a terminal we can measure".
func TermSize() (rows, cols int) {
	out, err := stty(os.Stdin, "size")
	if err != nil {
		return 0, 0
	}
	f := strings.Fields(string(out))
	if len(f) != 2 {
		return 0, 0
	}
	rows, _ = strconv.Atoi(f[0])
	cols, _ = strconv.Atoi(f[1])
	return rows, cols
}

// toolLabel picks the one argument worth watching while a call runs: the
// command for bash, the path for file tools, the mode-qualified task for a
// subagent. Anything unparseable falls back to the same summary the transcript
// line shows.
func toolLabel(name, args string) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(args), &fields); err == nil {
		if name == "subagent" {
			var mode, task string
			_ = json.Unmarshal(fields["mode"], &mode)
			_ = json.Unmarshal(fields["task"], &task)
			if task != "" {
				if mode != "" {
					return mode + ": " + task
				}
				return task
			}
		}
		// For a todo write the label is the item being started, not the arguments:
		// a whole list on one row is unreadable, and of the list exactly one line
		// answers "what is it doing".
		if name == "todo" {
			var a struct {
				Todos []tools.TodoItem `json:"todos"`
			}
			if json.Unmarshal([]byte(args), &a) == nil {
				if cur := tools.CurrentTodo(a.Todos); cur != "" {
					return cur
				}
				return fmt.Sprintf("%d item(s)", len(a.Todos))
			}
		}
		for _, k := range []string{"command", "path", "pattern", "query", "url"} {
			if v, ok := fields[k]; ok {
				var s string
				if json.Unmarshal(v, &s) == nil && s != "" {
					return s
				}
			}
		}
	}
	return Summarize(args, 80)
}

// oneLine maps control bytes to spaces: live rows are redrawn by absolute
// row, and a stray newline or escape in model-produced text would break the
// row math or worse.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// statusLine renders the one-line status bar: model · cwd on the left, spinner
// and context gauge on the right. spin is "" at the prompt (nothing is
// running); the gauge hides itself until a turn has reported usage and the
// model's window is known. width <= 0 means "unknown": no right-alignment.
//
// Layout is computed on plain text (cellsWidth is wide-char aware) and colours
// are injected afterwards, so escape bytes never disturb the column math. The
// result never exceeds width cells — the status row must not wrap.
func StatusLine(model, cwd, spin string, ctxUsed int64, ctxWindow, width int) string {
	left := model + " · " + cwd

	pct, pctText, gauge := 0.0, "", ""
	if ctxWindow > 0 && ctxUsed > 0 {
		pct = float64(ctxUsed) * 100 / float64(ctxWindow)
		// Sub-10% readings get a decimal: "ctx 0%" while a kilo-token of system
		// prompt is already loaded reads like a bug.
		pctText = fmt.Sprintf("%d%%", int(pct))
		if pct < 10 {
			pctText = fmt.Sprintf("%.1f%%", pct)
		}
		gauge = fmt.Sprintf("ctx %s (%s/%s)", pctText, HumanCtx(int(ctxUsed)), HumanCtx(ctxWindow))
	}
	right := gauge
	if spin != "" && gauge != "" {
		right = spin + " · " + gauge
	} else if spin != "" {
		right = spin
	}

	pad := 3 // arbitrary separator when the width is unknown
	if width > 0 {
		pad = width - cellsWidth([]rune(left)) - cellsWidth([]rune(right))
		if pad < 1 {
			// cwd yields first: truncate it alone, so the model name survives.
			sep := " · "
			maxCwd := width - cellsWidth([]rune(right)) - 1 - cellsWidth([]rune(model)) - cellsWidth([]rune(sep))
			if maxCwd >= 6 {
				left = model + sep + truncLeft(cwd, maxCwd)
			} else {
				// Not even the model name fits beside the gauge: drop the left
				// side and squeeze the right (the spinner is its leftmost part).
				left = ""
				right = truncLeft(right, width)
			}
			pad = width - cellsWidth([]rune(left)) - cellsWidth([]rune(right))
			if pad < 0 {
				pad = 0
			}
		}
	}

	out := left + strings.Repeat(" ", pad) + right
	if gauge != "" {
		// pi's thresholds: attention at 70%, alarm at 90%. The replace targets
		// the one number in the line, and is a no-op when colours are blanked.
		c := ""
		if pct > 90 {
			c = Red
		} else if pct > 70 {
			c = Yellow
		}
		if c != "" {
			out = strings.Replace(out, pctText, c+pctText+Dim, 1)
		}
	}
	return Dim + out + Reset
}

// truncLeft shortens s to at most max cells by dropping leading runes and
// prepending an ellipsis — the tail of a path says more than its root.
func truncLeft(s string, max int) string {
	if cellsWidth([]rune(s)) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	r := []rune(s)
	w := 0
	i := len(r)
	for i > 0 && w+runeWidth(r[i-1]) <= max-1 {
		i--
		w += runeWidth(r[i])
	}
	return "…" + string(r[i:])
}

// truncEnd shortens s to at most max cells by dropping trailing runes — the
// head of a command line says more than its tail.
func truncEnd(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if cellsWidth([]rune(s)) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	r := []rune(s)
	w := 0
	i := 0
	for i < len(r) && w+runeWidth(r[i]) <= max-1 {
		w += runeWidth(r[i])
		i++
	}
	return string(r[:i]) + "…"
}

// candidateLines renders the completion list for the editor's zone: one string
// per row, names padded to a column, descriptions dimmed, everything
// width-capped so no row can wrap and scroll the region.
func candidateLines(list []candidate, width int) []string {
	w := 0
	for _, c := range list {
		w = max(w, len(c.value)) // values are ASCII, len is the cell count
	}
	out := make([]string, len(list))
	for i, c := range list {
		line := fmt.Sprintf("  %-*s  ", w, c.value)
		if c.desc != "" {
			line += Dim + truncEnd(c.desc, width-cellsWidth([]rune(line))) + Reset
		}
		out[i] = line
	}
	return out
}

// visibleWidth counts cells in a string that may embed CSI escape sequences
// (the prompt's bold/reset brackets), which occupy none.
func visibleWidth(s string) int {
	n := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == 0x1b && i+1 < len(rs) && rs[i+1] == '[' {
			i += 2
			for i < len(rs) && !(rs[i] >= 0x40 && rs[i] <= 0x7e) {
				i++
			}
			continue
		}
		n += runeWidth(rs[i])
	}
	return n
}
