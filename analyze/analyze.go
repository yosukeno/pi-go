// Package analyze implements session file analysis for performance metrics.
package analyze

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/yosukeno/pi-go/session"
	"github.com/yosukeno/pi-go/tools"
)

// SessionStats contains all computed statistics for a session.
type SessionStats struct {
	SessionPath     string             `json:"session_path"`
	RoundCount      int                `json:"round_count"`
	Duration        int64              `json:"duration_ms"` // Total session duration in milliseconds
	TokenUsage      TokenAnalysis      `json:"token_usage"`
	BatchSizes      BatchDistribution  `json:"batch_sizes"`
	ToolTiming      ToolTimingAnalysis `json:"tool_timing"`
	RetryStats      RetryAnalysis      `json:"retry_stats"`
	TruncationStats TruncationAnalysis `json:"truncation_stats"`
	ApprovalStats   ApprovalAnalysis   `json:"approval_stats"`
	// Composition is the last one recorded, not a sum: it describes the whole
	// history rather than one run's increment. Nil for a transcript written before
	// the field existed, and for one that never completed a model call.
	Composition *session.Composition `json:"composition,omitempty"`
	Turns       []TurnStats          `json:"turns,omitempty"` // Per-turn stats, included in verbose mode
}

// TokenAnalysis aggregates token usage across all turns.
type TokenAnalysis struct {
	TotalInput     int64   `json:"total_input"`
	TotalOutput    int64   `json:"total_output"`
	TotalCacheRead int64   `json:"total_cache_read,omitempty"`
	TotalReasoning int64   `json:"total_reasoning,omitempty"`
	AverageInput   float64 `json:"average_input"`
	AverageOutput  float64 `json:"average_output"`
	MaxInput       int64   `json:"max_input"`
	MaxOutput      int64   `json:"max_output"`
	// Delegated is the part of the totals that subagents spent. A subset, never a
	// sibling: Total minus Delegated is what this agent itself spent, and adding them
	// would count the same tokens twice.
	DelegatedInput  int64 `json:"delegated_input,omitempty"`
	DelegatedOutput int64 `json:"delegated_output,omitempty"`
}

// BatchDistribution tracks how many tool calls were made per turn.
//
// The three fields past BySize exist to answer one question the counts cannot:
// whether batching is actually buying anything. A batch of four is only worth more
// than four batches of one if the four overlap, and in this harness they do not when
// any of them is a sequential tool — one such call serializes the whole batch (see
// agent.parallelBatch). So the size distribution alone can look healthy while every
// batch runs one at a time.
type BatchDistribution struct {
	TotalCalls int         `json:"total_calls"`
	Singles    int         `json:"singles"`  // 1 call
	Doubles    int         `json:"doubles"`  // 2 calls
	Multiple   int         `json:"multiple"` // 3+ calls
	BySize     map[int]int `json:"by_size"`  // count for each batch size
	// AverageCalls is calls per tool-calling turn. Anthropic's own check for whether
	// parallel tool use is working at all is this number being meaningfully above
	// 1.0, so it is reported rather than left to the reader's division.
	AverageCalls float64 `json:"average_calls"`
	// Combos counts turns by the set of tools they called, names sorted and joined
	// with "+" so that read+bash and bash+read are one entry. This is what says
	// which batches a harness change would even touch.
	Combos map[string]int `json:"combos,omitempty"`
	// Stalled counts parallel-capable calls that shared a batch with a sequential
	// one, and therefore ran one at a time for a reason that had nothing to do with
	// them. It is the size of the prize for making the batch rule finer-grained, and
	// zero is a real answer: it means the rule costs this workload nothing.
	Stalled int `json:"stalled_by_sequential_sibling"`
}

// sequentialByName reports, per tool name, whether that tool refuses to overlap a
// sibling. Read from the tools' own declarations rather than a list kept here, so a
// tool changing its mind cannot leave this report quietly wrong.
//
// tools.Default covers the seven that touch the workspace. todo and subagent are
// registered only for a top-level session and are absent here; both declare
// Parallel, so their absence gives the right answer today. A future sequential tool
// outside the default set would have to be added.
var sequentialByName = func() map[string]bool {
	m := map[string]bool{}
	for _, t := range tools.Default(".").All() {
		m[t.Name()] = t.ExecutionMode() == tools.Sequential
	}
	return m
}()

