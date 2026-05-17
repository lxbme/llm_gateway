package pool

import (
	"context"

	"github.com/sony/gobreaker"

	"llm_gateway/completion"
)

type EndpointConfig struct {
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	APIKeyEnv string   `json:"api_key_env"`
	Weight    int      `json:"weight"`
	Models    []string `json:"models,omitempty"`
	Enabled   bool     `json:"enabled"`
}

type upstreamClient interface {
	GetStream(ctx context.Context, req *completion.CompletionRequest) (<-chan *completion.CompletionChunk, error)
}

type Endpoint struct {
	Cfg     EndpointConfig
	Client  upstreamClient
	Breaker *gobreaker.CircuitBreaker
	Stats   *endpointStats
}
