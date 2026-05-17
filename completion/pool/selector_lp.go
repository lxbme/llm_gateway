package pool

import (
	"sort"

	"llm_gateway/completion"
)

type LeastPendingSelector struct{}

func NewLeastPendingSelector() *LeastPendingSelector { return &LeastPendingSelector{} }

func (LeastPendingSelector) Name() string { return "least_pending" }

func (LeastPendingSelector) Pick(_ *completion.CompletionRequest, candidates []*Endpoint, tried map[string]struct{}) (*Endpoint, bool) {
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
		ii := eligible[i].Stats.InFlight.Load()
		jj := eligible[j].Stats.InFlight.Load()
		if ii != jj {
			return ii < jj
		}
		// tie: prefer higher weight, then lexicographic name for stability
		if eligible[i].Cfg.Weight != eligible[j].Cfg.Weight {
			return eligible[i].Cfg.Weight > eligible[j].Cfg.Weight
		}
		return eligible[i].Cfg.Name < eligible[j].Cfg.Name
	})
	return eligible[0], true
}