// comboKey names a batch by its tool set, order-insensitively.
func comboKey(names []string) string {
	sorted := make([]string, len(names))
	copy(sorted, names)
	sort.Strings(sorted)
	return strings.Join(sorted, "+")
}

// ToolTimingAnalysis aggregates tool execution timing.
type ToolTimingAnalysis struct {
	TotalCalls      int                  `json:"total_calls"`
	TotalDuration   int64                `json:"total_duration_ms"`
	AverageDuration float64              `json:"average_duration_ms"`
	MaxDuration     int64                `json:"max_duration_ms"`
	MinDuration     int64                `json:"min_duration_ms"`
	ByTool          map[string]ToolStats `json:"by_tool"`
}

// ToolStats holds statistics for a specific tool.
type ToolStats struct {
	CallCount       int     `json:"call_count"`
	TotalDuration   int64   `json:"total_duration_ms"`
	AverageDuration float64 `json:"average_duration_ms"`
	MaxDuration     int64   `json:"max_duration_ms"`
	MinDuration     int64   `json:"min_duration_ms"`
	ErrorCount      int     `json:"error_count"`
}

// RetryAnalysis tracks retry behavior.
type RetryAnalysis struct {
	TotalRetries  int            `json:"total_retries"`
	RetriesByTurn map[int]int    `json:"retries_by_turn"` // turn number -> retry count
	Reasons       map[string]int `json:"reasons"`         // reason -> count
}

// TruncationAnalysis tracks message truncation.
type TruncationAnalysis struct {
	TruncatedCount int     `json:"truncated_count"`
	TruncationRate float64 `json:"truncation_rate"`
}

// ApprovalAnalysis tracks approval gate behavior.
type ApprovalAnalysis struct {
	TotalRequests   int     `json:"total_requests"`
	DeniedCount     int     `json:"denied_count"`
	TotalWaitTime   int64   `json:"total_wait_time_ms"`
	AverageWaitTime float64 `json:"average_wait_time_ms"`
	MaxWaitTime     int64   `json:"max_wait_time_ms"`
}

// TurnStats represents statistics for a single turn.
type TurnStats struct {
	TurnNumber int                 `json:"turn_number"`
	Timestamp  int64               `json:"timestamp"`
	Usage      *TokenAnalysis      `json:"usage,omitempty"`
	Tools      *ToolTimingAnalysis `json:"tools,omitempty"`
	Retry      *RetryAnalysis      `json:"retry,omitempty"`
	Approval   *ApprovalAnalysis   `json:"approval,omitempty"`
}

// record represents a JSONL record in the session file.
type record struct {
	ID       string         `json:"id"`
	ParentID string         `json:"parentId,omitempty"`
	Type     string         `json:"type"`
	Time     int64          `json:"time"`
	Message  *messageRecord `json:"message,omitempty"`
	Meta     *metaRecord    `json:"meta,omitempty"`
	Stats    *statsRecord   `json:"stats,omitempty"`
}

type messageRecord struct {
	Role    string        `json:"role"`
	Content []blockRecord `json:"content"`
}

type blockRecord struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type metaRecord struct {
	Cwd    string   `json:"cwd"`
	Model  string   `json:"model"`
	Skills []string `json:"skills,omitempty"`
}

type statsRecord struct {
	Usage *usageStatsRecord `json:"usage,omitempty"`
	// Delegated is the part of Usage spent inside subagents, absent on turns that
	// delegated nothing. Older transcripts have no such field and read as zero, which
	// is the right answer for them: they predate the subagent tool.
	Delegated *usageStatsRecord    `json:"delegated,omitempty"`
	Tools     *toolStatsRecord     `json:"tools,omitempty"`
	Retry     *retryStatsRecord    `json:"retry,omitempty"`
	Approval  *approvalStatsRecord `json:"approval,omitempty"`
	// Composition is the only field here that is not a delta: it describes the whole
	// history at the moment the record was written, so the last one wins and they
	// must not be summed. Absent on transcripts written before it existed.
	Composition *session.Composition `json:"composition,omitempty"`
}

