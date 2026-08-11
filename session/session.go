// Package session persists a conversation as append-only JSONL.
//
// Records form a tree via ParentID rather than a flat list. A linear chat is
// just a tree with no branches, but keeping the parent link from day one is what
// makes "rewind to an earlier point and try again" a read of a different leaf
// instead of a rewrite of the file. A fork is itself a record (type "fork"),
// so a reload replays the branch the fork chose rather than whatever happens
// to be the file's last line.
package session

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yosukeno/pi-go/llm"
)

type Record struct {
	ID       string       `json:"id"`
	ParentID string       `json:"parentId,omitempty"`
	Type     string       `json:"type"` // "meta" | "message" | "stats" | "fork"
	Time     int64        `json:"time"`
	Message  *llm.Message `json:"message,omitempty"`
	Meta     *Meta        `json:"meta,omitempty"`
	Stats    *Stats       `json:"stats,omitempty"`
}

type Meta struct {
	Cwd   string `json:"cwd"`
	Model string `json:"model"`
	// Skills are the skill names in effect when the session was created.
	//
	// Recorded because they change behaviour without appearing anywhere in the
	// messages: the same transcript replayed with a different skill directory is a
	// different run, and without this there is no way to tell after the fact.
	Skills []string `json:"skills,omitempty"`
	// Pinned and Title are sidebar edits. They arrive in a meta record appended
	// long after creation rather than by rewriting the first one — the file is
	// append-only — so they are pointers: "this record says nothing about it"
	// must stay distinct from "set to the zero value", and a reader merges by
	// taking the last non-nil.
	Pinned *bool   `json:"pinned,omitempty"`
	Title  *string `json:"title,omitempty"`
}

// Stats records per-turn performance metrics.
type Stats struct {
	// Token usage for this turn
	Usage *UsageStats `json:"usage,omitempty"`
	// Delegated is the part of Usage that subagents spent, absent when nothing was
	// delegated. A subset of Usage, never a sibling: adding the two would count the
	// same tokens twice, which is exactly the mistake this field exists to make
	// unnecessary — before it, the only way to attribute a delegation's cost was to
	// find each child's transcript and analyse it separately.
	Delegated *UsageStats `json:"delegated,omitempty"`
	// Tool execution statistics for this turn
	Tools *ToolStats `json:"tools,omitempty"`
	// Retry information for this turn
	Retry *RetryStats `json:"retry,omitempty"`
	// Approval gate information for this turn
	Approval *ApprovalStats `json:"approval,omitempty"`
	// Composition estimates what the prompt was made of when this record was
	// written. Unlike every other field here it is a snapshot rather than a delta,
	// so the newest one is the answer and summing them means nothing — see
	// Composition's own doc comment, which says so at length because the
	// surrounding convention is the opposite.
	Composition *Composition `json:"composition,omitempty"`
}

// UsageStats records token usage for one turn.
type UsageStats struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	CacheRead int64 `json:"cache_read,omitempty"`
	Reasoning int64 `json:"reasoning,omitempty"`
}

// ToolStats records tool execution metrics for one turn.
type ToolStats struct {
	CallCount int            `json:"call_count"`          // Number of tool calls in this turn
	Duration  int64          `json:"duration"`            // Total duration in milliseconds
	ExecTime  []ToolExecTime `json:"exec_time,omitempty"` // Individual tool execution times
}

// ToolExecTime records execution time for a single tool call.
type ToolExecTime struct {
	Name     string `json:"name"`
	Duration int64  `json:"duration"` // Duration in milliseconds
	IsError  bool   `json:"is_error,omitempty"`
}

// RetryStats records retry information for one turn.
type RetryStats struct {
	Count  int    `json:"count"`            // Number of retries
	Reason string `json:"reason,omitempty"` // Last retry reason
}

// ApprovalStats records approval gate metrics for one turn.
type ApprovalStats struct {
	WaitTime  int64 `json:"wait_time"`  // Total wait time in milliseconds
	AskCount  int   `json:"ask_count"`  // Number of approval requests
	DenyCount int   `json:"deny_count"` // Number of denied requests
}

