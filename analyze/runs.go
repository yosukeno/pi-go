package analyze

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Run is one Run() call reconstructed from a transcript.
//
// This is the unit -max-turns actually bounds, and it is not the unit
// SessionStats reports. RoundCount there counts every assistant message in the
// file: several runs summed together, plus the ones on branches a rewind
// abandoned. Picking a turn cap from that number would overshoot twice over, in
// the same direction both times, which is why this walks the history again
// rather than reusing it.
type Run struct {
	Session string `json:"session"`
	// Index is 1-based within its session, so a run can be found again.
	Index int `json:"index"`
	// Turns counts assistant messages, matching what the loop counts.
	Turns int `json:"turns"`
	Tools int `json:"tool_calls"`
	// Finished reports that the run reached an answer: its last assistant message
	// asked for no tools. A false here is a *lower bound* on the turns the work
	// needed, never an observation of it — see RunDistribution.
	Finished bool `json:"finished"`
	// Steered marks a run that received a message mid-flight. Such a run's
	// boundaries are the least certain ones in the set; see runsFrom.
	Steered  bool  `json:"steered,omitempty"`
	Duration int64 `json:"duration_ms,omitempty"`
}

// RunDistribution summarises turn counts over a set of runs.
//
// Only finished runs enter the percentiles. An unfinished run is right-censored
// data: the work needed *at least* that many turns and we do not know how many
// more, so counting it as an observation of "this task took N turns" biases every
// percentile downward — and it biases it hardest in the tail, which is the part a
// cap is chosen from. The censored runs are still reported, because how many there
// are is what decides whether the percentiles can be trusted at all.
type RunDistribution struct {
	Sessions int `json:"sessions"`
	// Unreadable files, reported rather than dropped: a percentile over an
	// unknown fraction of the data is not a measurement.
	Unreadable int `json:"unreadable,omitempty"`
	Runs       int `json:"runs"`
	Finished   int `json:"finished"`
	Unfinished int `json:"unfinished"`
	Steered    int `json:"steered,omitempty"`
	// Trivial counts finished runs that called no tool at all. Excluded from the
	// percentiles: such a run ends on turn 1 by construction, so it cannot tell you
	// anything about where a turn cap should sit, and there are enough of them to
	// drag the whole distribution down. On this project's own 102 transcripts,
	// leaving them in moved the p75 from 5 to 4 and the p90 from 9 to 6 — a cap set
	// from the mixed number would have been chosen from how much plain Q&A the
	// history contains.
	Trivial int `json:"trivial,omitempty"`
	// Population is how many runs the percentiles were computed over: finished, and
	// having called at least one tool.
	Population int `json:"population"`

	// Percentiles over Population, by nearest rank. Empty when it is zero.
	P50 int `json:"p50,omitempty"`
	P75 int `json:"p75,omitempty"`
	P90 int `json:"p90,omitempty"`
	P95 int `json:"p95,omitempty"`
	Max int `json:"max,omitempty"`
	// Mean is carried alongside P50 because the gap between them is the shape of
	// the tail, and the tail is the whole question here.
	Mean float64 `json:"mean,omitempty"`

	// Histogram buckets finished runs by turn count.
	Histogram map[int]int `json:"histogram,omitempty"`

	// Censored holds the turn count of every unfinished run, ascending.
	//
	// The list rather than a summary statistic, because unfinished runs are few and
	// they are not one population: a run cut off by the turn cap piles up on the cap,
	// while one ended by Ctrl-C or a dead connection stops wherever it happened to
	// be. A mode over a handful of those reports a spike that is not there — which
	// this field printed until the numbers were looked at.
	Censored []int `json:"censored,omitempty"`
	// CensoredMode is the most common value in Censored and how many runs share it.
	// Only meaningful as evidence of a cap when the count is most of the list;
	// FormatRunsText will not name it otherwise.
	CensoredMode  int `json:"censored_mode,omitempty"`
	CensoredCount int `json:"censored_count,omitempty"`
}

// RunReport is a distribution plus the runs behind it.
type RunReport struct {
	Dir          string          `json:"dir"`
	Distribution RunDistribution `json:"distribution"`
	Runs         []Run           `json:"runs,omitempty"`
}