type usageStatsRecord struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	CacheRead int64 `json:"cache_read,omitempty"`
	Reasoning int64 `json:"reasoning,omitempty"`
}

type toolStatsRecord struct {
	CallCount int                  `json:"call_count"`
	Duration  int64                `json:"duration"`
	ExecTime  []toolExecTimeRecord `json:"exec_time,omitempty"`
}

type toolExecTimeRecord struct {
	Name     string `json:"name"`
	Duration int64  `json:"duration"`
	IsError  bool   `json:"is_error,omitempty"`
}

type retryStatsRecord struct {
	Count  int    `json:"count"`
	Reason string `json:"reason,omitempty"`
}

type approvalStatsRecord struct {
	WaitTime  int64 `json:"wait_time"`
	AskCount  int   `json:"ask_count"`
	DenyCount int   `json:"deny_count"`
}

// Config holds analysis configuration.
type Config struct {
	IncludeTurns bool // Whether to include per-turn stats
}

// AnalyzeSession reads and analyzes a session JSONL file.
func AnalyzeSession(path string, config Config) (*SessionStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	stats := &SessionStats{
		SessionPath:     path,
		Turns:           []TurnStats{},
		TokenUsage:      TokenAnalysis{},
		BatchSizes:      BatchDistribution{BySize: make(map[int]int), Combos: make(map[string]int)},
		ToolTiming:      ToolTimingAnalysis{ByTool: make(map[string]ToolStats)},
		RetryStats:      RetryAnalysis{RetriesByTurn: make(map[int]int), Reasons: make(map[string]int)},
		TruncationStats: TruncationAnalysis{},
		ApprovalStats:   ApprovalAnalysis{},
	}

	var records []record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	// First pass: read all records
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue // Skip unparseable lines
		}
		records = append(records, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("error reading session file: %w", err)
	}

	if len(records) == 0 {
		return stats, nil
	}

	// Get session time range
	firstTime := records[0].Time
	lastTime := records[len(records)-1].Time
	stats.Duration = lastTime - firstTime

	// Group records by turn (message chains). The names are kept, not just the
	// count: which tools shared a batch is what decides whether the batch overlapped.
	turnNumber := 0
	toolNamesByTurn := make(map[int][]string)

	// Process records to compute statistics
	for _, r := range records {
		switch r.Type {
		// Every stats record counts, wherever it sits. This used to be reached only
		// from the assistant-message branch below, which meant a record was aggregated
		// only if it happened to be the line right after an assistant message — and a
		// turn that ended in tool results ends with a *user* message, so its cost was
		// silently dropped. A session total must not depend on record adjacency.
		//
		// turnNumber is whatever turn has been seen so far, which is what attributes a
		// record to the turn it followed. Adjacency is still the rule for that, because
		// it is the only ordering information a transcript carries; it is just no
		// longer the rule for whether the record exists.
		case "stats":
			if r.Stats != nil {
				processStats(stats, r.Stats, turnNumber, config)
			}

		case "message":
			if r.Message != nil && r.Message.Role == "assistant" {
				turnNumber++

				// Count tool calls from content
				var names []string
				for _, block := range r.Message.Content {
					if block.Type == "tool_use" {
						names = append(names, block.Name)
					}
				}
				if len(names) > 0 {
					toolNamesByTurn[turnNumber] = names
				}
			}
		}
	}

	// Compute batch distribution
	for _, names := range toolNamesByTurn {
		count := len(names)
		stats.BatchSizes.TotalCalls += count
		stats.BatchSizes.BySize[count]++
		stats.BatchSizes.Combos[comboKey(names)]++
		switch count {
		case 1:
			stats.BatchSizes.Singles++
		case 2:
			stats.BatchSizes.Doubles++
		default:
			if count >= 3 {
				stats.BatchSizes.Multiple++
			}
		}
		// A lone call has nothing to overlap with, so it is never stalled however
		// sequential it is.
		if count < 2 {
			continue
		}
		sequential, parallel := 0, 0
		for _, name := range names {
			if sequentialByName[name] {
				sequential++
			} else {
				parallel++
			}
		}
		if sequential > 0 {
			stats.BatchSizes.Stalled += parallel
		}
	}
	if turns := len(toolNamesByTurn); turns > 0 {
		stats.BatchSizes.AverageCalls = float64(stats.BatchSizes.TotalCalls) / float64(turns)
	}

	// Compute averages
	if turnNumber > 0 {
		stats.TokenUsage.AverageInput = float64(stats.TokenUsage.TotalInput) / float64(turnNumber)
		stats.TokenUsage.AverageOutput = float64(stats.TokenUsage.TotalOutput) / float64(turnNumber)

		if stats.ToolTiming.TotalCalls > 0 {
			stats.ToolTiming.AverageDuration = float64(stats.ToolTiming.TotalDuration) / float64(stats.ToolTiming.TotalCalls)
		}

		if stats.ApprovalStats.TotalRequests > 0 {
			stats.ApprovalStats.AverageWaitTime = float64(stats.ApprovalStats.TotalWaitTime) / float64(stats.ApprovalStats.TotalRequests)
		}
	}

	stats.RoundCount = turnNumber

	// Compute truncation rate
	if stats.RoundCount > 0 {
		stats.TruncationStats.TruncationRate = float64(stats.TruncationStats.TruncatedCount) / float64(stats.RoundCount)
	}

	return stats, nil
}

