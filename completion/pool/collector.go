package pool

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"
)

// Collector exposes the per-endpoint pool stats already tracked in
// endpointStats as Prometheus metrics. It reuses Service.PoolStats() so the
// RLock / breakerStateName logic lives in exactly one place.
type Collector struct {
	svc *Service

	descSuccess      *prometheus.Desc
	descFailure      *prometheus.Desc
	descInFlight     *prometheus.Desc
	descEWMAms       *prometheus.Desc
	descBreakerState *prometheus.Desc
}

func NewCollector(svc *Service) *Collector {
	labels := []string{"endpoint"}
	return &Collector{
		svc: svc,
		descSuccess: prometheus.NewDesc(
			"completion_pool_success_total",
			"Total successful upstream completion calls per endpoint.",
			labels, nil,
		),
		descFailure: prometheus.NewDesc(
			"completion_pool_failure_total",
			"Total failed upstream completion calls per endpoint.",
			labels, nil,
		),
		descInFlight: prometheus.NewDesc(
			"completion_pool_in_flight",
			"In-flight upstream completion calls per endpoint.",
			labels, nil,
		),
		descEWMAms: prometheus.NewDesc(
			"completion_pool_ewma_latency_ms",
			"EWMA of recent upstream call latency in milliseconds per endpoint.",
			labels, nil,
		),
		descBreakerState: prometheus.NewDesc(
			"completion_pool_breaker_state",
			"Circuit-breaker state per endpoint: -1=disabled, 0=closed, 1=half_open, 2=open.",
			labels, nil,
		),
	}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.descSuccess
	ch <- c.descFailure
	ch <- c.descInFlight
	ch <- c.descEWMAms
	ch <- c.descBreakerState
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	snaps, err := c.svc.PoolStats(context.Background())
	if err != nil {
		return
	}
	for _, s := range snaps {
		ch <- prometheus.MustNewConstMetric(c.descSuccess, prometheus.CounterValue, float64(s.Success), s.Endpoint)
		ch <- prometheus.MustNewConstMetric(c.descFailure, prometheus.CounterValue, float64(s.Failure), s.Endpoint)
		ch <- prometheus.MustNewConstMetric(c.descInFlight, prometheus.GaugeValue, float64(s.InFlight), s.Endpoint)
		ch <- prometheus.MustNewConstMetric(c.descEWMAms, prometheus.GaugeValue, s.LatencyMs, s.Endpoint)
		ch <- prometheus.MustNewConstMetric(c.descBreakerState, prometheus.GaugeValue, breakerStateNum(s.BreakerState), s.Endpoint)
	}
}

func breakerStateNum(state string) float64 {
	switch state {
	case "closed":
		return 0
	case "half_open":
		return 1
	case "open":
		return 2
	default:
		return -1
	}
}
