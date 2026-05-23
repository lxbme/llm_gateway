package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"llm_gateway/embedding"
)

// Service implements embedding.Service using Ollama's /api/embed endpoint.
type Service struct {
	endpoint   string
	model      string
	apiKey     string
	client     *http.Client
	dimensions int
}

type Config struct {
	Endpoint   string
	Model      string
	APIKey     string
	Dimensions string
}

func LoadConfigFromEnv() Config {
	return Config{
		Endpoint:   os.Getenv("EMBED_ENDPOINT"),
		Model:      os.Getenv("EMBED_MODEL"),
		APIKey:     os.Getenv("EMBED_API_KEY"),
		Dimensions: os.Getenv("EMBED_DIMENSIONS"),
	}
}

func NewFromEnv() (*Service, error) {
	return New(LoadConfigFromEnv())
}

// New constructs an Ollama embedding service and probes the configured model
// once so dimension mismatches surface at startup rather than first request.
func New(cfg Config) (*Service, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("EMBED_ENDPOINT should not be blank")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("EMBED_MODEL should not be blank")
	}
	if cfg.Dimensions == "" {
		return nil, fmt.Errorf("EMBED_DIMENSIONS should not be blank")
	}
	dimInt, err := strconv.Atoi(cfg.Dimensions)
	if err != nil {
		return nil, fmt.Errorf("EMBED_DIMENSIONS must be a valid integer: %w", err)
	}

	s := &Service{
		endpoint:   cfg.Endpoint,
		model:      cfg.Model,
		apiKey:     cfg.APIKey,
		client:     &http.Client{Timeout: 30 * time.Second},
		dimensions: dimInt,
	}

	probeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	probe, err := s.fetchEmbedding(probeCtx, "warmup")
	if err != nil {
		return nil, fmt.Errorf("ollama: probe failed: %w", err)
	}
	if len(probe) != dimInt {
		return nil, fmt.Errorf("ollama: declared dimensions=%d but model %s returned %d",
			dimInt, cfg.Model, len(probe))
	}
	fmt.Printf("[Info] Ollama embedding ready: model=%s dim=%d\n", cfg.Model, dimInt)

	return s, nil
}

func (s *Service) Get(ctx context.Context, question string) ([]float32, error) {
	return s.fetchEmbedding(ctx, question)
}

func (s *Service) Info(ctx context.Context) (embedding.Info, error) {
	_ = ctx
	return embedding.Info{
		Provider:   "ollama",
		Model:      s.model,
		Dimensions: s.dimensions,
	}, nil
}

func (s *Service) fetchEmbedding(ctx context.Context, input string) ([]float32, error) {
	body, err := json.Marshal(EmbedRequest{Model: s.model, Input: input})
	if err != nil {
		return nil, fmt.Errorf("fail to marshal embedding request body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.endpoint, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("fail to create embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if s.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fail to do embedding request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding request fail: (%d) %s", resp.StatusCode, respBody)
	}
	if err != nil {
		return nil, fmt.Errorf("fail to read embedding response body: %w", err)
	}

	var decoded EmbedResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("fail to unmarshal embedding response: %w", err)
	}
	if len(decoded.Embeddings) == 0 || len(decoded.Embeddings[0]) == 0 {
		return nil, fmt.Errorf("empty embedding response")
	}
	return decoded.Embeddings[0], nil
}