// Store appends records to one JSONL file.
type Store struct {
	path string
	// head is the id of the last written record, i.e. the current leaf.
	head    string
	records []Record
	meta    *Meta
	// parent overrides a record's own ParentID. It holds an entry only where
	// damage broke the chain and Open stitched it back together; see repairChain.
	parent map[string]string
	diags  []string
}

// Create starts a new session file under dir. skills is recorded as metadata; see
// Meta.Skills for why.
func Create(dir, cwd, model string, skills ...string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("%s-%s.jsonl", time.Now().UTC().Format("20060102T150405Z"), randomID(4))
	s := &Store{path: filepath.Join(dir, name)}
	return s, s.append(Record{Type: "meta", Meta: &Meta{Cwd: cwd, Model: model, Skills: skills}})
}

// Open loads an existing session file.
//
// An unreadable line never fails the load — the transcript is usually the only
// copy, so recovering what is there beats refusing to start. But it is not
// ignored either: see Diagnostics, and repairChain for how the surviving records
// are kept reachable.
func Open(path string) (*Store, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	s := &Store{path: path}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	// Unreadable lines are collected by line number rather than acted on where
	// they are found, because what one means depends on whether anything follows
	// it: the last line is a half-written record, an earlier one is damage.
	var bad []int
	lineNo, lastNonEmpty := 0, 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		lastNonEmpty = lineNo
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			bad = append(bad, lineNo)
			continue
		}
		if r.ID == "" {
			// Without an id the record cannot be linked to, so it is damage of
			// the same kind even though it parsed.
			bad = append(bad, lineNo)
			continue
		}
		s.records = append(s.records, r)
		s.head = r.ID
		if r.Type == "meta" && r.Meta != nil && s.meta == nil {
			s.meta = r.Meta
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	if n := len(bad); n > 0 && bad[n-1] == lastNonEmpty {
		// A half-written final line is the ordinary result of the process being
		// killed mid-append. Nothing follows it, so nothing is lost but the record
		// that never finished.
		bad = bad[:n-1]
	}
	if len(bad) > 0 {
		s.diags = append(s.diags, fmt.Sprintf(
			"%d unreadable record(s) at line %s: this file is damaged, not just truncated",
			len(bad), joinInts(bad)))
	}
	s.repairChain()
	return s, nil
}

// repairChain reconnects records whose parent did not survive.
//
// This is what stands between a single damaged line and losing every message
// before it. Replay walks up ParentID from the leaf, so one missing link used to
// end the walk there and silently drop the whole earlier history — the records
// were still on disk, just unreachable.
//
// The repair is only sound where file order is chain order, which is to say in a
// file with no branches. A branched file gets a diagnostic instead: guessing
// which branch a record belonged to could splice two conversations together, and
// a wrong transcript is worse than a short one.
func (s *Store) repairChain() {
	present := make(map[string]bool, len(s.records))
	children := make(map[string]int, len(s.records))
	for _, r := range s.records {
		present[r.ID] = true
	}
	for _, r := range s.records {
		if r.ParentID != "" {
			children[r.ParentID]++
		}
	}
	branched := false
	for _, n := range children {
		if n > 1 {
			branched = true
			break
		}
	}

	var broken, repaired int
	for i, r := range s.records {
		if r.ParentID == "" || present[r.ParentID] {
			continue
		}
		broken++
		if branched {
			continue
		}
		if s.parent == nil {
			s.parent = make(map[string]string)
		}
		// The previous surviving record is where this one rejoins. When there is
		// no previous record the lost parent was the root, so this record becomes
		// the root rather than a dead end.
		if i > 0 {
			s.parent[r.ID] = s.records[i-1].ID
			repaired++
		} else {
			s.parent[r.ID] = ""
		}
	}

	switch {
	case broken == 0:
	case branched:
		s.diags = append(s.diags, fmt.Sprintf(
			"%d record(s) lost their parent and this file has branches, so history before the damage is not replayed",
			broken))
	case repaired > 0:
		s.diags = append(s.diags, fmt.Sprintf(
			"reconnected %d broken link(s) using file order to keep the earlier history reachable", repaired))
	}
}

// Diagnostics reports damage found while loading. Empty for a healthy file.
//
// These are surfaced rather than returned as an error because none of them stops
// the session from being used, and a user who is told "4 records were
// unreadable" can act on it, while one who is told nothing cannot.
func (s *Store) Diagnostics() []string { return s.diags }

