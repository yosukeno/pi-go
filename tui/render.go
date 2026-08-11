package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/wangy/pi-go/agent"
	"github.com/wangy/pi-go/skills"
	"github.com/wangy/pi-go/tools"
)

// Renderer is the terminal consumer of the loop's event stream. Swapping it for
// a JSON or SSE writer is the whole reason the loop emits events instead of
// printing; the other consumers live in main (jsonmode.go) and web.
type Renderer struct {
	quiet bool
	// skills and cwd are only used to label a read that is loading a skill. The
	// label is derived from the call's arguments rather than its result details,
	// because arguments are what survives into the session file.
	skills     []skills.Skill
	cwd        string
	inThinking bool
	atLineHome bool
	// inFlight counts tool calls started but not yet finished, which is how the
	// renderer notices a batch is running in parallel.
	inFlight        int
	batchOverlapped bool

	// streamCallID is the call whose output is currently being printed live, or
	// empty. Only one at a time: two commands writing to the same terminal
	// interleave into something unreadable, and there are no panes here to
	// separate them.
	streamCallID string
	// streamHeld is the trailing incomplete line, kept back so every printed line
	// can carry its prefix.
	streamHeld  string
	streamLines int

	// Dock is the pinned status row shown during runs, nil off a terminal.
	// LastCtxInput is the last turn's prompt-token count — the one signal that
	// says how full the context window is — mirrored into the dock under its
	// lock and read by the REPL's prompt-time status line.
	Dock         *Dock
	LastCtxInput int64
	// md is the streaming markdown filter for assistant text, nil when output
	// is not a terminal: a piped transcript must stay byte-exact.
	md *mdStream
}

// NewRenderer builds the terminal front end. When stdout can be drawn on and
// the run is not in JSON mode it also gets the pinned dock and the markdown
// filter; anything else keeps the plain, byte-exact output.
func NewRenderer(quiet bool, skillList []skills.Skill, cwd string, jsonMode bool) *Renderer {
	r := &Renderer{quiet: quiet, skills: skillList, cwd: cwd}
	if interactive && !jsonMode {
		r.Dock = NewDock()
		r.md = newMDStream(os.Stdout, func() int {
			if r.Dock != nil && r.Dock.cols > 0 {
				return r.Dock.cols
			}
			_, c := TermSize()
			return c
		})
	}
	return r
}

