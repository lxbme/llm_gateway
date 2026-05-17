package pool

import (
	"context"
	"testing"

	"llm_gateway/completion"
)

func TestPoolStats_AfterTraffic(t *testing.T) {
	a := &fakeClient{queue: []fakeResult{{ch: makeChunkChan("hi"), err: nil}}}

	cfg := Config{
		Strategy:    "weighted_random",
		MaxAttempts: 1,
		Endpoints: []EndpointConfig{
			{Name: "a", URL: "http://a", APIKeyEnv: "K", Weight: 3, Enabled: true},
			{Name: "b", URL: "http://b", APIKeyEnv: "K", Weight: 1, Enabled: false},
		},
	}
	svc, err := newFromConfig(cfg, func(EndpointConfig) upstreamClient { return a })
	if err != nil {
		t.Fatal(err)
	}
	// drive one successful request through "a"
	svc.selector = &orderedSelector{order: []string{"a"}}
	ch, err := svc.GetStream(context.Background(), &completion.CompletionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}

	snaps, err := svc.PoolStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snaps))
	}
	byName := map[string]completion.EndpointStatsSnapshot{}
	for _, s := range snaps {
		byName[s.Endpoint] = s
	}
	if byName["a"].Success != 1 || byName["a"].Failure != 0 {
		t.Fatalf("a: %+v", byName["a"])
	}
	if byName["a"].SuccessRate != 1.0 {
		t.Fatalf("a success_rate expected 1.0, got %f", byName["a"].SuccessRate)
	}
	if byName["b"].Success != 0 || byName["b"].Failure != 0 {
		t.Fatalf("b should have no traffic: %+v", byName["b"])
	}
	if byName["a"].BreakerState != "disabled" {
		t.Fatalf("a breaker should be 'disabled' (breaker not configured), got %q", byName["a"].BreakerState)
	}
	if byName["a"].Weight != 3 || byName["b"].Weight != 1 {
		t.Fatalf("weights wrong: %+v", snaps)
	}
}

func BenchmarkWrapChannelForStats(b *testing.B) {
	ep := &Endpoint{Cfg: EndpointConfig{Name: "x"}, Stats: &endpointStats{}}
	for b.Loop() {
		src := make(chan *completion.CompletionChunk, 10)
		// pre-fill 10 chunks then close
		for range 10 {
			src <- &completion.CompletionChunk{Content: "x"}
		}
		close(src)
		out := wrapChannelForStats(ep, ep.Stats.start(), src)
		for range out {
		}
	}
}
