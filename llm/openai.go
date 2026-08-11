package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIClient speaks the OpenAI chat-completions protocol, which every provider
// worth targeting now implements. It is hand-written on net/http instead of
// wrapping an SDK: the two fields that matter most here, `reasoning_content` and
// `prompt_tokens_details.cached_tokens`, are outside the official schema, and
// fighting a generated client for them costs more than the 200 lines below.
type OpenAIClient struct {
	http       *http.Client
	baseURL    string
	apiKey     string
	model      string
	maxTokens  int64
	maxRetries int
	onRetry    func(RetryInfo)
}

type Options struct {
	BaseURL   string
	APIKey    string
	Model     string
	MaxTokens int64
	// Timeout bounds a single request. Streaming a long agent turn is slow, so
	// this is generous; cancellation is the real stop mechanism.
	Timeout time.Duration
	// MaxRetries counts retries, not attempts. Negative disables them.
	MaxRetries int
	// OnRetry, if set, is called before each backoff so a UI can say why it is
	// waiting instead of looking frozen.
	OnRetry func(RetryInfo)
}

func New(o Options) *OpenAIClient {
	if o.MaxTokens == 0 {
		o.MaxTokens = 16384
	}
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Minute
	}
	if o.MaxRetries == 0 {
		o.MaxRetries = DefaultMaxRetries
	}
	if o.MaxRetries < 0 {
		o.MaxRetries = 0
	}
	return &OpenAIClient{
		http:       &http.Client{Timeout: o.Timeout},
		baseURL:    strings.TrimSuffix(o.BaseURL, "/"),
		apiKey:     o.APIKey,
		model:      o.Model,
		maxTokens:  o.MaxTokens,
		maxRetries: o.MaxRetries,
		onRetry:    o.OnRetry,
	}
}

func (c *OpenAIClient) Model() string { return c.model }

// Stream performs one chat-completions call and invokes onDelta as content
// arrives. A transport or API failure returns an error; a normal stop or an
// over-length response comes back as a Response with the matching StopReason.
//
// Retries happen here rather than in the agent loop because only this layer can
// tell a transient failure from a real answer, and only this layer knows whether
// any output has already escaped to the caller.
func (c *OpenAIClient) Stream(
	ctx context.Context,
	systemPrompt string,
	history []Message,
	tools []ToolSchema,
	onDelta func(Delta),
) (Response, error) {
	body, err := json.Marshal(wireRequest{
		Model:         c.model,
		Messages:      toWireMessages(systemPrompt, history),
		Tools:         toWireTools(tools),
		MaxTokens:     c.maxTokens,
		Stream:        true,
		StreamOptions: &streamOpts{IncludeUsage: true},
	})
	if err != nil {
		return Response{}, err
	}

	// dirty is the retry guard. Once a delta has reached the caller it has been
	// printed, so replaying the request would duplicate visible output. This is
	// the one thing an HTTP-level retry in a generic SDK cannot get right for a
	// streaming caller.
	dirty := false
	guarded := func(d Delta) {
		dirty = true
		if onDelta != nil {
			onDelta(d)
		}
	}

	// callStart covers the whole call including retries; attemptStart is reset per
	// attempt, because a TTFT that included a failed attempt plus its backoff
	// would describe the retry policy rather than the model.
	callStart := time.Now()

	var lastErr error
	for attempt := 0; ; attempt++ {
		attemptStart := time.Now()
		resp, err := c.post(ctx, body)
		ttfb := time.Since(attemptStart)

		var (
			retry  bool
			delay  time.Duration
			reason string
		)
		switch {
		case err != nil:
			// No response at all: DNS, dial, TLS, or a dropped connection.
			if ctx.Err() != nil {
				return Response{StopReason: StopAborted, Timing: Timing{TTFB: ttfb, Total: time.Since(callStart)}}, nil
			}
			lastErr, retry, delay, reason = err, true, retryDelay(nil, attempt), err.Error()

		case resp.StatusCode != http.StatusOK:
			apiErr := apiError(resp)
			retry = shouldRetryResponse(resp)
			delay = retryDelay(resp, attempt)
			resp.Body.Close()
			lastErr, reason = apiErr, apiErr.Error()

		default:
			out, streamErr := c.readStream(ctx, resp.Body, guarded, attemptStart)
			resp.Body.Close()
			out.Timing.TTFB = ttfb
			out.Timing.Total = time.Since(callStart)
			if streamErr == nil {
				return out, nil
			}
			if ctx.Err() != nil {
				return Response{StopReason: StopAborted, Timing: out.Timing}, nil
			}
			lastErr, retry, delay = streamErr, !dirty, retryDelay(nil, attempt)
			reason = streamErr.Error()
		}

		if !retry {
			return Response{}, lastErr
		}
		if attempt >= c.maxRetries {
			return Response{}, fmt.Errorf("after %d attempts: %w", attempt+1, lastErr)
		}
		if c.onRetry != nil {
			c.onRetry(RetryInfo{Attempt: attempt + 1, Max: c.maxRetries, Delay: delay, Reason: reason})
		}
		if err := sleep(ctx, delay); err != nil {
			return Response{StopReason: StopAborted}, nil
		}
	}
}

