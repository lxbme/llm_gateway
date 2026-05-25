package pool

import (
	"context"
	"errors"
	"strings"
	"testing"

	"llm_gateway/completion"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestCollector_ExposesPoolStats drives one successful request through endpoint
// "a" and one failure through "b", then asserts that the Prometheus Collector
// produces the expected series for both endpoints.
func TestCollector_ExposesPoolStats(t *testing.T) {
	clientA := &fakeClient{queue: []fakeResult{{ch: makeChunkChan("hi"), err: nil}}}
	clientB := &fakeClient{queue: []fakeResult{{err: errors.New("upstream down")}}}

	cfg := Config{
		Strategy:    "weighted_random",
		MaxAttempts: 1,
		Endpoints: []EndpointConfig{
			{Name: "a", URL: "http://a", APIKeyEnv: "K", Weight: 1, Enabled: true},
			{Name: "b", URL: "http://b", APIKeyEnv: "K", Weight: 1, Enabled: true},
		},
	}
	clients := map[string]upstreamClient{"a": clientA, "b": clientB}
	svc, err := newFromConfig(cfg, func(c EndpointConfig) upstreamClient { return clients[c.Name] })
	if err != nil {
		t.Fatalf("newFromConfig: %v", err)
	}

	// Drive "a" successfully (1 success).
	svc.selector = &orderedSelector{order: []string{"a"}}
	ch, err := svc.GetStream(context.Background(), &completion.CompletionRequest{})
	if err != nil {
		t.Fatalf("GetStream a: %v", err)
	}
	for range ch {
	}

	// Drive "b" (1 failure).
	svc.selector = &orderedSelector{order: []string{"b"}}
	if _, err := svc.GetStream(context.Background(), &completion.CompletionRequest{}); err == nil {
		t.Fatalf("expected b to fail")
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(NewCollector(svc))

	// 5 metric families × 2 endpoints = 10 series total.
	if got := testutil.CollectAndCount(NewCollector(svc)); got != 10 {
		t.Fatalf("expected 10 series, got %d", got)
	}

	// Spot-check: success_total for a == 1, failure_total for b == 1.
	expected := `
# HELP completion_pool_success_total Total successful upstream completion calls per endpoint.
# TYPE completion_pool_success_total counter
completion_pool_success_total{endpoint="a"} 1
completion_pool_success_total{endpoint="b"} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "completion_pool_success_total"); err != nil {
		t.Fatalf("success counter mismatch: %v", err)
	}

	expectedFailure := `
# HELP completion_pool_failure_total Total failed upstream completion calls per endpoint.
# TYPE completion_pool_failure_total counter
completion_pool_failure_total{endpoint="a"} 0
completion_pool_failure_total{endpoint="b"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expectedFailure), "completion_pool_failure_total"); err != nil {
		t.Fatalf("failure counter mismatch: %v", err)
	}
}

func TestBreakerStateNum(t *testing.T) {
	cases := map[string]float64{
		"closed":    0,
		"half_open": 1,
		"open":      2,
		"disabled":  -1,
		"":         -1,
		"garbage":  -1,
	}
	for in, want := range cases {
		if got := breakerStateNum(in); got != want {
			t.Errorf("breakerStateNum(%q) = %v, want %v", in, got, want)
		}
	}
}
