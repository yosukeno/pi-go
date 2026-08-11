package llm

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// Argument fragments must stream out as they arrive, attributed to their call:
// that is what lets a UI preview a large write instead of sitting silent until
// the whole call has been generated.
func TestToolCallArgumentDeltasStreamWithTheirCallID(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flushFrame(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"write","arguments":"{\"pa"}}]},"finish_reason":null}]}`+"\n\n")
		flushFrame(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a.go\"}"}}]},"finish_reason":null}]}`+"\n\n")
		flushFrame(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		flushFrame(w, "data: [DONE]\n\n")
	})

	var deltas []Delta
	resp, err := c.Stream(context.Background(), "sys", []Message{UserText("hi")}, nil, func(d Delta) {
		deltas = append(deltas, d)
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var starts, fragments int
	for _, d := range deltas {
		switch d.Kind {
		case DeltaToolCallStart:
			starts++
			if d.ToolID != "c1" || d.ToolName != "write" {
				t.Errorf("start delta = %+v", d)
			}
		case DeltaToolCallArgs:
			fragments++
			if d.ToolID != "c1" {
				t.Errorf("fragment lost its call id: %+v", d)
			}
			if d.Text == "" {
				t.Error("an empty fragment was emitted")
			}
		}
	}
	if starts != 1 || fragments != 2 {
		t.Errorf("deltas = %d starts, %d fragments; want 1 and 2", starts, fragments)
	}

	// The settled message is unchanged: fragments are a preview, not a second
	// channel for the arguments.
	calls := resp.Message.ToolCalls()
	if len(calls) != 1 || string(calls[0].Input) != `{"path":"a.go"}` {
		t.Errorf("settled tool calls = %+v", calls)
	}
}

// The seam between the HTTP layer and the classifier: a provider's 400 body has to
// arrive at the caller as an *APIError with the status, type and message intact, or
// the loop cannot tell an overflow from a malformed request and the recovery never
// runs. The body here is the shape Kimi actually returns, captured from a live
// probe against pi-go's own endpoint.
func TestAnOverflowResponseArrivesAsAClassifiableError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error",` +
			`"message":"Invalid request: Your request exceeded model token limit: 262144 (requested: 400011)"}}`))
	})
	_, err := c.Stream(context.Background(), "sys", []Message{UserText("hi")}, nil, nil)
	if err == nil {
		t.Fatal("a 400 returned no error")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is %T, not *APIError: %v", err, err)
	}
	if apiErr.Status != http.StatusBadRequest {
		t.Errorf("Status = %d, want 400; the classifier restricts itself to 4xx", apiErr.Status)
	}
	if apiErr.Type != "invalid_request_error" {
		t.Errorf("Type = %q, want the provider's own classification", apiErr.Type)
	}
	if !strings.Contains(apiErr.Message, "exceeded model token limit") {
		t.Errorf("Message lost the provider's explanation: %q", apiErr.Message)
	}
	if !IsContextOverflow(err) {
		t.Error("the overflow was not classifiable end to end, so the loop cannot recover from it")
	}
}

// And a 400 that is not an overflow must not become one, all the way through the
// same path: responding to a malformed request by discarding history would destroy
// context to work around a bug in the request.
func TestAMalformedRequestIsNotMistakenForAnOverflow(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error",` +
			`"message":"Request format error, missing required parameter"}}`))
	})
	_, err := c.Stream(context.Background(), "sys", []Message{UserText("hi")}, nil, nil)
	if err == nil {
		t.Fatal("a 400 returned no error")
	}
	if IsContextOverflow(err) {
		t.Errorf("a malformed request was classified as an overflow: %v", err)
	}
	// It still has to be readable: this is what a user sees.
	if !strings.Contains(err.Error(), "missing required parameter") {
		t.Errorf("the provider's explanation was lost: %v", err)
	}
}
