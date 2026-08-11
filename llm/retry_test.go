package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const okStream = `data: {"choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2}}

data: [DONE]

`

func newTestClient(t *testing.T, h http.HandlerFunc) (*OpenAIClient, *[]RetryInfo) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	var seen []RetryInfo
	c := New(Options{
		BaseURL: srv.URL, APIKey: "test", Model: "m", MaxRetries: 3,
		OnRetry: func(r RetryInfo) { seen = append(seen, r) },
	})
	return c, &seen
}

func TestRetriesOn429AndHonorsRetryAfter(t *testing.T) {
	var attempts int
	c, seen := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.Header().Set("Retry-After-Ms", "20")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, okStream)
	})

	resp, err := c.Stream(context.Background(), "sys", []Message{UserText("hi")}, nil, nil)
	if err != nil {
		t.Fatalf("want success after retries, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	if got := resp.Message.Text(); got != "hello" {
		t.Errorf("text = %q, want %q", got, "hello")
	}
	if len(*seen) != 2 {
		t.Fatalf("OnRetry calls = %d, want 2", len(*seen))
	}
	for _, r := range *seen {
		if r.Delay > 100*time.Millisecond {
			t.Errorf("delay %v ignores Retry-After-Ms: 20", r.Delay)
		}
	}
}

