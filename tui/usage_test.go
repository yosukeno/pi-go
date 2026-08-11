package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pi-go/agent"
	"github.com/yosukeno/pi-go/llm"
)

func TestUsageLineNamesItsUnitsAndShowsCacheAsAShare(t *testing.T) {
	got := usageLine(
		llm.Usage{Input: 25473, Output: 935, CacheRead: 21504, Reasoning: 651},
		agent.Timing{Calls: 2, AvgTTFT: 1800 * time.Millisecond, MaxTTFT: 2400 * time.Millisecond},
	)
	for _, want := range []string{
		"in 25,473 tok",
		// A share, not a fourth number: cached is part of input, and printing it
		// as a peer invites adding the two together.
		"21,504 tok cached (84%)",
		"out 935 tok",
		"651 tok thinking",
		"ttft 1.8s avg of 2, max 2.4s",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("usageLine() = %q, missing %q", got, want)
		}
	}
}

func TestUsageLineOmitsWhatWasNotReported(t *testing.T) {
	// A provider with no prompt caching and no reasoning split must not produce
	// "cached 0 tok (0%)" — a zero here means "not reported", not "nothing hit".
	got := usageLine(llm.Usage{Input: 900, Output: 40}, agent.Timing{})
	if strings.Contains(got, "cached") || strings.Contains(got, "thinking") {
		t.Errorf("usageLine() = %q, want no cached/thinking clause", got)
	}
	// No measured call means no latency claim at all, rather than "ttft n/a".
	if strings.Contains(got, "ttft") {
		t.Errorf("usageLine() = %q, want no ttft clause with zero calls", got)
	}
}

func TestSingleCallTTFTDropsTheAverageWording(t *testing.T) {
	got := usageLine(llm.Usage{Input: 10, Output: 1}, agent.Timing{
		Calls: 1, AvgTTFT: 3600 * time.Millisecond, MaxTTFT: 3600 * time.Millisecond,
	})
	if !strings.Contains(got, "ttft 3.6s") {
		t.Errorf("usageLine() = %q, want %q", got, "ttft 3.6s")
	}
	if strings.Contains(got, "avg") || strings.Contains(got, "max") {
		t.Errorf("usageLine() = %q: one sample is not an average", got)
	}
}

func TestHumanDurationSwitchesPrecisionAtASecond(t *testing.T) {
	cases := map[time.Duration]string{
		0:                       "n/a",
		420 * time.Millisecond:  "420ms",
		999 * time.Millisecond:  "999ms",
		time.Second:             "1.0s",
		4300 * time.Millisecond: "4.3s",
	}
	for in, want := range cases {
		if got := humanDuration(in); got != want {
			t.Errorf("humanDuration(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestThousandsGroupsDigits(t *testing.T) {
	cases := map[int64]string{0: "0", 999: "999", 1000: "1,000", 25473: "25,473", 1234567: "1,234,567"}
	for in, want := range cases {
		if got := thousands(in); got != want {
			t.Errorf("thousands(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestPrintUsageSpellsOutTheNesting(t *testing.T) {
	var b strings.Builder
	PrintUsage(&b, llm.Usage{Input: 25473, Output: 935, CacheRead: 21504, Reasoning: 651},
		agent.Timing{
			Calls: 3, AvgTTFT: 2 * time.Second, AvgTTFB: 300 * time.Millisecond,
			MaxTTFT: 4 * time.Second, TotalWait: 6 * time.Second,
		})
	got := b.String()
	for _, want := range []string{
		"25,473 tok",
		// The two derived numbers are the point of the long form: cached is a
		// subset of input, so the fresh remainder is what was billed at full rate.
		"21,504 tok",
		"3,969 tok",
		"of that output, spent on thinking",
		// The split between network and model is what tells someone whether a slow
		// turn is their connection or the model.
		"connect",
		"model startup",
		"1.7s", // AvgTTFT - AvgTTFB
		"4.0s", // worst
	} {
		if !strings.Contains(got, want) {
			t.Errorf("PrintUsage() missing %q in:\n%s", want, got)
		}
	}
}

func TestPrintUsageSaysNothingAboutLatencyWithNoCalls(t *testing.T) {
	var b strings.Builder
	PrintUsage(&b, llm.Usage{Input: 100, Output: 10}, agent.Timing{})
	if strings.Contains(b.String(), "latency") {
		t.Errorf("PrintUsage() reported latency with no measured calls:\n%s", b.String())
	}
}