// AnalyzeRuns reconstructs every run in every session under dir.
//
// An unreadable session is counted, not fatal, matching session.List: one damaged
// file should not hide the rest. Unlike List it does not sort by recency, because
// the output is an aggregate and the order of the inputs does not reach it.
func AnalyzeRuns(dir string, config Config) (*RunReport, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read session directory: %w", err)
	}

	report := &RunReport{Dir: dir}
	var turns []int
	dist := RunDistribution{Histogram: map[int]int{}}
	censored := map[int]int{}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		runs, err := Runs(filepath.Join(dir, e.Name()))
		if err != nil {
			dist.Unreadable++
			continue
		}
		dist.Sessions++
		for _, r := range runs {
			dist.Runs++
			if r.Steered {
				dist.Steered++
			}
			if r.Finished {
				dist.Finished++
				if r.Tools == 0 {
					dist.Trivial++
				} else {
					turns = append(turns, r.Turns)
					dist.Histogram[r.Turns]++
				}
			} else {
				dist.Unfinished++
				censored[r.Turns]++
				dist.Censored = append(dist.Censored, r.Turns)
			}
			if config.IncludeTurns {
				report.Runs = append(report.Runs, r)
			}
		}
	}

	dist.Population = len(turns)
	if len(turns) > 0 {
		sort.Ints(turns)
		dist.P50 = percentile(turns, 50)
		dist.P75 = percentile(turns, 75)
		dist.P90 = percentile(turns, 90)
		dist.P95 = percentile(turns, 95)
		dist.Max = turns[len(turns)-1]
		total := 0
		for _, t := range turns {
			total += t
		}
		dist.Mean = float64(total) / float64(len(turns))
	}
	// Ties go to the larger turn count: the spike being looked for is a cap, and
	// reading a cap low is the error that costs work.
	for t, n := range censored {
		if n > dist.CensoredCount || (n == dist.CensoredCount && t > dist.CensoredMode) {
			dist.CensoredMode, dist.CensoredCount = t, n
		}
	}
	sort.Ints(dist.Censored)

	report.Distribution = dist
	return report, nil
}

