package session

import "github.com/yosukeno/pi-go/llm"

// BytesPerToken is re-exported so a reader of this file and of the report it
// produces does not have to know which package owns the divisor. It is llm's,
// because llm owns the wire format that decides what an estimate may count.
const BytesPerToken = llm.BytesPerToken

// Composition estimates what a prompt is made of.
//
// It exists to answer one question that no counter in this project could answer
// before: the gauges say how full the context is, and Usage says what it cost,
// but neither says *what the tokens are*. Those have different remedies — history
// dominated by tool output can be evicted mechanically, while history dominated by
// conversation can only be summarised — so choosing between them without this
// measurement is guessing.
//
// # This is a snapshot, not a delta
//
// Every other number in Stats is the increment since the last record, because the
// analyzer sums them. These are not: each one describes the whole history as it
// stood when the record was written, so the newest record is the answer and adding
// them across records is meaningless. Stated loudly because this file's neighbours
// established the opposite convention, and because it is the third arithmetic trap
// in this codebase of the same shape — see Usage.CacheRead (a subset of Input) and
// Stats.Delegated (a subset of Usage).
//
// # Estimated is estimated, Measured is measured
//
// The byte counts mirror what llm/convert.go actually puts on the wire, field by
// field: thinking blocks are excluded because they are never replayed, and Details
// is excluded because it never reaches a provider at all. What is not modelled is
// the wire envelope — role names, JSON punctuation, the tool-call wrapper — so
// Estimated reads low by a few percent before the tokenizer ratio is even
// considered. Use the shares; take the totals from Measured.
type Composition struct {
	// Fixed is the system prompt plus tool schemas: the part that is resent every
	// turn no matter how the conversation goes. Supplied by the caller, since it
	// is not in the messages.
	Fixed int64 `json:"fixed"`
	// User is text the human wrote, the one category that must never be evicted.
	User int64 `json:"user,omitempty"`
	// Assistant is the model's own prose.
	Assistant int64 `json:"assistant,omitempty"`
	// ToolArgs is the arguments of tool calls — small for a read, most of a message
	// for a write.
	ToolArgs int64 `json:"tool_args,omitempty"`
	// Tools is tool_result size by tool name, and it is the field the whole record
	// was built for: "read" against "assistant" is the eviction-versus-compaction
	// decision, stated in numbers.
	//
	// The name is recovered by pairing each result back to its tool_use, because a
	// result block carries only the id — the same back-fill web.Hub.Seed does.
	Tools map[string]int64 `json:"tools,omitempty"`
	// Estimated is Fixed plus everything above it, in estimated tokens.
	Estimated int64 `json:"estimated"`
	// Measured is the provider's own count for the most recent turn's prompt, or 0
	// when it is not known. The one authoritative number here.
	Measured int64 `json:"measured,omitempty"`
	// Cleared is how much of Estimated was not in the prompt Measured counted,
	// because context editing had blanked it.
	//
	// Without this the two numbers are not comparable and Calibration is not a
	// tokenizer ratio at all: Estimated covers the whole history, while Measured
	// counted a prompt from which old tool results and payload arguments had been
	// removed. A session where clearing fired would report the estimate as wildly
	// high and the reader would blame the divisor.
	Cleared int64 `json:"cleared,omitempty"`
	// Messages is how many messages the history holds, so a share can be read
	// against the length of the conversation that produced it.
	Messages int `json:"messages"`
}

// unknownTool is the bucket for a result whose tool_use did not survive. A
// damaged transcript should still be measurable, and silently dropping the bytes
// would make the shares add up to less than the whole for no visible reason.
const unknownTool = "(unknown)"