// Consume drains one run's events onto the terminal, returning the run's error.
//
// A run with no session around it (one-shot -p) pins the dock itself; in
// the REPL the session is already pinned and the caller wraps the run in
// BeginRun/EndRun instead.
func (r *Renderer) Consume(events <-chan agent.Event) error {
	if r.Dock != nil && !r.Dock.on {
		r.Dock.Pin()
		r.Dock.BeginRun()
		defer r.Dock.Unpin()
	}
	for e := range events {
		// The spinner tick is the only other writer on the terminal; the lock
		// keeps its redraw from interleaving escape bytes with the stream.
		if r.Dock != nil {
			r.Dock.mu.Lock()
		}
		err := r.dispatch(e)
		if r.Dock != nil {
			r.Dock.mu.Unlock()
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// dispatch prints one event. What used to be "continue" in Consume's switch is
// "return nil" here; the one error path is a run that ended badly.
func (r *Renderer) dispatch(e agent.Event) error {
	switch e.Kind {
	case agent.EventTurnStart:
		// Announced because the alternative is a context gauge that drops for no
		// visible reason, and because the model is about to be told that output it
		// produced is gone — someone reading the transcript afterwards needs to know
		// that happened rather than inferring it from a placeholder.
		// On stdout with the rest of the transcript, not on stderr: this is something
		// that happened in the conversation, not a diagnostic about the process. JSON
		// mode never reaches here — it has its own consumer, and the same fact travels
		// as a field on the turn_start event.
		if ce := e.ContextEdit; ce != nil && !r.quiet {
			r.mdFlush()
			r.newline()
			// Arguments are named separately rather than folded into the total,
			// because the two have different remedies: results coming back is a
			// re-read, whereas arguments coming back means a file was written whose
			// content is no longer in the conversation.
			args := ""
			if ce.ClearedArgs > 0 {
				args = fmt.Sprintf(" + %d call argument(s) ~%s",
					ce.ClearedArgs, thousands(ce.ClearedArgTokens))
			}
			fmt.Printf("%s… context edit: dropped %d old tool result(s), ~%s tokens%s (prompt was ~%s)%s\n",
				Dim, ce.ClearedResults, thousands(ce.ClearedTokens), args,
				thousands(ce.PromptTokens), Reset)
		}

	case agent.EventMessage:
		// That turn's own usage, not the accumulated total: it is the only
		// signal that says how full the context window currently is. The dock
		// picks the change up on its next tick; the REPL reads the field for
		// the prompt-time status line.
		r.LastCtxInput = e.Usage.Input
		if r.Dock != nil {
			r.Dock.ctxUsed = e.Usage.Input
		}

	case agent.EventSteer:
		// The CLI has no steering entry point, so the message that lands here
		// today is the loop's own soft-cap checkpoint. Shown dimmed like the
		// context-edit line: it entered the conversation, so it belongs on
		// stdout — and without it the model's next answer ("here is what is
		// left…") would read as answering a question nobody asked.
		if r.quiet {
			return nil
		}
		r.mdFlush()
		r.newline()
		fmt.Printf("%s… %s%s\n", Dim, e.Text, Reset)

	case agent.EventThinkingDelta:
		if r.quiet {
			return nil
		}
		if !r.inThinking {
			r.mdFlush()
			r.newline()
			fmt.Print(Dim + "thinking: ")
			r.inThinking = true
		}
		fmt.Print(e.Text)

	case agent.EventTextDelta:
		r.endThinking()
		r.mdWrite(e.Text)

	case agent.EventToolStart:
		r.mdFlush()
		r.endThinking()
		r.newline()
		r.inFlight++
		if r.inFlight > 1 {
			r.batchOverlapped = true
		}
		badge := ""
		if e.ToolName == "read" {
			if s, ok := skills.MatchRead(r.skills, r.cwd, json.RawMessage(e.ToolArgs)); ok {
				badge = fmt.Sprintf(" %s[skill %s]%s", Green, s.Name, Reset)
			}
		}
		fmt.Printf("%s· %s%s %s%s\n", Cyan, e.ToolName, Reset, Summarize(e.ToolArgs, 120), badge)
		if r.Dock != nil {
			r.Dock.toolStarted(e.ToolCallID, e.ToolName, toolLabel(e.ToolName, e.ToolArgs))
		}

	case agent.EventToolPartial:
		r.streamPartial(e)

	case agent.EventToolEnd:
		r.mdFlush()
		if r.Dock != nil {
			r.Dock.toolEnded(e.ToolCallID)
		}
		// Tools in a parallel batch finish out of order, so a result line has
		// to name its tool or it cannot be matched to the call above. The flag
		// persists for the whole batch: labelling only while two are in flight
		// would leave the last line of a parallel batch unlabelled.
		label := ""
		if r.batchOverlapped {
			label = e.ToolName + " "
		}
		if r.inFlight > 0 {
			r.inFlight--
		}
		if r.inFlight == 0 {
			r.batchOverlapped = false
		}
		if r.quiet {
			return nil
		}
		// Output that already scrolled past must not be printed a second time,
		// so a streamed call ends with a status line instead of its body.
		if r.streamCallID == e.ToolCallID {
			r.endStream(e)
			return nil
		}
		if e.IsError {
			fmt.Printf("%s  ! %s%s%s\n", Red, label, Summarize(e.ToolOutput, 240), Reset)
			return nil
		}
		// A diff says more than "replaced 1 block(s)", so prefer it when the
		// tool produced one.
		if body, stat := diffOf(e.ToolDetails); body != "" {
			fmt.Printf("%s  %s%s%s\n", Dim, label, stat, Reset)
			printDiff(body, maxDiffLines)
			return nil
		}
		// Command output keeps its own SGR colours at full strength: wrapping
		// it in dim would mute exactly the signal (a red failure) they carry.
		if e.ToolName == "bash" {
			fmt.Printf("  %s%s%s\n", label, Summarize(e.ToolOutput, 240), Reset)
			return nil
		}
		// A listing deserves its names, not "agent/ (+24 lines)": directories
		// in blue, files dimmed, bounded to one line's worth of width.
		if e.ToolName == "ls" {
			fmt.Printf("  %s%s\n", label, lsSummary(e.ToolOutput, 100))
			return nil
		}
		// The task list is the one result worth printing in full every time.
		// Summarize would flatten it onto one line, and a plan collapsed onto one
		// line is the shape it is least readable in — while being readable was the
		// entire reason for writing it down.
		if todos, ok := todosOf(e.ToolDetails); ok {
			printTodos(label, todos)
			return nil
		}
		fmt.Printf("%s  %s%s%s\n", Dim, label, Summarize(e.ToolOutput, 240), Reset)

	case agent.EventAgentEnd:
		r.mdFlush()
		r.endThinking()
		r.newline()
		if e.Err != nil {
			return e.Err
		}
		if !r.quiet {
			fmt.Printf("%s[%s]%s\n", Dim, usageLine(e.Usage, e.Timing), Reset)
		}
	}
	return nil
}

func (r *Renderer) endThinking() {
	if r.inThinking {
		fmt.Print(Reset + "\n")
		r.inThinking = false
		r.atLineHome = true
	}
}

// mdWrite styles an assistant text delta through the markdown filter when one
// is attached (interactive output); raw bytes otherwise — a piped transcript
// must stay byte-exact.
func (r *Renderer) mdWrite(s string) {
	if r.md == nil {
		fmt.Print(s)
		r.atLineHome = strings.HasSuffix(s, "\n")
		return
	}
	r.md.write(s)
	r.atLineHome = r.md.atHome
}

// mdFlush settles any held markdown (a partial line, a buffered table) before
// non-text output prints, so tool lines and summaries keep their line math.
func (r *Renderer) mdFlush() {
	if r.md == nil {
		return
	}
	r.md.flush()
	r.atLineHome = r.md.atHome
}

func (r *Renderer) newline() {
	if !r.atLineHome {
		fmt.Println()
		r.atLineHome = true
	}
}

// streamPartial prints a running command's output as it arrives.
//
// It is deliberately conservative about when it does this. A parallel batch is
// skipped entirely: two commands writing to one terminal interleave into
// something unreadable, and unlike a UI with panes there is nowhere to put the
// second one. Skipping costs nothing, because the settled output still arrives
// with tool_end.
func (r *Renderer) streamPartial(e agent.Event) {
	if r.quiet || r.inFlight != 1 || r.batchOverlapped {
		return
	}
	if r.streamCallID != "" && r.streamCallID != e.ToolCallID {
		return
	}
	r.streamCallID = e.ToolCallID

	// Only whole lines are printed, so each one can carry its prefix; the
	// remainder waits for the rest of the line rather than being shown without
	// one.
	text := r.streamHeld + e.Text
	lines := strings.Split(text, "\n")
	r.streamHeld = lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		// The gutter is dim; the line itself is not, so the command's own SGR
		// colours show at full strength. sgrOnly keeps those and drops the
		// escape classes (cursor motion, erase) that would corrupt the dock;
		// colorLongLine supplies the colours a piped `ls -l` never printed.
		fmt.Printf("%s  │ %s%s%s\n", Dim, Reset, colorLongLine(sgrOnly(line)), Reset)
		r.streamLines++
	}
	r.atLineHome = true
}

// endStream closes out a streamed call with a status line rather than its output,
// which the user has already watched go past.
func (r *Renderer) endStream(e agent.Event) {
	if r.streamHeld != "" {
		fmt.Printf("%s  │ %s%s%s\n", Dim, Reset, colorLongLine(sgrOnly(r.streamHeld)), Reset)
		r.streamLines++
	}
	colour := Dim
	if e.IsError {
		colour = Red
	}
	fmt.Printf("%s  %s%s\n", colour, streamStatus(e, r.streamLines), Reset)
	r.streamCallID, r.streamHeld, r.streamLines = "", "", 0
	r.atLineHome = true
}

// streamStatus describes how a streamed call ended. The output is gone from the
// renderer's hands by now, so this is the only place the exit code and the
// timing get said.
func streamStatus(e agent.Event, shown int) string {
	d, ok := e.ToolDetails.(tools.BashDetails)
	if !ok {
		if e.IsError {
			return "! " + Summarize(e.ToolOutput, 240)
		}
		return fmt.Sprintf("%d lines", shown)
	}
	parts := []string{fmt.Sprintf("exit %d", d.ExitCode)}
	if d.DurationMS >= 1000 {
		parts = append(parts, fmt.Sprintf("%.1fs", float64(d.DurationMS)/1000))
	} else {
		parts = append(parts, fmt.Sprintf("%dms", d.DurationMS))
	}
	if d.TotalLines > 0 {
		parts = append(parts, fmt.Sprintf("%d lines", d.TotalLines))
	}
	if d.TimedOut {
		parts = append(parts, "timed out")
	}
	// Worth saying explicitly: what scrolled past is not what the model received.
	if d.Truncated {
		parts = append(parts, "output truncated for the model")
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// maxDiffLines caps how much of a diff reaches the terminal. The full patch is
// in the tool details for interfaces that can scroll, unless it exceeded the
// details cap in tools/result.go.
const maxDiffLines = 24

// diffOf pulls the rendered diff and a "+3 -1" summary out of a tool's details,
// returning empty strings for tools that produce neither.
func diffOf(details any) (body, stat string) {
	switch d := details.(type) {
	case tools.EditDetails:
		return d.Diff, fmt.Sprintf("%s  +%d -%d", d.Path, d.Added, d.Removed)
	case tools.WriteDetails:
		if d.Diff == "" {
			return "", ""
		}
		return d.Diff, fmt.Sprintf("%s  +%d -%d", d.Path, d.Added, d.Removed)
	}
	return "", ""
}

// todosOf pulls the task list out of a tool's details, reporting false for every
// other tool. Keyed on the details type rather than the tool name for the same
// reason diffOf is: the type is the thing that guarantees the fields are there.
func todosOf(details any) ([]tools.TodoItem, bool) {
	d, ok := details.(tools.TodoDetails)
	if !ok {
		return nil, false
	}
	return d.Todos, true
}

// printTodos draws the checklist.
//
// Marks rather than status words, because the state of an item is glanceable and
// the text is not: five spelled-out statuses down the left margin push every task
// out to a ragged column and make the list harder to scan than the plain numbered
// form the model reads. The one in progress is the only line at full brightness —
// it is the answer to "what now", and everything else is context for it.
func printTodos(label string, todos []tools.TodoItem) {
	if len(todos) == 0 {
		fmt.Printf("%s  %stodo list cleared%s\n", Dim, label, Reset)
		return
	}
	done := 0
	for _, it := range todos {
		if it.Status == tools.TodoCompleted {
			done++
		}
	}
	fmt.Printf("%s  %s%d/%d done%s\n", Dim, label, done, len(todos), Reset)
	for _, it := range todos {
		mark, colour := "○", Dim
		switch it.Status {
		case tools.TodoCompleted:
			mark = "✓"
		case tools.TodoInProgress:
			mark, colour = "▸", ""
		case tools.TodoBlocked:
			mark, colour = "✗", Yellow
		case tools.TodoCancelled:
			// Struck through in spirit: kept visible because it records a decision,
			// dimmed because it is not work anyone is waiting on.
			mark = "–"
		}
		reset := Reset
		if colour == "" {
			reset = ""
		}
		fmt.Printf("  %s%s %s%s\n", colour, mark, oneLine(it.Task), reset)
	}
}

// printDiff colours a diff by line prefix: additions green, removals red,
// context dim.
func printDiff(body string, limit int) {
	lines := strings.Split(body, "\n")
	shown := min(len(lines), limit)
	for _, line := range lines[:shown] {
		colour := Dim
		if strings.HasPrefix(line, "+") {
			colour = Green
		} else if strings.HasPrefix(line, "-") {
			colour = Red
		}
		fmt.Printf("%s    %s%s\n", colour, line, Reset)
	}
	if len(lines) > shown {
		fmt.Printf("%s    … %d more diff line(s)%s\n", Dim, len(lines)-shown, Reset)
	}
}

// Summarize collapses a tool payload to one readable line.
func Summarize(s string, max int) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")
	first := lines[0]
	if len(first) > max {
		first = first[:max] + "…"
	}
	// Sanitize after the cut, because truncation can leave half an escape
	// sequence behind: sgrOnly drops the partial sequence along with cursor
	// motion and other escape classes that would corrupt the dock, while a
	// tool result's own SGR colours survive onto the summary line.
	first = sgrOnly(first)
	if len(lines) > 1 {
		return fmt.Sprintf("%s %s(+%d lines)%s", first, Dim, len(lines)-1, Reset)
	}
	return first
}

// lsSummary renders an ls result on one width-bounded line: directories in
// blue, files dimmed. The tool output is one entry per line, so the generic
// Summarize would show a single name and "(+24 lines)" — a listing deserves
// its names. Width is counted in cells so CJK names stay honest.
func lsSummary(out string, maxCells int) string {
	entries := make([]string, 0, 32)
	for _, ln := range strings.Split(out, "\n") {
		// The trailing "[N entries limit reached …]" note is metadata, not an
		// entry; the truncation hint already follows from "… (+N)".
		if ln == "" || strings.HasPrefix(ln, "[") {
			continue
		}
		entries = append(entries, ln)
	}
	var b strings.Builder
	cells := 0
	for i, name := range entries {
		w := cellsWidth([]rune(name))
		if i > 0 && cells+1+w > maxCells {
			fmt.Fprintf(&b, " %s… (+%d)%s", Dim, len(entries)-i, Reset)
			break
		}
		if i > 0 {
			b.WriteByte(' ')
			cells++
		}
		if strings.HasSuffix(name, "/") {
			b.WriteString(DirBlue + name + Reset)
		} else {
			b.WriteString(Dim + name + Reset)
		}
		cells += w
	}
	return b.String()
}
