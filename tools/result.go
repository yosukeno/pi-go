package tools

// Result is what a tool returns.
//
// The split is the point: Text goes to the model, Details goes to the UI and
// nowhere else. A diff is the clearest example — showing it to the model wastes
// tokens on content it just wrote, while showing it to a human is the whole
// reason a coding agent is watchable.
type Result struct {
	// Text is the tool output that enters the conversation. It is subject to the
	// truncation limits in truncate.go.
	Text string
	// Details is structured data for interfaces to render. It never enters the
	// LLM context and is therefore not truncated.
	Details any
}

// ReadDetails accompanies the read tool.
type ReadDetails struct {
	Path        string `json:"path"`
	Offset      int    `json:"offset,omitempty"`
	Limit       int    `json:"limit,omitempty"`
	TotalLines  int    `json:"total_lines"`
	ShownLines  int    `json:"shown_lines"`
	FirstLine   int    `json:"first_line"`
	Truncated   bool   `json:"truncated,omitempty"`
	TruncatedBy string `json:"truncated_by,omitempty"`
}

// ReadManyDetails accompanies a read of several files in one call.
//
// A separate type rather than a repeated ReadDetails, and without a top-level
// total_lines, because the web UI discriminates read results on exactly that field
// (isReadDetails in timeline.ts) and its ReadResult component renders one file: one
// path, one line count, one continue-from-here button. Shaped this way, a multi-file
// read falls through to the plain-text fallback instead of feeding a single-file
// component something it cannot draw. The structure is still on the transcript for
// the analyzer, and for whoever writes the component.
type ReadManyDetails struct {
	Files []ReadFileDetails `json:"files"`
}

// ReadFileDetails is one file inside a multi-file read. Error is set instead of the
// counts when that file could not be read, which does not fail the call — see
// Read.readMany.
type ReadFileDetails struct {
	Path        string `json:"path"`
	TotalLines  int    `json:"total_lines,omitempty"`
	ShownLines  int    `json:"shown_lines,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	TruncatedBy string `json:"truncated_by,omitempty"`
	Error       string `json:"error,omitempty"`

	// BodyOffset and BodyLength locate this file's content inside Result.Text, as
	// a byte range excluding the `==> path <==` header and the truncation note.
	//
	// They exist so an interface can show each file separately without parsing the
	// concatenated text back apart. Parsing looked cheap — the separator is a fixed
	// format this package writes itself — and it is not: a file whose own contents
	// contain a line reading `==> other.go <==` splits the section it belongs to,
	// and the result is a body attributed to the wrong file, which is the one
	// failure mode a viewer must not have. Offsets are computed by the same code
	// that writes the text, so they cannot drift from the format the way a reader's
	// idea of it can.
	//
	// The body is sent once, not twice: Text has to carry it regardless, since that
	// is the half the model reads, so duplicating it into Details would double a
	// payload that is already the largest thing on the wire.
	//
	// Zero means there is no body — an unreadable path. A real body can never start
	// at zero, because its header precedes it.
	BodyOffset int `json:"body_offset,omitempty"`
	BodyLength int `json:"body_length,omitempty"`
}

// LsDetails accompanies the ls tool. The counts are what the UI shows in place
// of re-parsing the listing text.
type LsDetails struct {
	Path         string `json:"path"`
	Entries      int    `json:"entries"`
	Dirs         int    `json:"dirs,omitempty"`
	Files        int    `json:"files,omitempty"`
	Total        int    `json:"total,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
	EntryLimited bool   `json:"entry_limited,omitempty"`
}

// FindDetails accompanies the find tool. Scanned is reported because "no
// matches" after 3 entries and after 200,000 mean different things.
type FindDetails struct {
	Pattern   string `json:"pattern"`
	Path      string `json:"path,omitempty"`
	Matches   int    `json:"matches"`
	Scanned   int    `json:"scanned"`
	Truncated bool   `json:"truncated,omitempty"`
	LimitHit  bool   `json:"limit_hit,omitempty"`
}

