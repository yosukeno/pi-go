package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/wangy/pi-go/llm"
)

// TestDetectStagnation tests the stagnation detection logic
func TestDetectStagnation(t *testing.T) {
	a := &Agent{stagnationThreshold: 3}

	tests := []struct {
		name     string
		history  []string
		expected bool
		reason   string
	}{
		{
			name:     "No history",
			history:  []string{},
			expected: false,
			reason:   "",
		},
		{
			name:     "Below threshold",
			history:  []string{"hash1", "hash2"},
			expected: false,
			reason:   "",
		},
		{
			name:     "Exactly at threshold",
			history:  []string{"hash1", "hash1", "hash1"},
			expected: true,
			reason:   "3 identical tool results in a row",
		},
		{
			name:     "Above threshold",
			history:  []string{"hash1", "hash1", "hash1", "hash1"},
			expected: true,
			reason:   "3 identical tool results in a row",
		},
		{
			name:     "Pattern broken",
			history:  []string{"hash1", "hash1", "hash2", "hash1", "hash1"},
			expected: false,
			reason:   "",
		},
		{
			name:     "Consecutive then broken",
			history:  []string{"hash1", "hash1", "hash1", "hash2"},
			expected: false,
			reason:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stagnant, reason := a.detectStagnation(tt.history)
			if stagnant != tt.expected {
				t.Errorf("detectStagnation() stagnant = %v, expected %v", stagnant, tt.expected)
			}
			if reason != tt.reason {
				t.Errorf("detectStagnation() reason = %q, expected %q", reason, tt.reason)
			}
		})
	}
}

// TestHashToolResults tests the hash function for tool results
func TestHashToolResults(t *testing.T) {
	a := &Agent{}

	results1 := []llm.Block{
		{Type: llm.BlockToolResult, ToolUseID: "call1", Name: "read", Text: "file content"},
		{Type: llm.BlockToolResult, ToolUseID: "call2", Name: "write", Text: "written"},
	}

	results2 := []llm.Block{
		{Type: llm.BlockToolResult, ToolUseID: "call1", Name: "read", Text: "file content"},
		{Type: llm.BlockToolResult, ToolUseID: "call2", Name: "write", Text: "written"},
	}

	results3 := []llm.Block{
		{Type: llm.BlockToolResult, ToolUseID: "call1", Name: "read", Text: "different content"},
		{Type: llm.BlockToolResult, ToolUseID: "call2", Name: "write", Text: "written"},
	}

	hash1 := a.hashToolResults(results1)
	hash2 := a.hashToolResults(results2)
	hash3 := a.hashToolResults(results3)

	if hash1 != hash2 {
		t.Errorf("Same tool results should produce same hash: %q != %q", hash1, hash2)
	}

	if hash1 == hash3 {
		t.Errorf("Different tool results should produce different hash: %q == %q", hash1, hash3)
	}
}

