package pool

import (
	"testing"

	"llm_gateway/completion"
)

func TestEWMA_PicksLowestLatency(t *testing.T) {
	sel := NewEWMALatencySelector()
	cs := []*Endpoint{
		{Cfg: EndpointConfig{Name: "slow", Weight: 1, Enabled: true}, Stats: &endpointStats{}},
		{Cfg: EndpointConfig{Name: "fast", Weight: 1, Enabled: true}, Stats: &endpointStats{}},
	}
	cs[0].Stats.LatencyUsEWMA.Store(300_000)
	cs[1].Stats.LatencyUsEWMA.Store(50_000)
	ep, ok := sel.Pick(&completion.CompletionRequest{}, cs, nil)
	if !ok || ep.Cfg.Name != "fast" {
		t.Fatalf("expected fast, got %v", ep)
	}
}

func TestEWMA_ProbesUnsampled(t *testing.T) {
	sel := NewEWMALatencySelector()
	// "new" has zero samples; "old" has very low latency.
	cs := []*Endpoint{
		{Cfg: EndpointConfig{Name: "old", Weight: 1, Enabled: true}, Stats: &endpointStats{}},
		{Cfg: EndpointConfig{Name: "new", Weight: 1, Enabled: true}, Stats: &endpointStats{}},
	}
	cs[0].Stats.LatencyUsEWMA.Store(10_000) // 10ms
	// "new" stays at zero.
	ep, ok := sel.Pick(nil, cs, nil)
	if !ok || ep.Cfg.Name != "new" {
		t.Fatalf("expected new (zero-sample probe boost), got %v", ep)
	}
}

func TestEWMA_AllUnsampledFallsBackToWeight(t *testing.T) {
	sel := NewEWMALatencySelector()
	cs := []*Endpoint{
		{Cfg: EndpointConfig{Name: "small", Weight: 1, Enabled: true}, Stats: &endpointStats{}},
		{Cfg: EndpointConfig{Name: "big", Weight: 9, Enabled: true}, Stats: &endpointStats{}},
	}
	ep, ok := sel.Pick(nil, cs, nil)
	if !ok || ep.Cfg.Name != "big" {
		t.Fatalf("expected big (weight tie-break among unsampled), got %v", ep)
	}
}

func TestEWMA_SkipsTriedAndDisabled(t *testing.T) {
	sel := NewEWMALatencySelector()
	cs := []*Endpoint{
		{Cfg: EndpointConfig{Name: "a", Weight: 1, Enabled: false}, Stats: &endpointStats{}},
		{Cfg: EndpointConfig{Name: "b", Weight: 1, Enabled: true}, Stats: &endpointStats{}},
		{Cfg: EndpointConfig{Name: "c", Weight: 1, Enabled: true}, Stats: &endpointStats{}},
	}
	cs[0].Stats.LatencyUsEWMA.Store(1)
	cs[1].Stats.LatencyUsEWMA.Store(100)
	cs[2].Stats.LatencyUsEWMA.Store(50)
	tried := map[string]struct{}{"c": {}}
	ep, ok := sel.Pick(nil, cs, tried)
	if !ok || ep.Cfg.Name != "b" {
		t.Fatalf("expected b (a disabled, c tried), got %v", ep)
	}
}
