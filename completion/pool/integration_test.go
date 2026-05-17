package pool

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"llm_gateway/completion"
)

// sseChunk is a minimal OpenAI-compatible streaming response chunk.
const sseChunk = `{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"%s"}}]}`

// validSSEHandler emits 3 content deltas + [DONE].
func validSSEHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher := w.(http.Flusher)
	for _, part := range []string{"hello", " ", "world"} {
		fmt.Fprintf(w, "data: "+sseChunk+"\n\n", part)
		flusher.Flush()
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func TestIntegration_FailoverAcrossUpstreams(t *testing.T) {
	var aCalls, bCalls int64

	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&aCalls, 1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer a.Close()

	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&bCalls, 1)
		validSSEHandler(w, r)
	}))
	defer b.Close()

	t.Setenv("FAKE_KEY", "test-key")

	cfg := Config{
		Strategy:    "weighted_random",
		MaxAttempts: 3,
		Endpoints: []EndpointConfig{
			{Name: "a", URL: a.URL, APIKeyEnv: "FAKE_KEY", Weight: 1, Enabled: true},
			{Name: "b", URL: b.URL, APIKeyEnv: "FAKE_KEY", Weight: 1, Enabled: true},
		},
	}
	svc, err := NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}

	const requests = 50
	successes := 0
	for i := range requests {
		ch, err := svc.GetStream(context.Background(), &completion.CompletionRequest{
			Model: "test", Question: "hi", MaxTokens: 16,
		})
		if err != nil {
			t.Errorf("req %d: GetStream failed: %v", i, err)
			continue
		}
		gotContent := false
		gotDone := false
		for chunk := range ch {
			if chunk.Error != nil {
				t.Errorf("req %d: stream error: %v", i, chunk.Error)
				break
			}
			if chunk.Content != "" {
				gotContent = true
			}
			if chunk.Done {
				gotDone = true
			}
		}
		if gotContent && gotDone {
			successes++
		}
	}
	if successes != requests {
		t.Fatalf("expected all %d requests to succeed, got %d (a calls=%d b calls=%d)", requests, successes, aCalls, bCalls)
	}
	if bCalls == 0 {
		t.Fatal("expected b to be hit at least once")
	}
}

func TestIntegration_AllUpstreamsFailingReturnsError(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer bad.Close()

	t.Setenv("FAKE_KEY", "test-key")

	cfg := Config{
		Strategy:    "weighted_random",
		MaxAttempts: 5,
		Endpoints: []EndpointConfig{
			{Name: "a", URL: bad.URL, APIKeyEnv: "FAKE_KEY", Weight: 1, Enabled: true},
			{Name: "b", URL: bad.URL, APIKeyEnv: "FAKE_KEY", Weight: 1, Enabled: true},
		},
	}
	svc, err := NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	_, err = svc.GetStream(context.Background(), &completion.CompletionRequest{Model: "x", Question: "y"})
	if err == nil {
		t.Fatal("expected error when all upstreams fail")
	}
}
