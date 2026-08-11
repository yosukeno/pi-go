package tui

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/wangy/pi-go/agent"
	"github.com/wangy/pi-go/llm"
)

// PrintUsage is the long form of usageLine, for /usage. It spells out where each
// number comes from, because the counters are cumulative and non-disjoint and
// every reader of the short form eventually asks about one or the other.
func PrintUsage(w io.Writer, u llm.Usage, t agent.Timing) {
	fmt.Fprintf(w, "%-22s %10s tok  %sprompt: system + tool schemas + the whole conversation, resent every turn%s\n",
		"input", thousands(u.Input), Dim, Reset)
	fmt.Fprintf(w, "%-22s %10s tok  %sof that input, served from the cached prefix (%d%%) and billed cheaper%s\n",
		"  from cache", thousands(u.CacheRead), Dim, percent(u.CacheRead, u.Input), Reset)
	fmt.Fprintf(w, "%-22s %10s tok  %sthe part that was billed at full rate%s\n",
		"  fresh", thousands(u.FreshInput()), Dim, Reset)
	fmt.Fprintf(w, "%-22s %10s tok  %scompletion: thinking + text + tool call arguments%s\n",
		"output", thousands(u.Output), Dim, Reset)
	fmt.Fprintf(w, "%-22s %10s tok  %sof that output, spent on thinking%s\n",
		"  thinking", thousands(u.Reasoning), Dim, Reset)
	fmt.Fprintf(w, "%s\ntotals are for the whole session and grow faster than the conversation,\n"+
		"because every turn resends it. they are a cost figure, not a context gauge.%s\n", Dim, Reset)

	if t.Calls == 0 {
		return
	}
	fmt.Fprintf(w, "\n%-22s %10s     %sfirst token, averaged over %d model call(s)%s\n",
		"latency", humanDuration(t.AvgTTFT), Dim, t.Calls, Reset)
	fmt.Fprintf(w, "%-22s %10s     %sresponse headers: network and provider queueing%s\n",
		"  connect", humanDuration(t.AvgTTFB), Dim, Reset)
	fmt.Fprintf(w, "%-22s %10s     %sprefill plus, on a reasoning model, starting to think%s\n",
		"  model startup", humanDuration(t.AvgTTFT-t.AvgTTFB), Dim, Reset)
	fmt.Fprintf(w, "%-22s %10s     %sslowest single wait%s\n",
		"  worst", humanDuration(t.MaxTTFT), Dim, Reset)
	fmt.Fprintf(w, "%-22s %10s     %severy first-token wait added up%s\n",
		"  spent waiting", humanDuration(t.TotalWait), Dim, Reset)
}

// usageLine renders the run's token accounting and how long its turns took to
// start.
//
// Units are on every number on purpose. "in 12463" is ambiguous between tokens,
// bytes and characters, and the three differ by more than an order of magnitude
// for Chinese text, so a reader guessing wrong misjudges the cost badly.
//
// The nesting is spelled out for the same reason: cached is a slice of in, not an
// addition to it, and reasoning is a slice of out. Printing them as a flat list of
// four numbers invites adding them up, which double-counts. See llm.Usage.
func usageLine(u llm.Usage, t agent.Timing) string {
	parts := []string{fmt.Sprintf("in %s tok", thousands(u.Input))}
	if u.CacheRead > 0 {
		parts = append(parts, fmt.Sprintf("%s tok cached (%d%%)",
			thousands(u.CacheRead), percent(u.CacheRead, u.Input)))
	}
	parts = append(parts, fmt.Sprintf("out %s tok", thousands(u.Output)))
	if u.Reasoning > 0 {
		parts = append(parts, fmt.Sprintf("%s tok thinking", thousands(u.Reasoning)))
	}
	line := strings.Join(parts, ", ")
	if t.Calls > 0 {
		line += " · " + ttftLine(t)
	}
	return line
}

// ttftLine describes the first-token wait. The average alone hides the case that
// prompted the measurement — one turn that took ten seconds among five that took
// one — so the worst one is named whenever there is more than one sample.
func ttftLine(t agent.Timing) string {
	s := fmt.Sprintf("ttft %s", humanDuration(t.AvgTTFT))
	if t.Calls > 1 {
		s += fmt.Sprintf(" avg of %d, max %s", t.Calls, humanDuration(t.MaxTTFT))
	}
	return s
}

// humanDuration prints a latency at a precision worth reading: milliseconds
// below a second, tenths above.
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "n/a"
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// percent rounds a share to a whole number, reporting 0 for an empty total.
func percent(part, whole int64) int64 {
	if whole <= 0 {
		return 0
	}
	return part * 100 / whole
}

// thousands groups digits. Token counts run to six figures on a long session,
// and at that length the grouping is the difference between reading the number
// and counting its digits.
func thousands(n int64) string {
	s := fmt.Sprintf("%d", n)
	if n < 0 {
		return s
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// HumanCtx renders a context-window size compactly: 200000 prints as "200K".
func HumanCtx(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.0fM", float64(n)/1_000_000)
	}
	return fmt.Sprintf("%dK", n/1000)
}
