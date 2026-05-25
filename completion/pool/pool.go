package pool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"llm_gateway/completion"
	"llm_gateway/completion/openai"
	"llm_gateway/internal/tracing"

	"github.com/sony/gobreaker"
	"go.opentelemetry.io/otel/attribute"
)

// errorClass maps an upstream/retry error into a small, closed enum suitable
// for use as a span event attribute. Open-ended strings (raw err.Error()) are
// cardinality-unsafe and may leak prompt fragments or upstream URLs — use
// tracing.TruncateErr alongside this for the human-readable side.
func errorClass(err error) string {
	if err == nil {
		return "none"
	}
	switch {
	case errors.Is(err, gobreaker.ErrOpenState):
		return "breaker_open"
	case errors.Is(err, gobreaker.ErrTooManyRequests):
		return "breaker_too_many"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "upstream api returned status 5"):
		return "http_5xx"
	case strings.Contains(s, "upstream api returned status 4"):
		return "http_4xx"
	case strings.Contains(s, "fail to call upstream api"),
		strings.Contains(s, "connection refused"),
		strings.Contains(s, "no such host"),
		strings.Contains(s, "i/o timeout"):
		return "network"
	case strings.Contains(s, "fail to build upstream request"),
		strings.Contains(s, "marshal"),
		strings.Contains(s, "unmarshal"):
		return "parse_error"
	}
	return "other"
}

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
	slog.Info("pool initialized",
		"strategy", sel.Name(),
		"max_attempts", cfg.MaxAttempts,
		"endpoints", names,
	)

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

func (s *Service) applyFilters(ctx context.Context, req *completion.CompletionRequest, candidates []*Endpoint) []*Endpoint {
	before := len(candidates)
	byModel, byBreaker := 0, 0
	for _, f := range s.filters {
		prev := len(candidates)
		candidates = f.Apply(req, candidates)
		removed := prev - len(candidates)
		// Per-filter removal accounting keeps the filtered event's attribute
		// set fixed-cardinality (one counter per filter name). When a new
		// filter is added, extend this switch + the event attrs together.
		switch f.Name() {
		case "model_affinity":
			byModel += removed
		case "breaker_open":
			byBreaker += removed
		}
		if len(candidates) == 0 {
			break
		}
	}
	if before-len(candidates) > 0 {
		tracing.AddEvent(ctx, "completion.endpoints.filtered",
			attribute.Int("before", before),
			attribute.Int("after", len(candidates)),
			attribute.Int("by_model_affinity", byModel),
			attribute.Int("by_breaker_open", byBreaker),
		)
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
		candidates := s.applyFilters(ctx, req, snapshot)

		tracing.AddEvent(ctx, "completion.retry.attempt",
			attribute.Int("attempt", attempt),
			attribute.Int("candidates", len(candidates)),
			attribute.Int("tried_count", len(tried)),
			attribute.String("strategy", s.selector.Name()),
		)

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
			tracing.AddEvent(ctx, "completion.retry.no_eligible",
				attribute.Int("attempts_used", attempt),
				attribute.String("last_error_class", errorClass(lastErr)),
			)
			if lastErr != nil {
				return nil, fmt.Errorf("pool: exhausted after %d attempt(s): %w", attempt, lastErr)
			}
			return nil, errors.New("pool: no eligible endpoint")
		}
		tried[ep.Cfg.Name] = struct{}{}

		tracing.AddEvent(ctx, "completion.endpoint.selected",
			attribute.String("endpoint", ep.Cfg.Name),
			attribute.String("breaker_state", breakerStateName(ep.Breaker)),
			attribute.Int("attempt", attempt),
		)

		started := ep.Stats.start()
		ch, err := callEndpoint(ctx, ep, req)
		if err == nil {
			tracing.AddEvent(ctx, "completion.retry.succeeded",
				attribute.String("endpoint", ep.Cfg.Name),
				attribute.Int("attempts_used", attempt+1),
			)
			slog.InfoContext(ctx, "pool served request",
				"endpoint", ep.Cfg.Name, "attempt", attempt+1)
			return wrapChannelForStats(ep, started, ch), nil
		}
		ep.Stats.end(started, true)
		tracing.AddEvent(ctx, "completion.endpoint.failed",
			attribute.String("endpoint", ep.Cfg.Name),
			attribute.String("error_class", errorClass(err)),
			attribute.String("error_msg", tracing.TruncateErr(err, 200)),
			attribute.Int64("latency_ms", time.Since(started).Milliseconds()),
			attribute.Int("attempt", attempt),
		)
		slog.InfoContext(ctx, "pool pre-stream error",
			"endpoint", ep.Cfg.Name, "err", err, "attempt", attempt+1)
		lastErr = err
	}
	tracing.AddEvent(ctx, "completion.retry.exhausted",
		attribute.Int("attempts_used", s.maxAttempts),
		attribute.String("last_error_class", errorClass(lastErr)),
	)
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
		// Surface the synchronous breaker rejection paths as a distinct event.
		// Async open->half_open->closed transitions are NOT traced here — they
		// happen off the request path (gobreaker.OnStateChange).
		if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
			tracing.AddEvent(ctx, "completion.breaker.rejected",
				attribute.String("endpoint", ep.Cfg.Name),
				attribute.String("state", breakerStateName(ep.Breaker)),
				attribute.String("reason", errorClass(err)),
			)
		}
		return nil, err
	}
	ch, _ := res.(<-chan *completion.CompletionChunk)
	return ch, nil
}
