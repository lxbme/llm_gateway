package pool

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"llm_gateway/completion"
	"llm_gateway/completion/openai"
	"llm_gateway/internal/tracing"

	"go.opentelemetry.io/otel/attribute"
)

type Service struct {
	mu          sync.RWMutex
	endpoints   []*Endpoint
	selector    Selector
	filters     []Filter
	maxAttempts int
	// Captured at construction so Admin.AddEndpoint / ResetBreaker can rebuild lazily.
	factory    clientFactory
	breakerCfg BreakerConfig
}

type clientFactory func(cfg EndpointConfig) upstreamClient

func defaultClientFactory(cfg EndpointConfig) upstreamClient {
	return openai.New(cfg.URL, cfg.APIKeyEnv)
}

func NewFromConfig(cfg Config) (*Service, error) {
	return newFromConfig(cfg, defaultClientFactory)
}

func newFromConfig(cfg Config, factory clientFactory) (*Service, error) {
	if err := validate(&cfg); err != nil {
		return nil, err
	}

	eps := make([]*Endpoint, 0, len(cfg.Endpoints))
	for _, ec := range cfg.Endpoints {
		ep := &Endpoint{
			Cfg:    ec,
			Client: factory(ec),
			Stats:  &endpointStats{},
		}
		if cfg.Breaker.Enabled {
			b, err := newBreaker(ec.Name, cfg.Breaker)
			if err != nil {
				return nil, fmt.Errorf("pool: build breaker for %s: %w", ec.Name, err)
			}
			ep.Breaker = b
		}
		eps = append(eps, ep)
	}

	var sel Selector
	switch cfg.Strategy {
	case "weighted_random":
		sel = NewWeightedRandomSelector()
	case "least_pending":
		sel = NewLeastPendingSelector()
	case "ewma_latency":
		sel = NewEWMALatencySelector()
	default:
		return nil, fmt.Errorf("pool: unsupported strategy %q", cfg.Strategy)
	}

	filters := []Filter{ModelAffinityFilter{}, BreakerOpenFilter{}}

	names := make([]string, 0, len(eps))
	for _, ep := range eps {
		names = append(names, fmt.Sprintf("%s(w=%d,enabled=%t,breaker=%s)", ep.Cfg.Name, ep.Cfg.Weight, ep.Cfg.Enabled, breakerStateName(ep.Breaker)))
	}
	log.Printf("[Info] pool: strategy=%s max_attempts=%d endpoints=%v", sel.Name(), cfg.MaxAttempts, names)

	return &Service{
		endpoints:   eps,
		selector:    sel,
		filters:     filters,
		maxAttempts: cfg.MaxAttempts,
		factory:     factory,
		breakerCfg:  cfg.Breaker,
	}, nil
}

// PoolStats returns a per-endpoint snapshot of runtime statistics.
// Implements completion.StatsProvider.
func (s *Service) PoolStats(_ context.Context) ([]completion.EndpointStatsSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]completion.EndpointStatsSnapshot, 0, len(s.endpoints))
	for _, ep := range s.endpoints {
		in, succ, fail, rate, latMs := ep.Stats.snapshot()
		out = append(out, completion.EndpointStatsSnapshot{
			Endpoint:     ep.Cfg.Name,
			Weight:       ep.Cfg.Weight,
			Enabled:      ep.Cfg.Enabled,
			InFlight:     in,
			Success:      succ,
			Failure:      fail,
			SuccessRate:  rate,
			LatencyMs:    latMs,
			BreakerState: breakerStateName(ep.Breaker),
		})
	}
	return out, nil
}

func (s *Service) snapshotEndpoints() []*Endpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Endpoint, len(s.endpoints))
	copy(out, s.endpoints)
	return out
}

func (s *Service) applyFilters(req *completion.CompletionRequest, candidates []*Endpoint) []*Endpoint {
	for _, f := range s.filters {
		candidates = f.Apply(req, candidates)
		if len(candidates) == 0 {
			return candidates
		}
	}
	return candidates
}

func (s *Service) GetStream(ctx context.Context, req *completion.CompletionRequest) (<-chan *completion.CompletionChunk, error) {
	snapshot := s.snapshotEndpoints()
	if len(snapshot) == 0 {
		return nil, errors.New("pool: no endpoints configured")
	}

	tried := make(map[string]struct{}, s.maxAttempts)
	var lastErr error
	for attempt := 0; attempt < s.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		candidates := s.applyFilters(req, snapshot)

		_, selectSpan := tracing.Tracer("completion.pool").Start(ctx, "completion.pool.select")
		ep, ok := s.selector.Pick(req, candidates, tried)
		selectSpan.SetAttributes(
			attribute.String("strategy", s.selector.Name()),
			attribute.Int("attempt", attempt),
			attribute.Int("candidates", len(candidates)),
		)
		if ok {
			selectSpan.SetAttributes(attribute.String("endpoint", ep.Cfg.Name))
		}
		selectSpan.End()

		if !ok {
			if lastErr != nil {
				return nil, fmt.Errorf("pool: exhausted after %d attempt(s): %w", attempt, lastErr)
			}
			return nil, errors.New("pool: no eligible endpoint")
		}
		tried[ep.Cfg.Name] = struct{}{}

		started := ep.Stats.start()
		ch, err := callEndpoint(ctx, ep, req)
		if err == nil {
			log.Printf("[Info] pool: served by %s (attempt %d)", ep.Cfg.Name, attempt+1)
			return wrapChannelForStats(ep, started, ch), nil
		}
		ep.Stats.end(started, true)
		log.Printf("[Info] pool: %s pre-stream error: %v (attempt %d)", ep.Cfg.Name, err, attempt+1)
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("unknown")
	}
	return nil, fmt.Errorf("pool: max_attempts=%d exhausted: %w", s.maxAttempts, lastErr)
}

// wrapChannelForStats forwards chunks while tracking success/failure + latency.
// First chunk.Error (or context.Canceled drain) marks the call failed.
func wrapChannelForStats(ep *Endpoint, started time.Time, src <-chan *completion.CompletionChunk) <-chan *completion.CompletionChunk {
	out := make(chan *completion.CompletionChunk, cap(src))
	go func() {
		defer close(out)
		errored := false
		for c := range src {
			if c != nil && c.Error != nil {
				errored = true
			}
			out <- c
		}
		ep.Stats.end(started, errored)
	}()
	return out
}

func callEndpoint(ctx context.Context, ep *Endpoint, req *completion.CompletionRequest) (<-chan *completion.CompletionChunk, error) {
	if ep.Breaker == nil {
		return ep.Client.GetStream(ctx, req)
	}
	res, err := ep.Breaker.Execute(func() (any, error) {
		return ep.Client.GetStream(ctx, req)
	})
	if err != nil {
		return nil, err
	}
	ch, _ := res.(<-chan *completion.CompletionChunk)
	return ch, nil
}
