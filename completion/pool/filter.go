package pool

import (
	"github.com/sony/gobreaker"

	"llm_gateway/completion"
)

type Filter interface {
	Apply(req *completion.CompletionRequest, candidates []*Endpoint) []*Endpoint
	Name() string
}

type ModelAffinityFilter struct{}

func (ModelAffinityFilter) Name() string { return "model_affinity" }

func (ModelAffinityFilter) Apply(req *completion.CompletionRequest, candidates []*Endpoint) []*Endpoint {
	if req == nil || req.Model == "" {
		return candidates
	}
	out := make([]*Endpoint, 0, len(candidates))
	for _, ep := range candidates {
		if ep == nil {
			continue
		}
		if endpointAcceptsModel(ep.Cfg.Models, req.Model) {
			out = append(out, ep)
		}
	}
	return out
}

func endpointAcceptsModel(models []string, requested string) bool {
	if len(models) == 0 {
		return true
	}
	for _, m := range models {
		if m == "*" || m == requested {
			return true
		}
	}
	return false
}

type BreakerOpenFilter struct{}

func (BreakerOpenFilter) Name() string { return "breaker_open" }

func (BreakerOpenFilter) Apply(_ *completion.CompletionRequest, candidates []*Endpoint) []*Endpoint {
	out := make([]*Endpoint, 0, len(candidates))
	for _, ep := range candidates {
		if ep == nil {
			continue
		}
		if ep.Breaker != nil && ep.Breaker.State() == gobreaker.StateOpen {
			continue
		}
		out = append(out, ep)
	}
	return out
}