// post issues one attempt. The body is a []byte so every retry can rebuild the
// reader; a stream-only body would make the request unrepeatable.
func (c *OpenAIClient) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// A credential-less provider (a local server, say) gets no header at all
	// rather than a "Bearer " some endpoints would trip on.
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	req.Header.Set("Accept", "text/event-stream")
	return c.http.Do(req)
}

// apiError turns a non-200 into something a human can act on, preferring the
// provider's own error message over the bare status code.
//
// It returns an *APIError rather than a formatted string so a caller can ask what
// kind of failure this was. The string was all there used to be, which meant the
// only way to tell "the prompt is too long" from "the key is wrong" — two 400s
// needing opposite responses — was to match on prose.
func apiError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
	e := &APIError{Status: resp.StatusCode, StatusText: resp.Status}
	var env struct {
		Error *wireAPIError `json:"error"`
	}
	if json.Unmarshal(raw, &env) == nil && env.Error != nil && env.Error.Message != "" {
		e.Type, e.Message = env.Error.Type, env.Error.Message
		return e
	}
	e.Message = strings.TrimSpace(string(raw))
	return e
}

// readStream consumes the SSE body. Two things need accumulating: text and
// thinking are plain appends, but tool calls arrive as fragments keyed by index,
// with the name in the first fragment and the arguments spread across the rest.
func (c *OpenAIClient) readStream(
	ctx context.Context, body io.Reader, onDelta func(Delta), attemptStart time.Time,
) (Response, error) {
	var (
		text     strings.Builder
		thinking strings.Builder
		usage    Usage
		finish   string
		// calls is indexed by the provider's tool_call index, which is stable
		// within a response but not necessarily contiguous.
		calls = map[int]*toolCallAccum{}
		order []int
		// ttft is stamped by the first content of any kind. Providers routinely
		// open with a role-only delta and send keep-alives before that, so the
		// stamp has to come from content rather than from the first frame.
		ttft time.Duration
	)
	markFirstToken := func() {
		if ttft == 0 {
			ttft = time.Since(attemptStart)
		}
	}

	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if ctx.Err() != nil {
			return Response{StopReason: StopAborted, Timing: Timing{TTFT: ttft}}, nil
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue // comments, keep-alives, and the event: lines nobody sends
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}

		var chunk wireChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			// A malformed frame mid-stream is not worth killing the turn over.
			continue
		}
		if chunk.Error != nil {
			return Response{}, chunk.Error
		}
		if chunk.Usage != nil {
			usage = Usage{
				Input:     chunk.Usage.PromptTokens,
				Output:    chunk.Usage.CompletionTokens,
				CacheRead: chunk.Usage.PromptTokensDetails.CachedTokens,
				Reasoning: chunk.Usage.CompletionTokensDetails.ReasoningTokens,
			}
		}
		if len(chunk.Choices) == 0 {
			continue // usage-only final chunk
		}
		ch := chunk.Choices[0]
		if ch.FinishReason != "" {
			finish = ch.FinishReason
		}
		if ch.Delta.ReasoningContent != "" {
			markFirstToken()
			thinking.WriteString(ch.Delta.ReasoningContent)
			emit(onDelta, Delta{Kind: DeltaThinking, Text: ch.Delta.ReasoningContent})
		}
		if ch.Delta.Content != "" {
			markFirstToken()
			text.WriteString(ch.Delta.Content)
			emit(onDelta, Delta{Kind: DeltaText, Text: ch.Delta.Content})
		}
		for _, tc := range ch.Delta.ToolCalls {
			idx := 0
			if tc.Index != nil {
				idx = *tc.Index
			}
			acc, ok := calls[idx]
			if !ok {
				acc = &toolCallAccum{}
				calls[idx] = acc
				order = append(order, idx)
			}
			if tc.ID != "" {
				acc.id = tc.ID
			}
			if tc.Function.Name != "" {
				// A turn that goes straight to a tool call with no preamble still
				// had a first token; not stamping it here would report zero for
				// exactly the turns that feel fastest.
				markFirstToken()
				acc.name = tc.Function.Name
				emit(onDelta, Delta{Kind: DeltaToolCallStart, ToolName: acc.name, ToolID: acc.id})
			}
			acc.args.WriteString(tc.Function.Arguments)
			if tc.Function.Arguments != "" && acc.id != "" {
				// Forwarded so a UI can preview a large argument as it streams in
				// rather than sitting silent until the call settles. The id guard is
				// belt and braces: OpenAI sends the id in the call's first chunk,
				// ahead of any arguments. No first-token stamp — the name chunk
				// above already took it.
				emit(onDelta, Delta{Kind: DeltaToolCallArgs, ToolID: acc.id, Text: tc.Function.Arguments})
			}
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return Response{StopReason: StopAborted, Timing: Timing{TTFT: ttft}}, nil
		}
		// The timing rides along even on failure: Stream needs it to attribute a
		// mid-stream error to a slow start rather than a slow stream.
		return Response{Timing: Timing{TTFT: ttft}}, err
	}

	msg := Message{Role: RoleAssistant}
	if thinking.Len() > 0 {
		msg.Content = append(msg.Content, Block{Type: BlockThinking, Text: thinking.String()})
	}
	if text.Len() > 0 {
		msg.Content = append(msg.Content, Block{Type: BlockText, Text: text.String()})
	}
	for _, idx := range order {
		acc := calls[idx]
		if acc.name == "" {
			continue
		}
		msg.Content = append(msg.Content, Block{
			Type:  BlockToolUse,
			ID:    acc.id,
			Name:  acc.name,
			Input: acc.arguments(),
		})
	}

	return Response{
		Message: msg, StopReason: stopReason(finish), Usage: usage,
		Timing: Timing{TTFT: ttft},
	}, nil
}

type toolCallAccum struct {
	id   string
	name string
	args strings.Builder
}

// arguments returns the accumulated JSON, substituting an empty object when the
// model sent no arguments at all. Malformed JSON is passed through untouched so
// the tool's own decoder produces the error the model gets to read.
func (a *toolCallAccum) arguments() json.RawMessage {
	s := strings.TrimSpace(a.args.String())
	if s == "" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(s)
}

func emit(onDelta func(Delta), d Delta) {
	if onDelta != nil {
		onDelta(d)
	}
}

func stopReason(finish string) StopReason {
	switch finish {
	case "tool_calls", "function_call":
		return StopToolUse
	case "length":
		return StopMaxTokens
	default:
		// stop, content_filter, or empty. The loop decides what to do next by
		// looking for tool calls, so anything else means "this turn is over".
		return StopEndTurn
	}
}
