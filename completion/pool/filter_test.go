package pool

import (
	"testing"

	"llm_gateway/completion"
)

func TestModelAffinity_ExactMatch(t *testing.T) {
	f := ModelAffinityFilter{}
	cs := []*Endpoint{
		{Cfg: EndpointConfig{Name: "a", Models: []string{"gpt-4o"}, Enabled: true}},
		{Cfg: EndpointConfig{Name: "b", Models: []string{"claude-3"}, Enabled: true}},
	}
	out := f.Apply(&completion.CompletionRequest{Model: "gpt-4o"}, cs)
	if len(out) != 1 || out[0].Cfg.Name != "a" {
		t.Fatalf("expected only a, got %v", names(out))
	}
}

func TestModelAffinity_Wildcard(t *testing.T) {
	f := ModelAffinityFilter{}
	cs := []*Endpoint{
		{Cfg: EndpointConfig{Name: "a", Models: []string{"gpt-4o"}, Enabled: true}},
		{Cfg: EndpointConfig{Name: "b", Models: []string{"*"}, Enabled: true}},
	}
	out := f.Apply(&completion.CompletionRequest{Model: "anything"}, cs)
	if len(out) != 1 || out[0].Cfg.Name != "b" {
		t.Fatalf("expected only b (wildcard), got %v", names(out))
	}
}

func TestModelAffinity_EmptyTreatedAsWildcard(t *testing.T) {
	f := ModelAffinityFilter{}
	cs := []*Endpoint{
		{Cfg: EndpointConfig{Name: "a", Models: nil, Enabled: true}},
		{Cfg: EndpointConfig{Name: "b", Models: []string{}, Enabled: true}},
	}
	out := f.Apply(&completion.CompletionRequest{Model: "whatever"}, cs)
	if len(out) != 2 {
		t.Fatalf("expected both, got %v", names(out))
	}
}

func TestModelAffinity_NoMatchReturnsEmpty(t *testing.T) {
	f := ModelAffinityFilter{}
	cs := []*Endpoint{
		{Cfg: EndpointConfig{Name: "a", Models: []string{"gpt-4o"}, Enabled: true}},
	}
	out := f.Apply(&completion.CompletionRequest{Model: "claude-3"}, cs)
	if len(out) != 0 {
		t.Fatalf("expected empty, got %v", names(out))
	}
}

func TestModelAffinity_NilOrEmptyModelKeepsAll(t *testing.T) {
	f := ModelAffinityFilter{}
	cs := []*Endpoint{
		{Cfg: EndpointConfig{Name: "a", Models: []string{"gpt-4o"}, Enabled: true}},
		{Cfg: EndpointConfig{Name: "b", Models: []string{"claude-3"}, Enabled: true}},
	}
	out := f.Apply(&completion.CompletionRequest{Model: ""}, cs)
	if len(out) != 2 {
		t.Fatalf("empty Model should not filter, got %v", names(out))
	}
}

func TestBreakerOpenFilter_PassthroughWhenNilBreaker(t *testing.T) {
	f := BreakerOpenFilter{}
	cs := []*Endpoint{
		{Cfg: EndpointConfig{Name: "a", Enabled: true}, Breaker: nil},
		{Cfg: EndpointConfig{Name: "b", Enabled: true}, Breaker: nil},
	}
	out := f.Apply(nil, cs)
	if len(out) != 2 {
		t.Fatalf("nil-breaker endpoints should pass, got %v", names(out))
	}
}

func TestBreakerOpenFilter_ExcludesOpen(t *testing.T) {
	b, err := newBreaker("test", BreakerConfig{Enabled: true, MinRequests: 1, FailureRatio: 0.1, Timeout: "1m"})
	if err != nil {
		t.Fatal(err)
	}
	// Force breaker open by failing more than MinRequests with ratio above threshold.
	for range 5 {
		_, _ = b.Execute(func() (any, error) {
			return nil, errBoom
		})
	}
	if b.State().String() != "open" {
		t.Fatalf("expected breaker open, got %s", b.State())
	}

	f := BreakerOpenFilter{}
	cs := []*Endpoint{
		{Cfg: EndpointConfig{Name: "open", Enabled: true}, Breaker: b},
		{Cfg: EndpointConfig{Name: "ok", Enabled: true}, Breaker: nil},
	}
	out := f.Apply(nil, cs)
	if len(out) != 1 || out[0].Cfg.Name != "ok" {
		t.Fatalf("expected only ok, got %v", names(out))
	}
}

func names(eps []*Endpoint) []string {
	out := make([]string, len(eps))
	for i, ep := range eps {
		out[i] = ep.Cfg.Name
	}
	return out
}