// Meta is the metadata recorded when the session was created. Nil for a file
// with no readable meta record.
func (s *Store) Meta() *Meta { return s.meta }

func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = fmt.Sprint(n)
	}
	return strings.Join(parts, ", ")
}

// Latest returns the most recently modified session file in dir.
func Latest(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no sessions in %s", dir)
	}
	// Filenames are UTC timestamps, so lexical order is chronological.
	sort.Strings(names)
	return filepath.Join(dir, names[len(names)-1]), nil
}

func (s *Store) Path() string { return s.path }

// Head is the current leaf id, usable as a fork point.
func (s *Store) Head() string { return s.head }

// Append writes a message as a child of the current head.
func (s *Store) Append(m llm.Message) error {
	return s.append(Record{Type: "message", Message: &m})
}

// AppendStats records metrics for the turns just written.
//
// This existed as a type and a parser long before it existed as a writer: the Stats
// struct here, the matching record in package analyze, and the whole "Token Usage"
// section of `-analyze-session` were all complete, and nothing ever produced one. So
// every session reported costing zero tokens — which reads as a fact rather than as
// the absence of one, and made a subagent's transcript unable to answer the only
// question anyone asks of it afterwards, which is what the delegation cost.
//
// What a transcript records is the *whole* cost of that run, delegation included.
// A parent's counters already have its subagents' tokens folded in — that is what
// makes -token-budget cover delegated work — so a parent's transcript answers "what
// did this session cost, all in", and each subagent's own transcript is the
// breakdown. Measured on one live run: parent 14635 in, child 9291 of them.
//
// So these numbers must not be added across transcripts. Stated here because the
// arithmetic looks additive and is not, and because a cost report that silently
// counts the same tokens twice is worse than no report at all. The link between the
// two is SubagentDetails.Session.
func (s *Store) AppendStats(st Stats) error {
	return s.append(Record{Type: "stats", Stats: &st})
}

// AppendMeta records sidebar edits (pin state, custom title) as an appended meta
// record. The creation record is never rewritten: the file stays append-only,
// and a reader merges by taking the last non-nil value — see Meta.Pinned.
func (s *Store) AppendMeta(m *Meta) error {
	return s.append(Record{Type: "meta", Meta: m})
}

// AppendAll writes messages in order.
func (s *Store) AppendAll(ms []llm.Message) error {
	for _, m := range ms {
		if err := s.Append(m); err != nil {
			return err
		}
	}
	return nil
}

// Timed pairs a message with when its record was written, for UIs that show
// send times.
type Timed struct {
	Message llm.Message
	Time    int64
}

// Messages replays the branch ending at leaf. An empty leaf means the head.
func (s *Store) Messages(leaf string) []llm.Message {
	timed := s.TimedMessages(leaf)
	out := make([]llm.Message, 0, len(timed))
	for _, t := range timed {
		out = append(out, t.Message)
	}
	return out
}

// TimedMessages is Messages with each record's timestamp attached.
func (s *Store) TimedMessages(leaf string) []Timed {
	var out []Timed
	for _, r := range s.chain(leaf) {
		if r.Type == "message" && r.Message != nil {
			out = append(out, Timed{Message: *r.Message, Time: r.Time})
		}
	}
	return out
}

// UsageTotals sums every usage delta in the file — the session's all-in cost,
// delegation included (see AppendStats for why nothing is added across
// transcripts). A reopened session seeds its counters from this, so a restart does
// not zero the running total.
//
// Every record, deliberately, and not just the ones on the live branch. This used
// to walk the current chain, which meant a rewind or a compaction made the
// abandoned branch's spending disappear: Fork("") drops everything reachable, so a
// compacted session reopened from disk reported having cost nothing. The provider
// charged for those tokens whichever branch is live now, and two mechanisms depend
// on that being true — -token-budget and -cost-budget are ceilings, and a ceiling
// that forgets what was spent every time the conversation is reorganised is not one.
//
// It also puts this in agreement with package analyze, which has always read every
// record linearly. Two things that count one session's cost should not disagree
// about it.
func (s *Store) UsageTotals() (usage, delegated llm.Usage) {
	for _, r := range s.records {
		if r.Type != "stats" || r.Stats == nil {
			continue
		}
		if u := r.Stats.Usage; u != nil {
			usage.Input += u.Input
			usage.Output += u.Output
			usage.CacheRead += u.CacheRead
			usage.Reasoning += u.Reasoning
		}
		if d := r.Stats.Delegated; d != nil {
			delegated.Input += d.Input
			delegated.Output += d.Output
			delegated.CacheRead += d.CacheRead
			delegated.Reasoning += d.Reasoning
		}
	}
	return usage, delegated
}