// percentile returns the nearest-rank value from a sorted ascending slice.
//
// Nearest rank rather than interpolation, because the answer is used as a turn
// cap and a cap of 23.5 turns does not exist. It rounds up, so "the p75" is a
// value at least 75% of the runs came in at or under — which is the claim the
// caller then relies on.
func percentile(sorted []int, p int) int {
	if len(sorted) == 0 {
		return 0
	}
	rank := (p*len(sorted) + 99) / 100 // ceil(p/100 * n)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// Runs reconstructs the runs on a session's live branch, oldest first.
//
// Only the live branch: a rewind abandons the records after the fork without
// deleting them, and a run on an abandoned branch was cut short by a person
// rather than by the work, so counting it would put human edits into a
// measurement of task difficulty. This mirrors session.summarise, which walks
// back from the tip for the same reason.
func Runs(path string) ([]Run, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	type node struct {
		parent string
		rec    record
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
		var r record
		if json.Unmarshal([]byte(line), &r) != nil || r.ID == "" {
			continue
		}
		nodes[r.ID] = node{parent: r.ParentID, rec: r}
		last = r.ID
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	// Collected tip-first then reversed, because segmentation needs to read the
	// history forwards while only the parent links can be followed.
	var chain []record
	for id := last; id != ""; {
		n, ok := nodes[id]
		if !ok {
			break // Damage; history before it is unreachable, as it is for Open.
		}
		chain = append(chain, n.rec)
		id = n.parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return runsFrom(filepath.Base(path), chain), nil
}

// runsFrom segments a forward-ordered chain into runs.
//
// A run starts at a user prompt and ends when an assistant message asks for no
// tools. The one ambiguity is steering: a steered message is appended as a plain
// user text message, the same shape a fresh prompt has, so the transcript does
// not say which it was. It is recovered from position instead — steering lands
// after the previous turn's tool results, so a text-only user message whose
// predecessor is also a user message can only be steering, since an ordinary
// history alternates roles.
//
// That leaves one case unrecoverable: a message steered in after the model had
// already stopped asking for tools (loop.go keeps the run open for exactly that)
// looks identical to a new prompt, and gets counted as one. It splits a long run
// into two shorter ones, so it can only push the percentiles *down* — the same
// direction censoring pushes them. Steered is reported so the reader can see how
// much of the set is exposed to it; on a set where it is rare the bound is tight.
func runsFrom(session string, chain []record) []Run {
	var out []Run
	cur := (*Run)(nil)
	curStart := int64(0)
	prevRole := ""

	for _, r := range chain {
		if r.Type != "message" || r.Message == nil {
			continue
		}
		role := r.Message.Role
		switch role {
		case "user":
			results := false
			for _, b := range r.Message.Content {
				if b.Type == "tool_result" {
					results = true
					break
				}
			}
			switch {
			case results:
				// Tool results continue the run they belong to.
			case prevRole == "user" && cur != nil:
				cur.Steered = true
			default:
				out = append(out, Run{Session: session, Index: len(out) + 1})
				cur = &out[len(out)-1]
				curStart = r.Time
			}
		case "assistant":
			if cur == nil {
				// An assistant message with no prompt before it: a transcript
				// truncated by damage. Counting it as a run would report a turn
				// count for work whose beginning is missing.
				prevRole = role
				continue
			}
			cur.Turns++
			calls := 0
			for _, b := range r.Message.Content {
				if b.Type == "tool_use" {
					calls++
				}
			}
			cur.Tools += calls
			// Last assistant message wins: a run kept open by steering has an
			// earlier tool-free message that did not end it.
			cur.Finished = calls == 0
		}
		// Extended by every later message on the run, so the value always spans
		// prompt to last record rather than prompt to first reply.
		if cur != nil && r.Time > curStart && curStart > 0 {
			cur.Duration = r.Time - curStart
		}
		prevRole = role
	}

	// A run with no assistant message at all never reached the model.
	kept := out[:0]
	for _, r := range out {
		if r.Turns > 0 {
			kept = append(kept, r)
		}
	}
	return kept
}

// FormatRunsText renders the distribution as a report.
//
// It is written to be read as an argument for a number, not as a table: the
// percentile alone invites being copied into -max-turns without noticing how much
// of the data was censored, and on a heavily censored set that number is wrong in
// a direction that silently throws away work.
func FormatRunsText(report *RunReport) string {
	var b strings.Builder
	d := report.Distribution

	b.WriteString(fmt.Sprintf("Run Length Distribution: %s\n", report.Dir))
	b.WriteString(strings.Repeat("=", 80) + "\n\n")

	b.WriteString("Population:\n")
	b.WriteString(fmt.Sprintf("  Sessions read: %d\n", d.Sessions))
	if d.Unreadable > 0 {
		b.WriteString(fmt.Sprintf("  Unreadable:    %d\n", d.Unreadable))
	}
	b.WriteString(fmt.Sprintf("  Runs:          %d\n", d.Runs))
	b.WriteString(fmt.Sprintf("    finished (reached an answer):   %d\n", d.Finished))
	b.WriteString(fmt.Sprintf("    unfinished (cut off):           %d\n", d.Unfinished))
	if d.Steered > 0 {
		b.WriteString(fmt.Sprintf("    steered mid-run:                %d\n", d.Steered))
	}
	if d.Trivial > 0 {
		b.WriteString(fmt.Sprintf("    no tool call, excluded:         %d\n", d.Trivial))
	}
	b.WriteString(fmt.Sprintf("  Population:    %d (finished, called at least one tool)\n", d.Population))
	b.WriteString("\n")

	if d.Population == 0 {
		if d.Finished == 0 {
			b.WriteString("No finished run in this set, so there is no distribution to report.\n" +
				"Every run here was cut off, which measures the limits that were in\n" +
				"effect rather than the work.\n")
		} else {
			b.WriteString("No finished run in this set called a tool, so there is no distribution\n" +
				"to report. A run that calls no tool ends on its first turn whatever the\n" +
				"cap is, so it carries no information about where the cap belongs.\n")
		}
		// The censoring read still runs here: a set with no finished run at all is
		// where it matters most, since censoring is then the whole story rather
		// than a caveat on the percentiles.
		b.WriteString("\n")
		writeCensoring(&b, d)
		return b.String()
	}

	b.WriteString(fmt.Sprintf("Turns per run (n=%d):\n", d.Population))
	b.WriteString(fmt.Sprintf("  p50: %d\n", d.P50))
	b.WriteString(fmt.Sprintf("  p75: %d\n", d.P75))
	b.WriteString(fmt.Sprintf("  p90: %d\n", d.P90))
	b.WriteString(fmt.Sprintf("  p95: %d\n", d.P95))
	b.WriteString(fmt.Sprintf("  max: %d\n", d.Max))
	b.WriteString(fmt.Sprintf("  mean: %.1f\n", d.Mean))
	b.WriteString("\n")

	writeHistogram(&b, d.Histogram, d.Population)

	covered := atOrUnder(d.Histogram, d.P75)
	b.WriteString(fmt.Sprintf("A cap at p75 (%d turns) covers %d of %d runs (%.0f%%).\n",
		d.P75, covered, d.Population, float64(covered)*100/float64(d.Population)))
	b.WriteString(fmt.Sprintf("It would cut %d of them, the longest at %d turns.\n\n",
		d.Population-covered, d.Max))

	writeCensoring(&b, d)
	// The threshold is the point where the censored runs outnumber the tail the
	// p75 is drawn from, i.e. where the missing data could plausibly move it.
	if d.Runs > 0 && d.Unfinished*4 >= d.Runs {
		b.WriteString(fmt.Sprintf("\nWarning: %.0f%% of runs were cut off. Each is a lower bound on the turns\n"+
			"the work needed, so the percentiles above are biased low, hardest in the\n"+
			"tail a cap is chosen from. Treat p75 as a floor, not the answer.\n",
			float64(d.Unfinished)*100/float64(d.Runs)))
	}
	// Said out loud because the p75 above is the one number a reader will lift, and
	// lifting it is only valid when the history it came from resembles the work the
	// cap will govern. A directory of interactive prompts and a directory of
	// unattended task runs give different answers, and nothing in the number says
	// which one it came from.
	b.WriteString("\nThis is the distribution of the runs in this directory, which is only the\n" +
		"right basis for a cap if those runs resemble the work the cap will govern.\n" +
		"Interactive prompts finish in a few turns; an unattended task is the case a\n" +
		"cap actually binds on, and it will not be represented here unless it is what\n" +
		"this history contains.\n")

	if d.Steered > 0 && d.Steered*4 >= d.Runs {
		b.WriteString(fmt.Sprintf("\nWarning: %.0f%% of runs were steered. A message steered in after the model\n"+
			"stopped calling tools is indistinguishable from a new prompt, so some of\n"+
			"these runs are split in two here, which also pushes the percentiles low.\n",
			float64(d.Steered)*100/float64(d.Runs)))
	}

	return b.String()
}

// writeCensoring lists the turn counts the censored runs stopped at, and judges
// whether they agree on one. A pile-up at a single count is what a turn cap looks
// like — no transcript records -max-turns, so it is the only evidence of it — but
// a handful of scattered interruptions is not, and naming their mode invents a
// limit.
func writeCensoring(b *strings.Builder, d RunDistribution) {
	if len(d.Censored) == 0 {
		return
	}
	b.WriteString(fmt.Sprintf("Censoring: %d run(s) stopped without an answer, at %s turns.\n",
		d.Unfinished, joinInts(d.Censored)))
	// Named as a cap only when most of the censored runs agree. Below that it is
	// a handful of interruptions landing wherever they landed.
	if d.CensoredCount >= 3 && d.CensoredCount*2 > d.Unfinished {
		b.WriteString(fmt.Sprintf("  %d of them stopped at %d, which is what a turn cap looks like: no\n"+
			"  transcript records -max-turns, so a pile-up is the only evidence of it.\n",
			d.CensoredCount, d.CensoredMode))
	} else {
		b.WriteString("  Too few, and too scattered, to show a cap. These read as interruptions\n" +
			"  (Ctrl-C, a dead connection) rather than a limit being hit — except any\n" +
			"  value that equals a cap you ran with.\n")
	}
}

func joinInts(v []int) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = fmt.Sprint(n)
	}
	return strings.Join(parts, ", ")
}

func atOrUnder(hist map[int]int, limit int) int {
	n := 0
	for turns, count := range hist {
		if turns <= limit {
			n += count
		}
	}
	return n
}

// writeHistogram draws the buckets, collapsing empty ranges rather than printing
// a row of zeros for every turn count nothing landed on.
func writeHistogram(b *strings.Builder, hist map[int]int, total int) {
	if len(hist) == 0 {
		return
	}
	keys := make([]int, 0, len(hist))
	peak := 0
	for k, v := range hist {
		keys = append(keys, k)
		if v > peak {
			peak = v
		}
	}
	sort.Ints(keys)

	b.WriteString("Histogram:\n")
	const width = 40
	for _, k := range keys {
		bar := hist[k] * width / peak
		if bar < 1 {
			bar = 1
		}
		b.WriteString(fmt.Sprintf("  %3d turns %s %d (%.0f%%)\n",
			k, strings.Repeat("█", bar), hist[k], float64(hist[k])*100/float64(total)))
	}
	b.WriteString("\n")
}

// FormatRunsJSON produces a JSON representation of the report.
func FormatRunsJSON(report *RunReport) (string, error) {
	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal run report to JSON: %w", err)
	}
	return string(out), nil
}