func TestNoRetryOn401(t *testing.T) {
	var attempts int
	c, seen := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"bad key","type":"authentication_error"}}`)
	})
	_, err := c.Stream(context.Background(), "", []Message{UserText("hi")}, nil, nil)
	if err == nil {
		t.Fatal("want error")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (401 is permanent)", attempts)
	}
	if len(*seen) != 0 {
		t.Errorf("should not announce retries for 401")
	}
	if !strings.Contains(err.Error(), "bad key") {
		t.Errorf("error should surface the provider message, got %v", err)
	}
}

func TestServerErrorHeaderOverride(t *testing.T) {
	var attempts int
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("x-should-retry", "false")
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err := c.Stream(context.Background(), "", []Message{UserText("hi")}, nil, nil); err == nil {
		t.Fatal("want error")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (x-should-retry: false)", attempts)
	}
}

// A stream that dies after emitting output must not be replayed: the caller has
// already printed those deltas.
func TestNoRetryAfterOutputEmitted(t *testing.T) {
	var attempts int
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
		// Terminate without [DONE]. The reader sees EOF mid-stream.
		if h, ok := w.(http.Hijacker); ok {
			conn, _, _ := h.Hijack()
			conn.Close()
		}
	})

	var got strings.Builder
	_, _ = c.Stream(context.Background(), "", []Message{UserText("hi")}, nil,
		func(d Delta) { got.WriteString(d.Text) })

	if attempts != 1 {
		t.Errorf("attempts = %d, want 1: retrying would duplicate %q", attempts, got.String())
	}
	if strings.Count(got.String(), "partial") > 1 {
		t.Errorf("output duplicated: %q", got.String())
	}
}

func TestExhaustsRetriesThenReports(t *testing.T) {
	var attempts int
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Retry-After-Ms", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	_, err := c.Stream(context.Background(), "", []Message{UserText("hi")}, nil, nil)
	if err == nil {
		t.Fatal("want error")
	}
	if attempts != 4 {
		t.Errorf("attempts = %d, want 4 (1 + 3 retries)", attempts)
	}
	if !strings.Contains(err.Error(), "after 4 attempts") {
		t.Errorf("error should report the attempt count, got %v", err)
	}
}

// Cancelling during a long Retry-After must return promptly.
func TestBackoffIsCancellable(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()

	start := time.Now()
	resp, err := c.Stream(ctx, "", []Message{UserText("hi")}, nil, nil)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("took %v, backoff ignored cancellation", elapsed)
	}
	if err != nil {
		t.Errorf("cancellation should be reported as aborted, got %v", err)
	}
	if resp.StopReason != StopAborted {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, StopAborted)
	}
}

// The distinction that forced APIError into existence: on Kimi a prompt that does
// not fit and a malformed request are both 400 invalid_request_error, and they need
// opposite responses. One is recoverable by shrinking and retrying; the other only
// by a human. Classifying on type alone cannot separate them.
func TestContextOverflowIsSeparatedFromOtherBadRequests(t *testing.T) {
	cases := []struct {
		name string
		err  *APIError
		want bool
	}{
		{
			// Captured from the live endpoint, not from documentation.
			name: "kimi live rejection",
			err: &APIError{Status: 400, Type: "invalid_request_error",
				Message: "Invalid request: Your request exceeded model token limit: 262144 (requested: 400011)"},
			want: true,
		},
		{
			name: "kimi prompt plus max_tokens",
			err: &APIError{Status: 400, Type: "invalid_request_error",
				Message: "prompt tokens + max_tokens exceeds the model specification"},
			want: true,
		},
		{
			name: "openai-compatible gateway",
			err: &APIError{Status: 400, Type: "invalid_request_error",
				Message: "This model's maximum context length is 128000 tokens."},
			want: true,
		},
		{
			// Same status, same type, and must not be treated as recoverable:
			// answering a malformed request by discarding history would destroy
			// context to work around a bug in the request.
			name: "missing parameter",
			err: &APIError{Status: 400, Type: "invalid_request_error",
				Message: "Request format error, missing required parameter"},
			want: false,
		},
		{
			name: "content filter",
			err: &APIError{Status: 400, Type: "content_filter",
				Message: "The request was rejected because it was considered high risk"},
			want: false,
		},
		{
			name: "bad key",
			err:  &APIError{Status: 401, Type: "invalid_authentication_error", Message: "Invalid Authentication"},
			want: false,
		},
		{
			// A server fault whose body happens to mention token limits is not a
			// reason to throw away the conversation.
			name: "server error mentioning tokens",
			err: &APIError{Status: 500, Type: "server_error",
				Message: "internal error handling maximum context length"},
			want: false,
		},
		{
			name: "rate limit",
			err:  &APIError{Status: 429, Type: "rate_limit_reached_error", Message: "TPM limit reached"},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.ContextOverflow(); got != tc.want {
				t.Errorf("ContextOverflow() = %v, want %v for %q", got, tc.want, tc.err.Error())
			}
			if got := IsContextOverflow(tc.err); got != tc.want {
				t.Errorf("IsContextOverflow() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The retry loop returns its last error wrapped in "after N attempts: %w", so the
// classifier has to see through that or it would only work when retries are off.
func TestIsContextOverflowSeesThroughWrapping(t *testing.T) {
	inner := &APIError{Status: 400, Type: "invalid_request_error",
		Message: "Your request exceeded model token limit: 262144"}
	if !IsContextOverflow(fmt.Errorf("after 4 attempts: %w", inner)) {
		t.Error("a wrapped overflow was not recognised")
	}
	if IsContextOverflow(errors.New("400 Bad Request: exceeded model token limit")) {
		t.Error("a bare string was treated as an overflow; the type is the signal, not the prose")
	}
	if IsContextOverflow(nil) {
		t.Error("nil is not an overflow")
	}
}

// Overflow must not be retried at the HTTP layer: the same body would be rejected
// identically, and the only useful response is to make the prompt smaller, which
// this layer cannot do.
func TestOverflowIsNotRetriedAtTheTransportLayer(t *testing.T) {
	if retryableStatus(http.StatusBadRequest) {
		t.Error("400 became retryable; an overflow would then burn the retry budget unchanged")
	}
}

// Error() has to stay readable, because it is what a user sees when recovery is not
// possible.
func TestAPIErrorMessageNamesStatusTypeAndReason(t *testing.T) {
	got := (&APIError{Status: 400, StatusText: "400 Bad Request",
		Type: "invalid_request_error", Message: "too long"}).Error()
	for _, want := range []string{"400 Bad Request", "invalid_request_error", "too long"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is missing %q", got, want)
		}
	}
	// A body this type could not parse still has to produce something.
	if got := (&APIError{Status: 503, StatusText: "503 Service Unavailable"}).Error(); got == "" {
		t.Error("an unparseable body produced an empty error")
	}
}
