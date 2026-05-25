package pool

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/sony/gobreaker"
)

type BreakerConfig struct {
	Enabled      bool    `json:"enabled"`
	MaxRequests  uint32  `json:"max_requests"`
	Interval     string  `json:"interval"`
	Timeout      string  `json:"timeout"`
	FailureRatio float64 `json:"failure_ratio"`
	MinRequests  uint32  `json:"min_requests"`
}

const (
	defaultBreakerMaxRequests  uint32  = 1
	defaultBreakerInterval             = 60 * time.Second
	defaultBreakerTimeout              = 30 * time.Second
	defaultBreakerFailureRatio float64 = 0.5
	defaultBreakerMinRequests  uint32  = 5
)

func (c BreakerConfig) resolved() (uint32, time.Duration, time.Duration, float64, uint32, error) {
	maxReq := c.MaxRequests
	if maxReq == 0 {
		maxReq = defaultBreakerMaxRequests
	}
	interval := defaultBreakerInterval
	if c.Interval != "" {
		d, err := time.ParseDuration(c.Interval)
		if err != nil {
			return 0, 0, 0, 0, 0, fmt.Errorf("breaker.interval: %w", err)
		}
		interval = d
	}
	timeout := defaultBreakerTimeout
	if c.Timeout != "" {
		d, err := time.ParseDuration(c.Timeout)
		if err != nil {
			return 0, 0, 0, 0, 0, fmt.Errorf("breaker.timeout: %w", err)
		}
		timeout = d
	}
	ratio := c.FailureRatio
	if ratio <= 0 || ratio > 1 {
		ratio = defaultBreakerFailureRatio
	}
	minReq := c.MinRequests
	if minReq == 0 {
		minReq = defaultBreakerMinRequests
	}
	return maxReq, interval, timeout, ratio, minReq, nil
}

func newBreaker(endpointName string, cfg BreakerConfig) (*gobreaker.CircuitBreaker, error) {
	maxReq, interval, timeout, ratio, minReq, err := cfg.resolved()
	if err != nil {
		return nil, err
	}
	settings := gobreaker.Settings{
		Name:        endpointName,
		MaxRequests: maxReq,
		Interval:    interval,
		Timeout:     timeout,
		ReadyToTrip: func(c gobreaker.Counts) bool {
			if c.Requests < minReq {
				return false
			}
			failures := float64(c.TotalFailures) / float64(c.Requests)
			return failures >= ratio
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			// Async callback — no request ctx is reachable here, so the log
			// record carries no trace_id. That's expected: state transitions
			// happen outside any specific request.
			slog.Info("breaker state transition",
				"endpoint", name,
				"from", from.String(),
				"to", to.String(),
			)
		},
	}
	return gobreaker.NewCircuitBreaker(settings), nil
}

func breakerStateName(b *gobreaker.CircuitBreaker) string {
	if b == nil {
		return "disabled"
	}
	switch b.State() {
	case gobreaker.StateClosed:
		return "closed"
	case gobreaker.StateHalfOpen:
		return "half_open"
	case gobreaker.StateOpen:
		return "open"
	default:
		return "unknown"
	}
}
