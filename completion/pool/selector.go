package pool

import (
	"math/rand"
	"sync"
	"time"

	"llm_gateway/completion"
)

type Selector interface {
	Pick(req *completion.CompletionRequest, candidates []*Endpoint, tried map[string]struct{}) (*Endpoint, bool)
	Name() string
}

type WeightedRandomSelector struct {
	mu  sync.Mutex
	rng *rand.Rand
}

func NewWeightedRandomSelector() *WeightedRandomSelector {
	return &WeightedRandomSelector{
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func newWeightedRandomSelectorWithRng(rng *rand.Rand) *WeightedRandomSelector {
	return &WeightedRandomSelector{rng: rng}
}

func (s *WeightedRandomSelector) Name() string { return "weighted_random" }

func (s *WeightedRandomSelector) Pick(_ *completion.CompletionRequest, candidates []*Endpoint, tried map[string]struct{}) (*Endpoint, bool) {
	eligible := make([]*Endpoint, 0, len(candidates))
	total := 0
	for _, ep := range candidates {
		if ep == nil || !ep.Cfg.Enabled {
			continue
		}
		if _, skip := tried[ep.Cfg.Name]; skip {
			continue
		}
		if ep.Cfg.Weight <= 0 {
			continue
		}
		eligible = append(eligible, ep)
		total += ep.Cfg.Weight
	}
	if len(eligible) == 0 || total <= 0 {
		return nil, false
	}

	s.mu.Lock()
	r := s.rng.Intn(total)
	s.mu.Unlock()

	for _, ep := range eligible {
		if r < ep.Cfg.Weight {
			return ep, true
		}
		r -= ep.Cfg.Weight
	}
	return eligible[len(eligible)-1], true
}
