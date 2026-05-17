package pool

import (
	"testing"

	"llm_gateway/completion"
)

func TestLeastPending_PicksMin(t *testing.T) {
	sel := NewLeastPendingSelector()
	cs := []*Endpoint{
		{Cfg: EndpointConfig{Name: "a", Weight: 1, Enabled: true}, Stats: &endpointStats{}},
		{Cfg: EndpointConfig{Name: "b", Weight: 1, Enabled: true}, Stats: &endpointStats{}},
		{Cfg: EndpointConfig{Name: "c", Weight: 1, Enabled: true}, Stats: &endpointStats{}},
	}
	cs[0].Stats.InFlight.Store(3)
	cs[1].Stats.InFlight.Store(1)
	cs[2].Stats.InFlight.Store(2)

	ep, ok := sel.Pick(&completion.CompletionRequest{}, cs, nil)
	if !ok || ep.Cfg.Name != "b" {
		t.Fatalf("expected b, got %v", ep)
	}
}

func TestLeastPending_TieBreakByWeight(t *testing.T) {
	sel := NewLeastPendingSelector()
	cs := []*Endpoint{
		{Cfg: EndpointConfig{Name: "low", Weight: 1, Enabled: true}, Stats: &endpointStats{}},
		{Cfg: EndpointConfig{Name: "high", Weight: 5, Enabled: true}, Stats: &endpointStats{}},
	}
	// Both at 0 in-flight; higher weight wins.
	ep, ok := sel.Pick(&completion.CompletionRequest{}, cs, nil)
	if !ok || ep.Cfg.Name != "high" {
		t.Fatalf("expected high (weight tie-break), got %v", ep)
	}
}

func TestLeastPending_SkipsTried(t *testing.T) {
	sel := NewLeastPendingSelector()
	cs := []*Endpoint{
		{Cfg: EndpointConfig{Name: "a", Weight: 1, Enabled: true}, Stats: &endpointStats{}},
		{Cfg: EndpointConfig{Name: "b", Weight: 1, Enabled: true}, Stats: &endpointStats{}},
	}
	cs[0].Stats.InFlight.Store(0) // would be picked
	cs[1].Stats.InFlight.Store(5)
	tried := map[string]struct{}{"a": {}}
	ep, ok := sel.Pick(nil, cs, tried)
	if !ok || ep.Cfg.Name != "b" {
		t.Fatalf("expected b (a tried), got %v", ep)
	}
}

func TestLeastPending_SkipsDisabled(t *testing.T) {
	sel := NewLeastPendingSelector()
	cs := []*Endpoint{
		{Cfg: EndpointConfig{Name: "a", Weight: 1, Enabled: false}, Stats: &endpointStats{}},
		{Cfg: EndpointConfig{Name: "b", Weight: 1, Enabled: true}, Stats: &endpointStats{}},
	}
	cs[0].Stats.InFlight.Store(0)
	cs[1].Stats.InFlight.Store(99)
	ep, ok := sel.Pick(nil, cs, nil)
	if !ok || ep.Cfg.Name != "b" {
		t.Fatalf("expected b (a disabled), got %v", ep)
	}
}