func processStats(sessionStats *SessionStats, sr *statsRecord, turnNumber int, config Config) {
	// Process usage stats
	if sr.Usage != nil {
		sessionStats.TokenUsage.TotalInput += sr.Usage.Input
		sessionStats.TokenUsage.TotalOutput += sr.Usage.Output
		sessionStats.TokenUsage.TotalCacheRead += sr.Usage.CacheRead
		sessionStats.TokenUsage.TotalReasoning += sr.Usage.Reasoning

		if sr.Usage.Input > sessionStats.TokenUsage.MaxInput {
			sessionStats.TokenUsage.MaxInput = sr.Usage.Input
		}
		if sr.Usage.Output > sessionStats.TokenUsage.MaxOutput {
			sessionStats.TokenUsage.MaxOutput = sr.Usage.Output
		}
	}
	if sr.Delegated != nil {
		sessionStats.TokenUsage.DelegatedInput += sr.Delegated.Input
		sessionStats.TokenUsage.DelegatedOutput += sr.Delegated.Output
	}
	// Overwritten rather than accumulated, unlike everything around it: each record
	// describes the whole history at the time it was written, so the last one is the
	// state and a sum would be nonsense. See session.Composition.
	if sr.Composition != nil {
		sessionStats.Composition = sr.Composition
	}

	// Process tool stats
	if sr.Tools != nil {
		sessionStats.ToolTiming.TotalCalls += sr.Tools.CallCount
		sessionStats.ToolTiming.TotalDuration += sr.Tools.Duration

		if sr.Tools.Duration > sessionStats.ToolTiming.MaxDuration {
			sessionStats.ToolTiming.MaxDuration = sr.Tools.Duration
		}

		for _, exec := range sr.Tools.ExecTime {
			toolStats := sessionStats.ToolTiming.ByTool[exec.Name]
			toolStats.CallCount++
			toolStats.TotalDuration += exec.Duration

			if exec.Duration > toolStats.MaxDuration {
				toolStats.MaxDuration = exec.Duration
			}
			if toolStats.MinDuration == 0 || exec.Duration < toolStats.MinDuration {
				toolStats.MinDuration = exec.Duration
			}

			if exec.IsError {
				toolStats.ErrorCount++
			}

			sessionStats.ToolTiming.ByTool[exec.Name] = toolStats
		}
	}

	// Process retry stats
	if sr.Retry != nil && sr.Retry.Count > 0 {
		sessionStats.RetryStats.TotalRetries += sr.Retry.Count
		sessionStats.RetryStats.RetriesByTurn[turnNumber] = sr.Retry.Count
		if sr.Retry.Reason != "" {
			sessionStats.RetryStats.Reasons[sr.Retry.Reason]++
		}
	}

	// Process approval stats
	if sr.Approval != nil {
		sessionStats.ApprovalStats.TotalRequests += sr.Approval.AskCount
		sessionStats.ApprovalStats.DeniedCount += sr.Approval.DenyCount
		sessionStats.ApprovalStats.TotalWaitTime += sr.Approval.WaitTime

		if sr.Approval.WaitTime > sessionStats.ApprovalStats.MaxWaitTime {
			sessionStats.ApprovalStats.MaxWaitTime = sr.Approval.WaitTime
		}
	}
}