// GrepDetails accompanies the grep tool.
type GrepDetails struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
	Include string `json:"include,omitempty"`
	Matches int    `json:"matches"`
	// Files is how many distinct files contained a match.
	Files         int  `json:"files"`
	Scanned       int  `json:"scanned"`
	SkippedBinary int  `json:"skipped_binary,omitempty"`
	Truncated     bool `json:"truncated,omitempty"`
	LimitHit      bool `json:"limit_hit,omitempty"`
}

// maxDetailsDiff caps the combined Display and Unified diff bodies carried in
// a tool's Details. A full-file rewrite of a large file would otherwise put
// ~2x the file size into every SSE subscriber's tool_end event and leave the
// browser to JSON.parse it on the main thread. The 1 MiB figure follows the
// journalMaxBase precedent in journal.go: past the cap the Added/Removed stats
// still describe the change, and the UI degrades to showing just those (an
// absent diff already renders as stats-only). Like the journal caps it is a
// var so tests can shrink it.
var maxDetailsDiff = 1 << 20

// capDetailsDiff enforces maxDetailsDiff on a (display, unified) diff pair.
// Over the cap both bodies are dropped and tooBig reports true; the stats are
// computed separately and are unaffected.
func capDetailsDiff(display, unified string) (string, string, bool) {
	if len(display)+len(unified) <= maxDetailsDiff {
		return display, unified, false
	}
	return "", "", true
}

// WriteDetails accompanies the write tool. Diff and Patch are only populated
// when an existing file was overwritten; creating a file has nothing to diff
// against, but its Added/Removed stats still count every line as an addition.
// TooBig reports that Diff and Patch were dropped for exceeding maxDetailsDiff
// (same "too_big" convention as the workspace-diff endpoint in web/workspace.go).
type WriteDetails struct {
	Path    string `json:"path"`
	Bytes   int    `json:"bytes"`
	Created bool   `json:"created"`
	Diff    string `json:"diff,omitempty"`
	Patch   string `json:"patch,omitempty"`
	Added   int    `json:"added,omitempty"`
	Removed int    `json:"removed,omitempty"`
	TooBig  bool   `json:"too_big,omitempty"`
}

// EditDetails accompanies the edit tool. This is the payload the Web UI's diff
// view is built around. TooBig has the same meaning as on WriteDetails.
type EditDetails struct {
	Path             string `json:"path"`
	Edits            int    `json:"edits"`
	Diff             string `json:"diff"`
	Patch            string `json:"patch"`
	FirstChangedLine int    `json:"first_changed_line,omitempty"`
	Added            int    `json:"added"`
	Removed          int    `json:"removed"`
	TooBig           bool   `json:"too_big,omitempty"`
}

// TodoDetails accompanies the todo tool: the list as structure, so an interface
// can draw a checklist instead of re-parsing the text the model reads.
//
// No counts, unlike LsDetails. The counts there describe something the text does
// not contain (how many entries existed before truncation); here every item is in
// the list, so a precomputed total would only be a second thing that can disagree
// with it.
type TodoDetails struct {
	Todos []TodoItem `json:"todos"`
}

// BashDetails accompanies the bash tool. It is populated even when the command
// fails, so the UI can show the exit code alongside the output.
type BashDetails struct {
	Command string `json:"command"`
	// Workdir is where the command ran, and only when that was not the session's
	// own directory. Recorded because a command that ran somewhere else and a card
	// that does not say so is the same class of confusion the tool's description
	// exists to prevent: a plausible wrong answer rather than an error.
	Workdir        string `json:"workdir,omitempty"`
	ExitCode       int    `json:"exit_code"`
	DurationMS     int64  `json:"duration_ms"`
	TimedOut       bool   `json:"timed_out,omitempty"`
	Truncated      bool   `json:"truncated,omitempty"`
	TotalLines     int    `json:"total_lines,omitempty"`
	FullOutputPath string `json:"full_output_path,omitempty"`
}
