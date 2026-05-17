package pool

import (
	"sort"

	"llm_gateway/completion"
)

type EWMALatencySelector struct{}

func NewEWMALatencySelector() *EWMALatencySelector { return &EWMALatencySelector{} }

func (EWMALatencySelector) Name() string { return "ewma_latency" }

// Pick prefers endpoints with the lowest EWMA latency. Endpoints with zero samples
// are always preferred (probed first) to avoid cold-start starvation.
func (EWMALatencySelector) Pick(_ *completion.CompletionRequest, candidates []*Endpoint, tried map[string]struct{}) (*Endpoint, bool) {
	eligible := make([]*Endpoint, 0, len(candidates))
	for _, ep := range candidates {
		if ep == nil || !ep.Cfg.Enabled || ep.Stats == nil {
			continue
		}
		if _, skip := tried[ep.Cfg.Name]; skip {
			continue
		}
		eligible = append(eligible, ep)
	}
	if len(eligible) == 0 {
		return nil, false
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		li := eligible[i].Stats.LatencyUsEWMA.Load()
		lj := eligible[j].Stats.LatencyUsEWMA.Load()
		// Zero-sample endpoints sort first (probe boost).
		iZero, jZero := li == 0, lj == 0
		if iZero != jZero {
			return iZero
		}
		if li != lj {
			return li < lj
		}
		if eligible[i].Cfg.Weight != eligible[j].Cfg.Weight {
			return eligible[i].Cfg.Weight > eligible[j].Cfg.Weight
		}
		return eligible[i].Cfg.Name < eligible[j].Cfg.Name
	})
	return eligible[0], true
}