// writeComposition reports what the prompt was made of, which is a different
// question from what it cost.
//
// The section is built around one comparison: tool output against conversation.
// Those two have different remedies — output can be evicted mechanically and
// re-fetched, conversation can only be summarised — so the share decides which
// mechanism a workload actually needs. Every other number here is context for
// reading that one.
//
// Absent entirely for a transcript that has none, rather than printed as zeros. A
// session recorded before the field existed did not measure this; saying so by
// omission beats claiming its prompt was empty.
func writeComposition(b *strings.Builder, c *session.Composition) {
	if c == nil || c.Estimated <= 0 {
		return
	}
	share := func(n int64) string {
		return fmt.Sprintf("%d (%.0f%%)", n, float64(n)*100/float64(c.Estimated))
	}

	b.WriteString("Context Composition (last recorded state, estimated):\n")
	b.WriteString(fmt.Sprintf("  Fixed (system prompt + tool schemas): %s\n", share(c.Fixed)))
	b.WriteString(fmt.Sprintf("  Tool results:                         %s\n", share(c.ToolTotal())))
	// Sorted by size: the question is which tool dominates, and alphabetical order
	// makes that a hunt through the list.
	names := make([]string, 0, len(c.Tools))
	for name := range c.Tools {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return c.Tools[names[i]] > c.Tools[names[j]] })
	for _, name := range names {
		b.WriteString(fmt.Sprintf("    %-34s %s\n", name, share(c.Tools[name])))
	}
	b.WriteString(fmt.Sprintf("  Tool call arguments:                  %s\n", share(c.ToolArgs)))
	b.WriteString(fmt.Sprintf("  Assistant text:                       %s\n", share(c.Assistant)))
	b.WriteString(fmt.Sprintf("  User text:                            %s\n", share(c.User)))
	b.WriteString(fmt.Sprintf("  Estimated total:                      %d over %d message(s)\n",
		c.Estimated, c.Messages))

	// Named when non-zero, because it is the difference between what the history
	// holds and what the provider was actually sent — without it the two numbers
	// below look inconsistent for a reason the reader cannot see.
	if c.Cleared > 0 {
		b.WriteString(fmt.Sprintf("  Cleared from the last prompt:         %s\n", share(c.Cleared)))
	}

	if ratio, ok := c.Calibration(); ok {
		// The direction is computed, not assumed. It was hardcoded as "low", which
		// prints "reads 0.83x low" for the common case — and 0.83 is what this
		// project's own transcripts actually give for Chinese-heavy sessions.
		direction := "high"
		if ratio > 1 {
			direction = "low"
		}
		b.WriteString(fmt.Sprintf("  Provider's own count:                 %d  (estimate reads %.2fx, i.e. %s)\n",
			c.Measured, ratio, direction))
		b.WriteString(fmt.Sprintf("\nthe shares are byte estimates at %d bytes/token; the provider's count is not.\n"+
			"that ratio is how wrong the divisor was for this session's text, and every\n"+
			"threshold anyone later picks in tokens is only as good as it. measured across\n"+
			"25 real sessions it runs 0.98 for ASCII and 0.83 for Chinese-heavy text, so on\n"+
			"these providers the estimate errs high — the safe direction, but not the one\n"+
			"this field was originally documented to expect.\n", session.BytesPerToken))
	}
	b.WriteString("\n")
}

