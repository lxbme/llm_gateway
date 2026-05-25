// Package metrics provides a process-wide Prometheus registry and the
// metrics shared across all llm_gateway services (gateway, completion, cache,
// embedding, auth, rag).
//
// LABEL CARDINALITY RULES
//
// Allowed labels (small, bounded sets):
//   - method        gRPC method name
//   - code          gRPC status code
//   - model         model name (limited by upstream pool config)
//   - endpoint      pool endpoint name (~10 entries max)
//   - outcome       enum: ok|error|canceled
//   - result        enum: hit|miss
//   - kind          enum: pre_stream|mid_stream
//   - path          gateway HTTP route path (whitelisted; today only /v1/chat/completions)
//   - status        HTTP status code (small int range)
//
// FORBIDDEN labels (high or unbounded cardinality, or PII):
//   - prompt / question / message body
//   - user_id / subject / bearer_token
//   - request_id / trace_id (those belong in traces/logs, never as a metric label)
//   - any raw user input or upstream response content
package metrics

import (
	"net/http"
	"time"

	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry is the process-wide Prometheus registry. All collectors register
// here, not on the global DefaultRegisterer, to keep ownership explicit.
var Registry = prometheus.NewRegistry()

// GRPCServer / GRPCClient are the gRPC interceptor metric collectors. They
// auto-track per-method counters and (with the histogram option enabled)
// per-method handling-time histograms.
var (
	GRPCServer = grpcprom.NewServerMetrics(
		grpcprom.WithServerHandlingTimeHistogram(),
	)
	GRPCClient = grpcprom.NewClientMetrics(
		grpcprom.WithClientHandlingTimeHistogram(),
	)
)

// HTTP entry metrics (gateway only).
var (
	HTTPRequestsTotal = promauto.With(Registry).NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_http_requests_total",
			Help: "Total HTTP requests handled by the gateway, partitioned by path and status code.",
		},
		[]string{"path", "status"},
	)

	HTTPDurationSec = promauto.With(Registry).NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "gateway_http_duration_seconds",
			Help:    "End-to-end HTTP request duration in seconds. Streams are timed until the SSE connection closes.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60, 120, 300},
		},
		[]string{"path"},
	)

	HTTPInFlight = promauto.With(Registry).NewGauge(
		prometheus.GaugeOpts{
			Name: "gateway_http_in_flight",
			Help: "HTTP requests currently being served by the gateway.",
		},
	)
)

// Gateway business metrics: cache lookups (semantic cache hit/miss as seen
// from the gateway-side stage). Backed by the gateway's cache stage; not the
// cache service itself, to keep hit/miss semantics in the call site that
// actually has the prompt key.
var (
	CacheLookupTotal = promauto.With(Registry).NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_cache_lookup_total",
			Help: "Total cache lookups performed by the gateway, partitioned by result.",
		},
		[]string{"result"}, // hit | miss | error
	)

	CacheLookupLatencySec = promauto.With(Registry).NewHistogram(
		prometheus.HistogramOpts{
			Name:    "gateway_cache_lookup_duration_seconds",
			Help:    "Cache lookup latency in seconds, observed from the gateway side.",
			Buckets: prometheus.DefBuckets,
		},
	)
)

// Upstream stream-level errors. Mid-stream errors are particularly interesting
// because they cannot be auto-retried by the pool (the channel has already
// been returned upstream).
var (
	UpstreamStreamErrors = promauto.With(Registry).NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_upstream_stream_errors_total",
			Help: "Upstream stream errors split by where in the lifecycle they occurred.",
		},
		[]string{"kind"}, // pre_stream | mid_stream
	)
)

func init() {
	Registry.MustRegister(GRPCServer, GRPCClient)
	Registry.MustRegister(collectors.NewGoCollector())
	Registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}

// Serve starts a minimal HTTP server that exposes /metrics (Prometheus
// scrape format) and /healthz on addr. It is intended to be called from a
// goroutine in each service's main(); a failure here must NOT crash the
// business server, so callers should log-only and not propagate.
func Serve(addr string) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(Registry, promhttp.HandlerOpts{Registry: Registry}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv.ListenAndServe()
}
