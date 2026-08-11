package llm

import "encoding/json"

// The OpenAI chat-completions shapes we actually use, plus the two extensions
// both target providers rely on. Hand-written rather than pulled from an SDK:
// `reasoning_content` is not part of the official schema, and it is the field
// that carries all of the thinking output for GLM and Kimi.

type wireRequest struct {
	Model         string        `json:"model"`
	Messages      []wireMessage `json:"messages"`
	Tools         []wireTool    `json:"tools,omitempty"`
	MaxTokens     int64         `json:"max_tokens,omitempty"`
	Temperature   *float64      `json:"temperature,omitempty"`
	Stream        bool          `json:"stream"`
	StreamOptions *streamOpts   `json:"stream_options,omitempty"`
}

type streamOpts struct {
	// Without this, neither provider reports token usage on a streamed call.
	IncludeUsage bool `json:"include_usage"`
}

type wireMessage struct {
	Role    string `json:"role"` // system | user | assistant | tool
	Content string `json:"content,omitempty"`
	// Assistant turns that called tools.
	ToolCalls []wireToolCall `json:"tool_calls,omitempty"`
	// Tool result turns.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // always "function"
	Index    *int         `json:"index,omitempty"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name string `json:"name,omitempty"`
	// Arguments is a JSON *string*, not an object. Streamed in fragments.
	Arguments string `json:"arguments,omitempty"`
}

type wireTool struct {
	Type     string         `json:"type"` // always "function"
	Function wireToolSchema `json:"function"`
}

type wireToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

// --- streaming response ---

type wireChunk struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content string `json:"content"`
			// ReasoningContent is the GLM / Kimi thinking channel.
			ReasoningContent string         `json:"reasoning_content"`
			ToolCalls        []wireToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *wireUsage    `json:"usage"`
	Error *wireAPIError `json:"error"`
}

type wireUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type wireAPIError struct {
	Message string          `json:"message"`
	Type    string          `json:"type"`
	Code    json.RawMessage `json:"code"`
}

func (e *wireAPIError) Error() string {
	if e.Type != "" {
		return e.Type + ": " + e.Message
	}
	return e.Message
}
