package completion

import "context"

type Service interface {
	GetStream(ctx context.Context, req *CompletionRequest) (completionStream <-chan *CompletionChunk, err error)
}

// EndpointStatsSnapshot is a transport-neutral view of one upstream endpoint's runtime stats.
type EndpointStatsSnapshot struct {
	Endpoint     string  `json:"endpoint"`
	Weight       int     `json:"weight"`
	Enabled      bool    `json:"enabled"`
	InFlight     int64   `json:"in_flight"`
	Success      uint64  `json:"success"`
	Failure      uint64  `json:"failure"`
	SuccessRate  float64 `json:"success_rate"`
	LatencyMs    float64 `json:"latency_ms_ewma"`
	BreakerState string  `json:"breaker_state"`
}

// StatsProvider is implemented by anything that can report per-endpoint stats — both
// the in-process pool.Service (on the server side) and the gRPC client (on the gateway side).
type StatsProvider interface {
	PoolStats(ctx context.Context) ([]EndpointStatsSnapshot, error)
}

// EndpointSpec is the transport-neutral shape for runtime endpoint additions.
type EndpointSpec struct {
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	APIKeyEnv string   `json:"api_key_env"`
	Weight    int      `json:"weight"`
	Models    []string `json:"models,omitempty"`
	Enabled   bool     `json:"enabled"`
}

// EndpointView is the read-side of EndpointSpec plus current breaker state.
type EndpointView struct {
	Name         string   `json:"name"`
	URL          string   `json:"url"`
	APIKeyEnv    string   `json:"api_key_env"`
	Weight       int      `json:"weight"`
	Models       []string `json:"models,omitempty"`
	Enabled      bool     `json:"enabled"`
	BreakerState string   `json:"breaker_state"`
}

// Admin is the runtime-management contract on the completion-service upstream pool.
// Mutations affect only the receiving replica's in-memory state — see docs.
type Admin interface {
	ListEndpoints(ctx context.Context) ([]EndpointView, error)
	AddEndpoint(ctx context.Context, spec EndpointSpec) error
	RemoveEndpoint(ctx context.Context, name string) error
	Reweight(ctx context.Context, name string, weight int) error
	SetEnabled(ctx context.Context, name string, enabled bool) error
	ResetBreaker(ctx context.Context, name string) error
}