// chain walks the branch ending at leaf (an empty leaf means the head),
// oldest record first.
func (s *Store) chain(leaf string) []Record {
	if leaf == "" {
		leaf = s.head
	}
	byID := make(map[string]Record, len(s.records))
	for _, r := range s.records {
		byID[r.ID] = r
	}

	var chain []Record
	// Links always point backwards in file order, so a cycle cannot arise from
	// anything this package writes. The guard is for a hand-edited file: reading
	// one should not hang.
	seen := make(map[string]bool, len(s.records))
	for id := leaf; id != "" && !seen[id]; {
		seen[id] = true
		r, ok := byID[id]
		if !ok {
			break
		}
		chain = append(chain, r)
		if p, ok := s.parent[r.ID]; ok {
			id = p // a link Open repaired
		} else {
			id = r.ParentID
		}
	}
	// Walking up yields newest first.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// Fork points subsequent appends at an earlier record, branching the tree.
// The fork is itself recorded — a marker hanging off the fork point — because
// the head is otherwise just "the last line of the file": without the marker,
// a reload between the fork and the next append would replay the abandoned
// branch as if the rewind had never happened. An empty recordID points at the
// root: everything currently reachable is abandoned, and the marker starts a
// fresh chain.
func (s *Store) Fork(recordID string) error {
	if recordID != "" {
		found := false
		for _, r := range s.records {
			if r.ID == recordID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("record %s not found in %s", recordID, s.path)
		}
	}
	s.head = recordID
	return s.append(Record{Type: "fork"})
}

// RewindPoint finds the fork point that removes the kth visible user message
// (1-based) and everything that follows it. Visible means the message carries
// something other than tool results — the same filter the web Hub's Seed
// applies when it decides which records reach the timeline, which is what
// makes the ordinal the UI counts match the records counted here. The
// returned id is the record to Fork to: the target's parent (repaired link
// if Open stitched one), possibly "" when the target hangs off the root.
func (s *Store) RewindPoint(k int) (string, bool) {
	if k < 1 {
		return "", false
	}
	n := 0
	for _, r := range s.chain("") {
		if r.Type != "message" || r.Message == nil || r.Message.Role != llm.RoleUser {
			continue
		}
		if !visible(r.Message) {
			continue
		}
		n++
		if n == k {
			if p, ok := s.parent[r.ID]; ok {
				return p, true
			}
			return r.ParentID, true
		}
	}
	return "", false
}

// visible reports whether a message carries anything other than tool results.
// A tool-result-only user message never appears on a timeline, so it must not
// count toward a rewind ordinal either.
func visible(m *llm.Message) bool {
	for _, b := range m.Content {
		if b.Type != llm.BlockToolResult {
			return true
		}
	}
	return false
}

func (s *Store) append(r Record) error {
	r.ID = randomID(8)
	r.ParentID = s.head
	r.Time = time.Now().UnixMilli()

	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	// O_RDWR rather than O_WRONLY so the last byte can be checked; O_APPEND still
	// forces every write to the end.
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	err = writeLine(f, line)
	// Close can be where a deferred write finally fails, so its error counts too.
	// Reporting success for a record that never landed is how a transcript and
	// the agent's memory drift apart.
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	s.records = append(s.records, r)
	s.head = r.ID
	// The first meta record is the creation record; same rule as Open's parse,
	// so a just-created Store answers Meta() like a reloaded one.
	if r.Type == "meta" && r.Meta != nil && s.meta == nil {
		s.meta = r.Meta
	}
	return nil
}

// writeLine appends one record, first closing off any unterminated line.
//
// A file whose previous write was cut short has no closing newline. Appending
// straight onto it fuses the leftover fragment and this record into a single
// unparseable line, which destroys a good record on top of the damaged one — and
// that second loss is what breaks the parent chain and takes the earlier history
// with it.
func writeLine(f *os.File, line []byte) error {
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, st.Size()-1); err != nil {
			return err
		}
		if last[0] != '\n' {
			if _, err := f.Write([]byte{'\n'}); err != nil {
				return err
			}
		}
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

// DefaultDir is where sessions live when no path is given.
func DefaultDir() string {
	if d := os.Getenv("PIGO_SESSION_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pi-go", "sessions")
}

func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// Info summarises a session file without loading its whole history. The Web UI's
// session sidebar is built from these.
type Info struct {
	Path string `json:"path"`
	ID   string `json:"id"`
	// Title is the first user message, trimmed, or the custom name a rename
	// recorded. Empty for a session that was created but never used.
	Title string `json:"title"`
	// Pinned sessions sort above the rest in List. Set by a later meta record;
	// see Meta.Pinned.
	Pinned bool   `json:"pinned,omitempty"`
	Cwd    string `json:"cwd,omitempty"`
	Model  string `json:"model,omitempty"`
	// Skills were in effect when the session was created. Exposed because they
	// change how the session behaves without appearing in any message, so a
	// listing that omits them cannot explain why two sessions differ.
	Skills   []string `json:"skills,omitempty"`
	Messages int      `json:"messages"`
	Created  int64    `json:"created,omitempty"`
	Updated  int64    `json:"updated,omitempty"`
}

const titleMaxRunes = 80

// List summarises every session in dir, newest first.
//
// It streams each file rather than calling Open, because the sidebar needs a
// title and a count, not the transcript. A corrupt or unreadable file is skipped
// rather than failing the whole listing: one bad session should not hide the
// others.
func List(dir string) ([]Info, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var out []Info
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := summarise(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, info)
	}
	// Filenames start with a UTC timestamp, so lexical order is chronological.
	// Pinned sessions come first; the rest stay newest-first.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		return out[i].Path > out[j].Path
	})
	return out, nil
}

