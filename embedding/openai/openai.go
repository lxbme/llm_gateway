package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"llm_gateway/embedding"
)

// Service implements embedding.Service using OpenAI API
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
	Dimensions int
}

// New creates a new OpenAI embedding service
func New(cfg Config) *Service {
	return &Service{
		endpoint:   cfg.Endpoint,
		model:      cfg.Model,
		apiKey:     cfg.APIKey,
		client:     &http.Client{},
		dimensions: cfg.Dimensions,
	}
}

// Get implements embedding.Service
func (s *Service) Get(ctx context.Context, question string) ([]float32, error) {
	return s.getEmbedding(question)
}

func (s *Service) Info(ctx context.Context) (embedding.Info, error) {
	_ = ctx
	return embedding.Info{
		Provider:   "openai",
		Model:      s.model,
		Dimensions: s.dimensions,
	}, nil
}

// getEmbedding gets embedding vector from OpenAI API
func (s *Service) getEmbedding(input string) ([]float32, error) {
	requestBody := EmbeddingRequest{
		Model:          s.model,
		Input:          input,
		EncodingFormat: "float",
		Dimensions:     int32(s.dimensions),
	}
	requestBodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("fail to marshal embedding request body: %w", err)
	}
	req, err := http.NewRequest("POST", s.endpoint, bytes.NewBuffer(requestBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("fail to create embedding request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fail to do embedding request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding request fail: (%d) %s", resp.StatusCode, body)
	}
	if err != nil {
		return nil, fmt.Errorf("fail to read embedding response body: %w", err)
	}
	var respBody EmbeddingResponse
	if err := json.Unmarshal(body, &respBody); err != nil {
		return nil, fmt.Errorf("fail to unmarshal embedding response: %w", err)
	}
	if len(respBody.Data) == 0 {
		return nil, fmt.Errorf("empty embedding response data")
	}
	return respBody.Data[0].Embedding, nil
}