// TestCheckBudgets tests the budget checking logic.
//
// The assertion is on the EndReason rather than on a boolean, because which budget
// was hit is the part a caller acts on: the two are refilled by different things
// (money, waiting) and a driver deciding whether to start another run needs to tell
// them apart without reading the detail prose.
func TestCheckBudgets(t *testing.T) {
	tests := []struct {
		name         string
		tokenBudget  int64
		costBudget   float64
		price        *llm.Price
		timeBudget   time.Duration
		elapsed      time.Duration
		currentUsage llm.Usage
		want         EndReason
		detailSubstr string
	}{
		{
			name:         "No budget limit",
			tokenBudget:  0,
			currentUsage: llm.Usage{Input: 1000, Output: 500},
			want:         "",
		},
		{
			name:         "Within budget",
			tokenBudget:  10000,
			currentUsage: llm.Usage{Input: 4000, Output: 1000},
			want:         "",
		},
		{
			name:         "At budget limit",
			tokenBudget:  5000,
			currentUsage: llm.Usage{Input: 3000, Output: 2000},
			want:         EndTokenBudget,
			detailSubstr: "token budget exceeded",
		},
		{
			name:         "Over budget",
			tokenBudget:  1000,
			currentUsage: llm.Usage{Input: 800, Output: 400},
			want:         EndTokenBudget,
			detailSubstr: "token budget exceeded",
		},
		{
			name:         "Time budget spent",
			timeBudget:   time.Minute,
			elapsed:      2 * time.Minute,
			currentUsage: llm.Usage{Input: 10, Output: 10},
			want:         EndTimeBudget,
			detailSubstr: "time budget exceeded",
		},
		{
			name:         "Time budget still has room",
			timeBudget:   time.Hour,
			elapsed:      time.Minute,
			currentUsage: llm.Usage{Input: 10, Output: 10},
			want:         "",
		},
		{
			// Reasoning is a subset of Output, so it must not be added on top. This case
			// is over budget only if it is: 600 + 500 is under 1200, 600 + 500 + 400 is
			// not. The bug this pins used to stop thinking-heavy runs that had room left,
			// which reads as the budget working.
			name:         "Reasoning is not added on top of Output",
			tokenBudget:  1200,
			currentUsage: llm.Usage{Input: 600, Output: 500, Reasoning: 400},
			want:         "",
		},
		{
			// CacheRead is a subset of Input, same rule in the other field. Was already
			// right; pinned so the two stay consistent.
			name:         "CacheRead is not added on top of Input",
			tokenBudget:  1200,
			currentUsage: llm.Usage{Input: 600, CacheRead: 500, Output: 500},
			want:         "",
		},
		{
			// A cost budget with no price does nothing here, on purpose: the CLI refuses
			// that combination before a run starts (see checkCostBudget), so reaching it
			// would mean that guard was bypassed, and inventing a cost then would compound
			// two errors rather than catching one.
			name:         "Cost budget without a price does not fire",
			costBudget:   0.001,
			currentUsage: llm.Usage{Input: 10_000_000, Output: 10_000_000},
			want:         "",
		},
		{
			name:         "Cost budget spent",
			costBudget:   1.0,
			price:        &llm.Price{Input: 10, Output: 30},
			currentUsage: llm.Usage{Input: 1_000_000, Output: 0}, // 10 units
			want:         EndCostBudget,
			detailSubstr: "cost budget exceeded",
		},
		{
			name:         "Cost budget still has room",
			costBudget:   1.0,
			price:        &llm.Price{Input: 10, Output: 30},
			currentUsage: llm.Usage{Input: 10_000, Output: 0}, // 0.1 units
			want:         "",
		},
		{
			// The cache rate is where a ceiling earns its keep: the same token count
			// costs an order of magnitude less when the prefix was reused, so a budget
			// that ignored CacheRead would stop a cheap run early.
			name:         "Cached tokens are charged at the cache rate",
			costBudget:   1.0,
			price:        &llm.Price{Input: 10, Output: 30, CacheRead: 0.1},
			currentUsage: llm.Usage{Input: 1_000_000, CacheRead: 990_000},
			want:         "", // 0.01M*10 + 0.99M*0.1 = 0.199, under 1.0
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Agent{
				tokenBudget:  tt.tokenBudget,
				costBudget:   tt.costBudget,
				price:        tt.price,
				timeBudget:   tt.timeBudget,
				usage:        tt.currentUsage,
				runStartTime: time.Now().Add(-tt.elapsed),
			}
			got, detail := a.checkBudgets()
			if got != tt.want {
				t.Errorf("checkBudgets() reason = %q, want %q", got, tt.want)
			}
			if tt.detailSubstr != "" && !strings.Contains(detail, tt.detailSubstr) {
				t.Errorf("checkBudgets() detail = %q, want it to contain %q", detail, tt.detailSubstr)
			}
			if tt.want == "" && detail != "" {
				t.Errorf("checkBudgets() returned no reason but a detail %q", detail)
			}
		})
	}
}
