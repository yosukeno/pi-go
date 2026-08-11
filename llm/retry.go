package llm

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Retry defaults follow what the Stainless-generated OpenAI and Anthropic SDKs
// do, since those are tuned against the real services: retry a small number of
// times, prefer the server's own Retry-After over any local guess, and back off
// exponentially with jitter when there is no hint.
const (
	DefaultMaxRetries = 3
	baseRetryDelay    = 500 * time.Millisecond
	maxRetryDelay     = 8 * time.Second
)

// RetryInfo is reported to the OnRetry hook so a UI can show that a turn is
// being retried rather than appearing to hang.
type RetryInfo struct {
	Attempt int // 1-based index of the attempt that just failed
	Max     int
	Delay   time.Duration
	Reason  string
}

// retryableStatus lists the statuses worth a second attempt.
//
// 408 and 409 are included because a request timeout or a transient conflict is
// not the caller's fault. Everything else in 4xx is a permanent problem with the
// request itself (bad key, bad model, malformed body) and retrying only wastes
// the rate limit.
func retryableStatus(code int) bool {
	return code == http.StatusRequestTimeout ||
		code == http.StatusConflict ||
		code == http.StatusTooManyRequests ||
		code >= http.StatusInternalServerError
}

// shouldRetryResponse lets the server override the status-based decision, which
// is how providers signal "this 400 is actually transient" or "do not hammer me
// on this 500".
func shouldRetryResponse(resp *http.Response) bool {
	switch resp.Header.Get("x-should-retry") {
	case "true":
		return true
	case "false":
		return false
	}
	return retryableStatus(resp.StatusCode)
}

// retryDelay prefers the server's instruction, falling back to exponential
// backoff with jitter. Jitter is subtracted rather than added so the delay never
// exceeds the cap, and so a fleet of clients that hit a 429 together does not
// come back in lockstep.
func retryDelay(resp *http.Response, attempt int) time.Duration {
	if d, ok := retryAfter(resp); ok {
		return max(0, d)
	}
	delay := time.Duration(float64(baseRetryDelay) * math.Pow(2, float64(attempt)))
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}
	return delay - rand.N(delay/4)
}

// retryAfter reads the wait hint. Retry-After-Ms is checked first because it is
// more precise; Retry-After itself is either a number of seconds or an HTTP-date.
func retryAfter(resp *http.Response) (time.Duration, bool) {
	if resp == nil {
		return 0, false
	}
	if v := resp.Header.Get("Retry-After-Ms"); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil {
			return time.Duration(ms * float64(time.Millisecond)), true
		}
	}
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		return time.Duration(secs * float64(time.Second)), true
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t), true
	}
	return 0, false
}

// sleep waits out the backoff while staying cancellable. A Ctrl-C during a
// 30-second Retry-After should return immediately, not after 30 seconds.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// APIError is a provider's non-2xx response, kept structured rather than flattened
// into a string so callers can act on the kind of failure rather than on its prose.
//
// The distinction that forced this type: a prompt that exceeds the context window
// and an invalid API key are both 400s on one of pi-go's providers, and they need
// opposite responses. One is recoverable by making the prompt smaller and trying
// again; the other is recoverable only by a human.
type APIError struct {
	// Status is the HTTP status code, and StatusText the status line, kept because
	// some providers answer with a body this type cannot parse.
	Status     int
	StatusText string
	// Type is the provider's own error classification, e.g. "invalid_request_error".
	// Empty when the body was not the expected shape.
	Type string
	// Message is the provider's human-readable explanation.
	Message string
}

func (e *APIError) Error() string {
	status := e.StatusText
	if status == "" {
		status = fmt.Sprintf("%d", e.Status)
	}
	switch {
	case e.Type != "" && e.Message != "":
		return status + ": " + e.Type + ": " + e.Message
	case e.Message != "":
		return status + ": " + e.Message
	default:
		return status
	}
}

// overflowPhrases are the fragments that mean "the prompt did not fit".
//
// Matching on prose is unpleasant and it is what the providers leave available:
// on Kimi this failure arrives as type "invalid_request_error", which is the same
// type as a missing parameter and a malformed body, so the type alone cannot
// separate a recoverable overflow from a bug in the request. Verified against the
// live endpoint rather than taken from documentation:
//
//	400 invalid_request_error
//	"Invalid request: Your request exceeded model token limit: 262144 (requested: 400011)"
//
// Kimi's published table lists a second wording for the case where the prompt fits
// but prompt + max_tokens does not, and OpenAI-compatible gateways in front of
// other models commonly answer "context_length_exceeded" or "maximum context
// length", so those are here too. The list is deliberately about phrases that only
// ever mean this: a false positive would make pi-go respond to a malformed request
// by throwing away context, which is worse than reporting the error.
var overflowPhrases = []string{
	"exceeded model token limit",
	"context_length_exceeded",
	"maximum context length",
	"exceeds the model specification",
	"input token length too long",
	"reduce the length of the messages",
	"prompt is too long",
	"too many tokens",
}

// ContextOverflow reports whether this failure was the prompt not fitting.
//
// Restricted to 4xx: a 500 whose body happens to mention token limits is a server
// fault, and answering it by discarding history would destroy context to work
// around an outage. 413 is included because a gateway may answer that way.
func (e *APIError) ContextOverflow() bool {
	if e == nil || e.Status < 400 || e.Status >= 500 {
		return false
	}
	hay := strings.ToLower(e.Type + " " + e.Message)
	for _, p := range overflowPhrases {
		if strings.Contains(hay, p) {
			return true
		}
	}
	return false
}

// IsContextOverflow reports whether err is, or wraps, a provider rejection caused
// by the prompt being too large. The wrapped form matters: the retry loop returns
// "after N attempts: %w" around the last error.
func IsContextOverflow(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.ContextOverflow()
	}
	return false
}
