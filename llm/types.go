// Package llm is the only place that knows about the wire protocol.
// Everything above it works with the neutral types declared here, mirroring pi's
// split between packages/ai and the agent harness.
package llm

import (
	"context"
	"encoding/json"
	"time"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// BlockType enumerates the content blocks we care about.
type BlockType string

const (
	BlockText       BlockType = "text"
	BlockThinking   BlockType = "thinking"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
)

// Block is a single piece of message content. The flat shape keeps JSONL session
// records easy to read and diff.
type Block struct {
	Type BlockType `json:"type"`

	// Text / Thinking
	Text string `json:"text,omitempty"`

	// ToolUse
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// ToolResult
	ToolUseID string `json:"tool_use_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	// Details is a tool's structured payload (tools.EditDetails and friends) in
	// its marshalled form. It is carried here so that it reaches the session file,
	// and it is the one field on this type that must never reach a provider.
	//
	// That it cannot is a property of convert.go, not a rule someone has to
	// remember: toWireMessages builds each wire struct field by field, so a field
	// it does not mention has no path to the API. TestDetailsNeverReachTheWire
	// pins it.
	//
	// Why it lives on the wire-shaped type at all: pi keeps two message types, a
	// rich persisted one and a lean protocol one, converting between them. That is
	// the cleaner design and also the more expensive one — it would mean a second
	// type threaded through the agent, the session and the Hub. One field plus one
	// test buys the same user-visible result: a diff that is still there after a
	// reload.
	Details json.RawMessage `json:"details,omitempty"`
}

// Message keeps a block list even though the OpenAI wire format is flatter.
// Grouping all of one turn's tool results into a single message is what lets the
// loop stay protocol-agnostic; the expansion into role:"tool" entries happens at
// the boundary in openai.go.
type Message struct {
	Role    Role    `json:"role"`
	Content []Block `json:"content"`
}

// UserText builds a plain user message.
func UserText(text string) Message {
	return Message{Role: RoleUser, Content: []Block{{Type: BlockText, Text: text}}}
}

// ToolCalls returns the tool_use blocks of an assistant message.
func (m Message) ToolCalls() []Block {
	var out []Block
	for _, b := range m.Content {
		if b.Type == BlockToolUse {
			out = append(out, b)
		}
	}
	return out
}

// Text concatenates all text blocks, which is what the -p mode prints.
func (m Message) Text() string {
	s := ""
	for _, b := range m.Content {
		if b.Type == BlockText {
			s += b.Text
		}
	}
	return s
}

// StopReason is the subset of finish reasons the loop branches on.
type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopAborted   StopReason = "aborted"
	StopError     StopReason = "error"
)

// BytesPerToken is the divisor every size estimate in this project uses.
//
// It lives here, beside convert.go, because that file defines what actually goes
// on the wire and therefore what an estimate is allowed to count: thinking blocks
// are not replayed and Block.Details cannot reach a provider at all, so neither
// belongs in any estimate. Two packages estimate sizes for different purposes —
// session.Compose for the record, the agent for its context-edit trigger — and a
// divisor that differed between them would make their numbers incomparable rather
// than merely imprecise.
//
// Four is roughly right for English and off by about two and a half times for
// Chinese. Nothing derived from it should be trusted beyond the ratio that
// session.Composition.Calibration reports against the provider's own count.
const BytesPerToken = 4

// EstimateTokens is the estimated prompt cost of some messages, counting only what
// convert.go actually sends. Used for the parts of a prompt no provider has
// measured yet — see Agent.promptTokens, which adds this to the last measured
// total rather than estimating the whole history.
func EstimateTokens(msgs []Message) int64 {
	var b int64
	for _, m := range msgs {
		for _, blk := range m.Content {
			switch blk.Type {
			case BlockText, BlockToolResult:
				b += int64(len(blk.Text))
			case BlockToolUse:
				b += int64(len(blk.Input)) + int64(len(blk.Name))
			case BlockThinking:
				// Not replayed; see convert.go.
			}
		}
	}
	return b / BytesPerToken
}

// Usage is one call's token accounting, exactly as the provider reported it.
// Every field is a count of tokens.
//
// The fields are not disjoint, and that trips people up: Input is the whole
// prompt the provider billed for, and CacheRead is the part of that prompt which
// came from the cached prefix. So CacheRead <= Input, and the fresh part of the
// prompt is Input - CacheRead. Adding them together double-counts.
//
// Reasoning is the opposite arrangement for the completion side: on providers
// that report it, the reasoning tokens are already inside Output.
type Usage struct {
	// Input is prompt tokens: system prompt + tool schemas + the whole
	// conversation so far, resent on every turn.
	Input int64 `json:"input"`
	// Output is completion tokens, reasoning included where the provider
	// reports it separately below.
	Output int64 `json:"output"`
	// CacheRead is the subset of Input served from the provider's cached prefix,
	// billed at a lower rate. Not an addition to Input.
	CacheRead int64 `json:"cache_read,omitempty"`
	// Reasoning is the subset of Output spent on thinking. Not an addition to
	// Output.
	Reasoning int64 `json:"reasoning,omitempty"`
}

// FreshInput is the part of the prompt that was not served from cache. This is
// the number that moves when the prompt prefix changes, so it is the one worth
// looking at when a turn suddenly costs more than the one before it.
func (u Usage) FreshInput() int64 {
	if u.CacheRead > u.Input {
		// Defensive: a provider that reports cached_tokens against a prompt total
		// it computed differently would otherwise produce a negative number.
		return 0
	}
	return u.Input - u.CacheRead
}

// Timing is where one streaming call spent its wall clock.
//
// It exists because a slow turn has three quite different causes and they are
// indistinguishable from the outside: a slow connection, a model that takes a
// long time to start, or a model that streams slowly once it has started. TTFB
// and TTFT separate the first two; Total minus TTFT is the third.
type Timing struct {
	// TTFB is request start to response headers: DNS, dial, TLS, and however
	// long the provider queued the request.
	TTFB time.Duration `json:"ttfb"`
	// TTFT is request start to the first content delta — thinking, text, or a
	// tool call name. This is the wait a user actually perceives, and on a
	// reasoning model it is usually most of it.
	//
	// Zero when the call produced no content at all, which is why the aggregate
	// in the agent counts calls rather than dividing by turns.
	TTFT time.Duration `json:"ttft"`
	// Total is the whole call as the caller experienced it, including any
	// retries and their backoff. TTFB and TTFT are measured against the final
	// attempt, so Total can be much larger than either.
	Total time.Duration `json:"total"`
}

// ToolSchema is the tool declaration sent to the model.
type ToolSchema struct {
	Name        string
	Description string
	// InputSchema is a hand-written JSON Schema object. No reflection: four
	// literals are cheaper to read than a generator.
	InputSchema map[string]any
}

// DeltaKind describes an incremental update during streaming.
type DeltaKind string

const (
	DeltaText          DeltaKind = "text"
	DeltaThinking      DeltaKind = "thinking"
	DeltaToolCallStart DeltaKind = "tool_call_start"
	// DeltaToolCallArgs carries one fragment of a tool call's arguments while the
	// model is still generating them: the raw text rides in Text, the call's
	// identity in ToolID. It exists so a UI can preview a large argument as it
	// streams in — the settled arguments still arrive with the response itself.
	DeltaToolCallArgs DeltaKind = "tool_call_args"
)

// Delta is what the client streams out while a response is being produced.
// The agent loop forwards these as events; it never inspects partial state.
type Delta struct {
	Kind     DeltaKind
	Text     string
	ToolName string
	ToolID   string
}

// Response is the settled result of one LLM call.
type Response struct {
	Message    Message
	StopReason StopReason
	Usage      Usage
	// Timing is filled in by the client for every outcome, including an abort:
	// how long a cancelled turn spent waiting is worth knowing too.
	Timing Timing
}

// Client is what the agent loop depends on. Keeping it an interface is what
// makes swapping models at runtime a pointer assignment.
type Client interface {
	Model() string
	Stream(ctx context.Context, systemPrompt string, history []Message, tools []ToolSchema, onDelta func(Delta)) (Response, error)
}
