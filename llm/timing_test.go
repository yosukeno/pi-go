package llm

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// flusher writes an SSE frame and pushes it out immediately. Without the flush
// the whole body arrives at once and every latency measurement collapses to the
// same number, which would make these tests pass for the wrong reason.
func flushFrame(w http.ResponseWriter, frame string) {
	fmt.Fprint(w, frame)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// TestTTFTMeasuresTheWaitBeforeContentNotBeforeTheFirstFrame is the core of the
// measurement. Providers routinely open with a role-only delta and send
// keep-alives before anything real, so timing the first frame would report a
// latency the user never experienced.
func TestTTFTMeasuresTheWaitBeforeContentNotBeforeTheFirstFrame(t *testing.T) {
	const gap = 150 * time.Millisecond
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// An empty delta first: this is what the role-only opening chunk looks
		// like, and it must not stop the clock.
		flushFrame(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":null}]}\n\n")
		time.Sleep(gap)
		flushFrame(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n")
		flushFrame(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		flushFrame(w, "data: [DONE]\n\n")
	})

	resp, err := c.Stream(context.Background(), "sys", []Message{UserText("hi")}, nil, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if resp.Timing.TTFT < gap {
		t.Errorf("TTFT = %v, want at least the %v the server waited before sending content",
			resp.Timing.TTFT, gap)
	}
	if resp.Timing.TTFB >= resp.Timing.TTFT {
		t.Errorf("TTFB = %v, TTFT = %v: headers must arrive before content",
			resp.Timing.TTFB, resp.Timing.TTFT)
	}
	if resp.Timing.Total < resp.Timing.TTFT {
		t.Errorf("Total = %v, want at least TTFT %v", resp.Timing.Total, resp.Timing.TTFT)
	}
}

// TestTTFTIsStampedByAToolCallWithNoPreamble covers the turn that feels fastest:
// straight to a tool call, no thinking and no text. Stamping only on text would
// report zero for it and drag the average down.
func TestTTFTIsStampedByAToolCallWithNoPreamble(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		time.Sleep(30 * time.Millisecond)
		flushFrame(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"ls","arguments":"{}"}}]},"finish_reason":null}]}`+"\n\n")
		flushFrame(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		flushFrame(w, "data: [DONE]\n\n")
	})

	resp, err := c.Stream(context.Background(), "sys", []Message{UserText("hi")}, nil, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if resp.Timing.TTFT <= 0 {
		t.Fatal("TTFT = 0 for a turn that went straight to a tool call")
	}
}

// TestTTFTIsMeasuredAgainstTheFinalAttempt keeps the number about the model
// rather than about the retry policy: a TTFT that included a failed attempt plus
// its backoff would describe pi-go's own configuration.
func TestTTFTIsMeasuredAgainstTheFinalAttempt(t *testing.T) {
	var attempts int
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After-Ms", "200")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, okStream)
	})

	resp, err := c.Stream(context.Background(), "sys", []Message{UserText("hi")}, nil, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if resp.Timing.TTFT > 150*time.Millisecond {
		t.Errorf("TTFT = %v, want the final attempt only, not the 200ms backoff",
			resp.Timing.TTFT)
	}
	if resp.Timing.Total < 200*time.Millisecond {
		t.Errorf("Total = %v, want the backoff included", resp.Timing.Total)
	}
}

func TestFreshInputIsInputMinusCache(t *testing.T) {
	// The nesting is the thing worth pinning: cached is part of input, so the
	// fresh part is the difference and never the sum.
	u := Usage{Input: 1000, CacheRead: 768, Output: 50, Reasoning: 10}
	if got := u.FreshInput(); got != 232 {
		t.Errorf("FreshInput() = %d, want 232", got)
	}
	// A provider whose cached count exceeds its own prompt total must not produce
	// a negative number in the UI.
	odd := Usage{Input: 100, CacheRead: 200}
	if got := odd.FreshInput(); got != 0 {
		t.Errorf("FreshInput() = %d, want 0 for an inconsistent report", got)
	}
}