func summarise(path string) (Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return Info{}, err
	}
	defer f.Close()

	info := Info{
		Path: path,
		ID:   strings.TrimSuffix(filepath.Base(path), ".jsonl"),
	}
	if st, err := f.Stat(); err == nil {
		info.Updated = st.ModTime().UnixMilli()
	}

	// Records are indexed so the summary can walk the live branch back from
	// its tip rather than counting every line in the file: a rewind abandons
	// the records after the fork without deleting them, and counting those
	// would report turns the session no longer has.
	type node struct {
		parent string
		rec    Record
	}
	nodes := make(map[string]node)
	last := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Record
		if json.Unmarshal([]byte(line), &r) != nil || r.ID == "" {
			continue
		}
		nodes[r.ID] = node{parent: r.ParentID, rec: r}
		last = r.ID
	}
	if err := sc.Err(); err != nil {
		return info, err
	}

	// Walking newest→oldest: for merged fields (pin, rename) the FIRST value
	// met wins, matching the last-non-nil merge of a forward read; for the
	// derived title and the creation meta the LAST one standing wins, since
	// the oldest record on the branch owns them.
	customTitle := ""
	derivedTitle := ""
	pinnedSet := false
	for id := last; id != ""; {
		n, ok := nodes[id]
		if !ok {
			// A missing parent ends the walk: history before the damage is
			// unreachable, which is exactly what Open replays too.
			break
		}
		r := n.rec
		id = n.parent
		switch {
		case r.Type == "meta" && r.Meta != nil:
			// Overwritten all the way up, so the oldest meta on the branch —
			// the creation record — is what remains.
			info.Cwd, info.Model, info.Created = r.Meta.Cwd, r.Meta.Model, r.Time
			info.Skills = r.Meta.Skills
			if r.Meta.Pinned != nil && !pinnedSet {
				info.Pinned, pinnedSet = *r.Meta.Pinned, true
			}
			if r.Meta.Title != nil && customTitle == "" {
				customTitle = *r.Meta.Title
			}
		case r.Type == "message" && r.Message != nil:
			info.Messages++
			if text := r.Message.Text(); r.Message.Role == llm.RoleUser && text != "" {
				derivedTitle = text
			}
		}
	}
	// An empty custom title means the rename was reverted; the derived one
	// comes back.
	if customTitle != "" {
		info.Title = customTitle
	} else if derivedTitle != "" {
		info.Title = title(derivedTitle)
	}
	return info, nil
}