// FormatText produces a human-readable text report.
func FormatText(stats *SessionStats) string {
	var builder strings.Builder

	builder.WriteString(fmt.Sprintf("Session Analysis: %s\n", stats.SessionPath))
	builder.WriteString(strings.Repeat("=", 80) + "\n\n")

	builder.WriteString(fmt.Sprintf("Session Overview:\n"))
	builder.WriteString(fmt.Sprintf("  Total Rounds: %d\n", stats.RoundCount))
	builder.WriteString(fmt.Sprintf("  Duration: %s\n", formatDuration(stats.Duration)))
	builder.WriteString(fmt.Sprintf("  Truncation Rate: %.2f%%\n\n", stats.TruncationStats.TruncationRate*100))

	builder.WriteString(fmt.Sprintf("Token Usage:\n"))
	builder.WriteString(fmt.Sprintf("  Input Tokens: %d (avg: %.0f, max: %d)\n",
		stats.TokenUsage.TotalInput, stats.TokenUsage.AverageInput, stats.TokenUsage.MaxInput))
	builder.WriteString(fmt.Sprintf("  Output Tokens: %d (avg: %.0f, max: %d)\n",
		stats.TokenUsage.TotalOutput, stats.TokenUsage.AverageOutput, stats.TokenUsage.MaxOutput))
	// Only when something was delegated. Printing "delegated: 0" on every session
	// would put a subagent-shaped question in front of everyone who never used one.
	if u := stats.TokenUsage; u.DelegatedInput > 0 || u.DelegatedOutput > 0 {
		builder.WriteString(fmt.Sprintf("    of which delegated to subagents: %d in, %d out\n",
			u.DelegatedInput, u.DelegatedOutput))
		builder.WriteString(fmt.Sprintf("    spent by this agent itself:      %d in, %d out\n",
			u.TotalInput-u.DelegatedInput, u.TotalOutput-u.DelegatedOutput))
	}
	if stats.TokenUsage.TotalCacheRead > 0 {
		builder.WriteString(fmt.Sprintf("  Cache Read Tokens: %d\n", stats.TokenUsage.TotalCacheRead))
	}
	if stats.TokenUsage.TotalReasoning > 0 {
		builder.WriteString(fmt.Sprintf("  Reasoning Tokens: %d\n", stats.TokenUsage.TotalReasoning))
	}
	builder.WriteString("\n")

	writeComposition(&builder, stats.Composition)

	builder.WriteString(fmt.Sprintf("Tool Call Distribution:\n"))
	builder.WriteString(fmt.Sprintf("  Total Calls: %d\n", stats.BatchSizes.TotalCalls))
	builder.WriteString(fmt.Sprintf("  Average per tool-calling turn: %.2f (>1 means batches are happening)\n",
		stats.BatchSizes.AverageCalls))
	builder.WriteString(fmt.Sprintf("  Singles (1 call): %d\n", stats.BatchSizes.Singles))
	builder.WriteString(fmt.Sprintf("  Doubles (2 calls): %d\n", stats.BatchSizes.Doubles))
	builder.WriteString(fmt.Sprintf("  Multiple (3+ calls): %d\n", stats.BatchSizes.Multiple))

	if len(stats.BatchSizes.BySize) > 0 {
		builder.WriteString(fmt.Sprintf("  By Size:\n"))
		sizes := make([]int, 0, len(stats.BatchSizes.BySize))
		for size := range stats.BatchSizes.BySize {
			sizes = append(sizes, size)
		}
		sort.Ints(sizes)
		for _, size := range sizes {
			builder.WriteString(fmt.Sprintf("    %d calls: %d turns\n", size, stats.BatchSizes.BySize[size]))
		}
	}
	// Sorted by frequency: the question is which batch shape dominates, and
	// alphabetical order makes that a hunt.
	if len(stats.BatchSizes.Combos) > 0 {
		builder.WriteString(fmt.Sprintf("  By Tool Set:\n"))
		combos := make([]string, 0, len(stats.BatchSizes.Combos))
		for c := range stats.BatchSizes.Combos {
			combos = append(combos, c)
		}
		sort.Slice(combos, func(i, j int) bool {
			if a, b := stats.BatchSizes.Combos[combos[i]], stats.BatchSizes.Combos[combos[j]]; a != b {
				return a > b
			}
			return combos[i] < combos[j]
		})
		for _, c := range combos {
			builder.WriteString(fmt.Sprintf("    %-28s %d turn(s)\n", c, stats.BatchSizes.Combos[c]))
		}
	}
	builder.WriteString(fmt.Sprintf("  Serialized by a sequential sibling: %d call(s)\n", stats.BatchSizes.Stalled))
	builder.WriteString("\n")

	builder.WriteString(fmt.Sprintf("Tool Timing:\n"))
	if stats.ToolTiming.TotalCalls > 0 {
		builder.WriteString(fmt.Sprintf("  Total Calls: %d\n", stats.ToolTiming.TotalCalls))
		builder.WriteString(fmt.Sprintf("  Total Duration: %s\n", formatDuration(stats.ToolTiming.TotalDuration)))
		builder.WriteString(fmt.Sprintf("  Average Duration: %s\n", formatDuration(int64(stats.ToolTiming.AverageDuration))))
		builder.WriteString(fmt.Sprintf("  Max Duration: %s\n", formatDuration(stats.ToolTiming.MaxDuration)))

		if len(stats.ToolTiming.ByTool) > 0 {
			builder.WriteString(fmt.Sprintf("  By Tool:\n"))
			tools := make([]string, 0, len(stats.ToolTiming.ByTool))
			for name := range stats.ToolTiming.ByTool {
				tools = append(tools, name)
			}
			sort.Strings(tools)

			for _, name := range tools {
				tool := stats.ToolTiming.ByTool[name]
				builder.WriteString(fmt.Sprintf("    %s:\n", name))
				builder.WriteString(fmt.Sprintf("      Calls: %d\n", tool.CallCount))
				builder.WriteString(fmt.Sprintf("      Total: %s\n", formatDuration(tool.TotalDuration)))
				builder.WriteString(fmt.Sprintf("      Average: %s\n", formatDuration(int64(tool.AverageDuration))))
				if tool.ErrorCount > 0 {
					builder.WriteString(fmt.Sprintf("      Errors: %d\n", tool.ErrorCount))
				}
			}
		}
	} else {
		builder.WriteString(fmt.Sprintf("  No tool calls recorded\n"))
	}
	builder.WriteString("\n")

	if stats.RetryStats.TotalRetries > 0 {
		builder.WriteString(fmt.Sprintf("Retry Statistics:\n"))
		builder.WriteString(fmt.Sprintf("  Total Retries: %d\n", stats.RetryStats.TotalRetries))
		if len(stats.RetryStats.Reasons) > 0 {
			builder.WriteString(fmt.Sprintf("  Reasons:\n"))
			for reason, count := range stats.RetryStats.Reasons {
				builder.WriteString(fmt.Sprintf("    %s: %d\n", reason, count))
			}
		}
		builder.WriteString("\n")
	}

	if stats.ApprovalStats.TotalRequests > 0 {
		builder.WriteString(fmt.Sprintf("Approval Gate Statistics:\n"))
		builder.WriteString(fmt.Sprintf("  Total Requests: %d\n", stats.ApprovalStats.TotalRequests))
		builder.WriteString(fmt.Sprintf("  Denied: %d (%.1f%%)\n",
			stats.ApprovalStats.DeniedCount,
			float64(stats.ApprovalStats.DeniedCount)/float64(stats.ApprovalStats.TotalRequests)*100))
		builder.WriteString(fmt.Sprintf("  Total Wait Time: %s\n", formatDuration(stats.ApprovalStats.TotalWaitTime)))
		builder.WriteString(fmt.Sprintf("  Average Wait Time: %s\n", formatDuration(int64(stats.ApprovalStats.AverageWaitTime))))
		builder.WriteString(fmt.Sprintf("  Max Wait Time: %s\n", formatDuration(stats.ApprovalStats.MaxWaitTime)))
		builder.WriteString("\n")
	}

	return builder.String()
}

func formatDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	seconds := ms / 1000
	if seconds < 60 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	minutes := seconds / 60
	secondsRem := seconds % 60
	return fmt.Sprintf("%dm%ds", minutes, secondsRem)
}

// FormatJSON produces a JSON representation of the stats.
func FormatJSON(stats *SessionStats) (string, error) {
	b, err := json.MarshalIndent(stats, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal stats to JSON: %w", err)
	}
	return string(b), nil
}
