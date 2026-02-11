package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// Service implements embedding.Service using OpenAI API
type Service struct {
	endpoint   string
	model      string
	apiKeyEnv  string
	client     *http.Client
	dimensions int
}

// New creates a new OpenAI embedding service
func New(endpoint string, model string, apiKeyEnvName string, dimensions int) *Service {
	return &Service{
		endpoint:   endpoint,
		model:      model,
		apiKeyEnv:  apiKeyEnvName,
		client:     &http.Client{},
		dimensions: dimensions,
	}
}

// Get implements embedding.Service
func (s *Service) Get(ctx context.Context, question string) ([]float32, error) {
	return s.getEmbedding(question)
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
	apiKey := os.Getenv(s.apiKeyEnv)
	if apiKey == "" {
		return nil, fmt.Errorf("empty api key from env: %s", s.apiKeyEnv)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
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