// title collapses a prompt to a single readable line.
func title(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > titleMaxRunes {
		return string(r[:titleMaxRunes]) + "…"
	}
	return s
}

// emptyGrace is how long a message-less session must have sat before
// CleanEmpty may remove it. A younger file may belong to a process that just
// created it — terminal and web modes share DefaultDir — and deleting it
// would cost that session its meta record when the first message lands.
const emptyGrace = time.Minute

// CleanEmpty removes session files that hold no messages and reports how many
// were removed. The web UI writes the file when the session is created, not
// when the first prompt is sent, so every abandoned "new session" click
// accumulates in the sidebar as an unnamed empty row; sweeping them at
// startup keeps the listing to sessions that actually happened.
//
// Removal is deliberately conservative, because the file is the only copy:
// a pinned session stays (someone marked it on purpose), a file with an
// unreadable line stays (damage cannot prove emptiness), and a file younger
// than emptyGrace stays (its first message may be on its way).
func CleanEmpty(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		empty, pinned, err := provablyEmpty(path)
		if err != nil || !empty || pinned {
			continue
		}
		if info, err := e.Info(); err != nil || time.Since(info.ModTime()) < emptyGrace {
			continue
		}
		if err := os.Remove(path); err == nil {
			removed++
		}
	}
	return removed, nil
}

// provablyEmpty reports whether a session file parses cleanly and holds no
// message records, plus whether it is pinned — the two reasons CleanEmpty
// spares an otherwise empty file.
func provablyEmpty(path string) (empty, pinned bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, false, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Record
		if json.Unmarshal([]byte(line), &r) != nil {
			// An unreadable line may be a message; damage is not emptiness.
			return false, false, nil
		}
		switch {
		case r.Type == "message" && r.Message != nil:
			return false, false, nil
		case r.Type == "meta" && r.Meta != nil && r.Meta.Pinned != nil:
			pinned = *r.Meta.Pinned
		}
	}
	if sc.Err() != nil {
		return false, false, nil
	}
	return true, pinned, nil
}

// Recorded is how much of an agent's running totals is already on disk. Callers keep
// one and hand it to UsageDelta, which advances it.
//
// A struct rather than two loose variables because the two have to move together: a
// caller that advanced one and forgot the other would silently attribute the next
// turn's own spending to delegation, or the reverse.
type Recorded struct {
	Usage     llm.Usage
	Delegated llm.Usage
}

// UsageDelta is the usage accrued since the last recorded point, advancing that
// point as a side effect. It reports false when nothing has accrued, so a flush
// that wrote only user input does not leave a row of zeroes in the transcript.
//
// Shared by the terminal and the web server because both persist the same way — in
// bulk, when a turn settles — and a second copy of "subtract the last total" is a
// second place for the sign to be wrong.
func UsageDelta(rec *Recorded, total, delegated llm.Usage) (Stats, bool) {
	d := diff(total, rec.Usage)
	dd := diff(delegated, rec.Delegated)
	rec.Usage, rec.Delegated = total, delegated
	if zero(d) {
		return Stats{}, false
	}
	st := Stats{Usage: usageOf(d)}
	// Omitted rather than written as zeroes when nothing was delegated, which is most
	// turns: a field that is always present says nothing by being present.
	if !zero(dd) {
		st.Delegated = usageOf(dd)
	}
	return st, true
}

func diff(now, then llm.Usage) llm.Usage {
	return llm.Usage{
		Input:     now.Input - then.Input,
		Output:    now.Output - then.Output,
		CacheRead: now.CacheRead - then.CacheRead,
		Reasoning: now.Reasoning - then.Reasoning,
	}
}

func zero(u llm.Usage) bool {
	return u.Input == 0 && u.Output == 0 && u.CacheRead == 0 && u.Reasoning == 0
}

func usageOf(u llm.Usage) *UsageStats {
	return &UsageStats{
		Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, Reasoning: u.Reasoning,
	}
}