// Compose estimates the composition of a history. fixed is the per-turn fixed
// cost in tokens, which the caller has because it lives on the agent rather than
// in the messages (agent.OverheadTokens).
//
// A pure function over the transcript: no state, no clock, no I/O. Both front ends
// call it at the same moment — after a run, on the goroutine that owns the
// messages — which is the only time reading them is safe.
func Compose(msgs []llm.Message, fixed int64) Composition {
	c := Composition{Fixed: fixed, Messages: len(msgs)}

	// One pass to learn which tool produced which result. Results arrive in the
	// message after the call, so a single forward pass would already have the name
	// by the time it is needed; the map is built separately anyway because a
	// repaired transcript can interleave them.
	names := make(map[string]string)
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolUse {
				names[b.ID] = b.Name
			}
		}
	}

	var bytes struct {
		user, assistant, args int64
		tools                 map[string]int64
	}
	bytes.tools = make(map[string]int64)

	for _, m := range msgs {
		for _, b := range m.Content {
			switch b.Type {
			case llm.BlockText:
				if m.Role == llm.RoleUser {
					bytes.user += int64(len(b.Text))
				} else {
					bytes.assistant += int64(len(b.Text))
				}
			case llm.BlockToolUse:
				// The name rides along on every request beside the arguments, so it
				// is counted here rather than being treated as free.
				bytes.args += int64(len(b.Input)) + int64(len(b.Name))
			case llm.BlockToolResult:
				name := names[b.ToolUseID]
				if name == "" {
					name = unknownTool
				}
				bytes.tools[name] += int64(len(b.Text))
			case llm.BlockThinking:
				// Deliberately uncounted: convert.go does not replay thinking, so
				// these bytes are not in any prompt. Counting them would put the
				// estimate permanently above the measured total and make the
				// calibration useless. What they cost is already in Usage.Reasoning.
			}
		}
	}

	c.User = tokensOf(bytes.user)
	c.Assistant = tokensOf(bytes.assistant)
	c.ToolArgs = tokensOf(bytes.args)
	if len(bytes.tools) > 0 {
		c.Tools = make(map[string]int64, len(bytes.tools))
		for name, n := range bytes.tools {
			c.Tools[name] = tokensOf(n)
		}
	}

	// Summed from the rounded parts rather than from the raw bytes, so the parts
	// visibly add up to the total. A total that disagreed with its own breakdown by
	// a few tokens is the kind of thing a reader spends ten minutes on.
	c.Estimated = c.Fixed + c.User + c.Assistant + c.ToolArgs
	for _, n := range c.Tools {
		c.Estimated += n
	}
	return c
}

// tokensOf converts bytes to the project's estimated tokens.
func tokensOf(b int64) int64 { return b / BytesPerToken }

// ToolTotal is the estimated tokens held by every tool result together — the
// number to compare against Assistant when deciding whether this workload's
// context problem is tool output or conversation.
func (c Composition) ToolTotal() int64 {
	var n int64
	for _, v := range c.Tools {
		n += v
	}
	return n
}

// Calibration is how far off BytesPerToken is for the text this session actually
// contained: the provider's count for the last prompt, over the estimate of what
// that prompt held. It reports false when there is no measurement to calibrate
// against, or when clearing removed more than the estimate can account for.
//
// Cleared is subtracted rather than ignored, because Estimated describes the whole
// history while Measured counted a prompt that clearing had already shrunk. Two
// numbers over different content are not a ratio.
//
// # Measured direction, against this project's own transcripts
//
// The expectation written here originally was that the ratio sits above 1 — that
// four bytes per token reads low, and much more so for Chinese, "off by roughly a
// factor of two and a half". Recovering the ratio from 25 real multi-turn sessions
// says the opposite for both of pi-go's providers:
//
//	mostly ASCII prompts   median 0.98   (n=9)
//	CJK-heavy prompts       median 0.83   (n=11, lowest 0.60 at 92% non-ASCII)
//	overall                 median 0.97   spread 0.51 – 1.11
//
// The estimate reads slightly *high*, and highest for Chinese. Both endpoints run
// tokenizers trained on Chinese, where a two-character word — six bytes — is often
// one token, so the real divisor for that text is nearer six than four. Four bytes
// per token is the conservative choice for these providers rather than the
// dangerous one, which is worth knowing before anyone tightens a threshold on the
// assumption that the estimate understates.
//
// One methodological trap, recorded because it cost a full pass to find: a
// delegating session's usage counters include the subagent's tokens, so comparing
// them against an estimate of the parent's own history reports the divisor as being
// off by two orders of magnitude. Measured here is LastInput — one of the parent's
// own responses — so the shipped number does not have that problem, but any probe
// that sums usage records does.
func (c Composition) Calibration() (float64, bool) {
	sent := c.Estimated - c.Cleared
	if c.Measured <= 0 || sent <= 0 {
		return 0, false
	}
	return float64(c.Measured) / float64(sent), true
}
