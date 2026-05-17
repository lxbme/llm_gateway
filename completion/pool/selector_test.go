package pool

import (
	"math"
	"math/rand"
	"testing"
)

func mkEndpoint(name string, weight int, enabled bool) *Endpoint {
	return &Endpoint{Cfg: EndpointConfig{Name: name, Weight: weight, Enabled: enabled}}
}

func TestWeightedRandom_RespectsWeights(t *testing.T) {
	sel := newWeightedRandomSelectorWithRng(rand.New(rand.NewSource(42)))
	candidates := []*Endpoint{
		mkEndpoint("a", 1, true),
		mkEndpoint("b", 9, true),
	}

	const trials = 10000
	counts := map[string]int{}
	for i := 0; i < trials; i++ {
		ep, ok := sel.Pick(nil, candidates, nil)
		if !ok {
			t.Fatalf("pick failed at trial %d", i)
		}
		counts[ep.Cfg.Name]++
	}

	ratioB := float64(counts["b"]) / float64(trials)
	if math.Abs(ratioB-0.9) > 0.02 {
		t.Errorf("expected b ratio ~0.9, got %.4f (a=%d b=%d)", ratioB, counts["a"], counts["b"])
	}
}

func TestWeightedRandom_SkipsTried(t *testing.T) {
	sel := newWeightedRandomSelectorWithRng(rand.New(rand.NewSource(1)))
	cs := []*Endpoint{mkEndpoint("a", 1, true), mkEndpoint("b", 1, true)}
	tried := map[string]struct{}{"a": {}}
	for i := 0; i < 50; i++ {
		ep, ok := sel.Pick(nil, cs, tried)
		if !ok {
			t.Fatalf("pick failed")
		}
		if ep.Cfg.Name != "b" {
			t.Fatalf("expected b, got %s", ep.Cfg.Name)
		}
	}
}

func TestWeightedRandom_SkipsDisabled(t *testing.T) {
	sel := newWeightedRandomSelectorWithRng(rand.New(rand.NewSource(1)))
	cs := []*Endpoint{
		mkEndpoint("a", 1, false),
		mkEndpoint("b", 1, true),
	}
	for i := 0; i < 50; i++ {
		ep, ok := sel.Pick(nil, cs, nil)
		if !ok {
			t.Fatalf("pick failed")
		}
		if ep.Cfg.Name != "b" {
			t.Fatalf("expected b, got %s", ep.Cfg.Name)
		}
	}
}

func TestWeightedRandom_AllTriedReturnsFalse(t *testing.T) {
	sel := newWeightedRandomSelectorWithRng(rand.New(rand.NewSource(1)))
	cs := []*Endpoint{mkEndpoint("a", 1, true), mkEndpoint("b", 1, true)}
	tried := map[string]struct{}{"a": {}, "b": {}}
	if _, ok := sel.Pick(nil, cs, tried); ok {
		t.Fatal("expected ok=false when all tried")
	}
}

func TestWeightedRandom_NoEligibleReturnsFalse(t *testing.T) {
	sel := newWeightedRandomSelectorWithRng(rand.New(rand.NewSource(1)))
	cs := []*Endpoint{mkEndpoint("a", 1, false)}
	if _, ok := sel.Pick(nil, cs, nil); ok {
		t.Fatal("expected ok=false when no enabled endpoint")
	}
}

func TestWeightedRandom_ZeroWeightSkipped(t *testing.T) {
	sel := newWeightedRandomSelectorWithRng(rand.New(rand.NewSource(1)))
	cs := []*Endpoint{
		mkEndpoint("a", 0, true),
		mkEndpoint("b", 5, true),
	}
	for i := 0; i < 50; i++ {
		ep, ok := sel.Pick(nil, cs, nil)
		if !ok || ep.Cfg.Name != "b" {
			t.Fatalf("expected b, got %v ok=%v", ep, ok)
		}
	}
}
