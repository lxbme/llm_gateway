package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"llm_gateway/completion"
	"net/http"
	"os"
	"time"
)

type OpenaiCompletionService struct {
	client        *http.Client
	endpoint      string
	apiKeyEnvName string
}

func New(endpoint string, apiKeyEnvName string) *OpenaiCompletionService {
	return &OpenaiCompletionService{
		client:        &http.Client{Timeout: time.Second * 30},
		endpoint:      endpoint,
		apiKeyEnvName: apiKeyEnvName,
	}
}

func (s *OpenaiCompletionService) GetStream(ctx context.Context, req *completion.CompletionRequest) (<-chan *completion.CompletionChunk, error) {
	upstreamReq, err := s.buildUpstreamRequest(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("fail to build upstream request: %w", err)
	}

	resp, err := s.client.Do(upstreamReq)
	if err != nil {
		return nil, fmt.Errorf("fail to call upstream api: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("upstream api returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	ch := make(chan *completion.CompletionChunk, 10)

	// parse SSE and add content to channel
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		reader := bufio.NewReader(resp.Body)
		var totalTokens int

		for {
			// Check if context is cancelled
			select {
			case <-ctx.Done():
				ch <- &completion.CompletionChunk{
					Content:    "",
					Error:      ctx.Err(),
					Done:       true,
					TokenUsage: totalTokens,
				}
				return
			default:
			}

			line, err := reader.ReadBytes('\n')
			if err != nil {
				if err == io.EOF {
					// Stream completed successfully
					ch <- &completion.CompletionChunk{
						Content:    "",
						Error:      nil,
						Done:       true,
						TokenUsage: totalTokens,
					}
					return
				}
				// Read error
				ch <- &completion.CompletionChunk{
					Content:    "",
					Error:      fmt.Errorf("failed to read from upstream: %w", err),
					Done:       true,
					TokenUsage: totalTokens,
				}
				return
			}

			// Skip blank lines
			if len(line) == 0 || line[0] == '\n' || line[0] == '\r' {
				continue
			}

			// Parse SSE line
			content, done, tokenUsage, err := s.parseSSELine(line)
			if err != nil {
				// Non-fatal parse error, continue
				continue
			}

			// Update total tokens if received
			if tokenUsage > 0 {
				totalTokens = tokenUsage
			}

			if content != "" {
				ch <- &completion.CompletionChunk{
					Content:    content,
					Error:      nil,
					Done:       false,
					TokenUsage: 0,
				}
			}

			if done {
				ch <- &completion.CompletionChunk{
					Content:    "",
					Error:      nil,
					Done:       true,
					TokenUsage: totalTokens,
				}
				return
			}
		}
	}()

	return ch, nil
}

func (s *OpenaiCompletionService) buildUpstreamRequest(ctx context.Context, original_req *completion.CompletionRequest) (*http.Request, error) {
	// build openai api format request
	openaiReq := ChatCompleteionRequest{
		Model: original_req.Model,
		Messages: []Message{
			{
				Role:    "user",
				Content: original_req.Question,
			},
		},
		Temperature: original_req.Temperature,
		MaxTokens:   original_req.MaxTokens,
		Stream:      true,
	}

	reqBodyBytes, err := json.Marshal(openaiReq)
	if err != nil {
		return nil, fmt.Errorf("fail to marshal openai request: %w", err)
	}

	fmt.Printf("[Debug] Sending to OpenAI: %s\n", string(reqBodyBytes))

	req, err := http.NewRequestWithContext(ctx, "POST", s.endpoint, bytes.NewBuffer(reqBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("fail to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	apiKey := os.Getenv(s.apiKeyEnvName)
	req.Header.Set("Authorization", "Bearer "+apiKey)

	return req, nil
}

// parseSSELine parses a single SSE line and returns (content, isDone, tokenUsage, error)
func (s *OpenaiCompletionService) parseSSELine(line []byte) (string, bool, int, error) {
	if len(line) == 0 {
		return "", false, 0, nil
	}

	// Check for "data: " prefix
	if !bytes.HasPrefix(line, []byte("data: ")) {
		return "", false, 0, fmt.Errorf("invalid SSE line, missing 'data: ' prefix")
	}

	// Extract JSON part
	jsonBytes := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data: ")))

	// Check for [DONE] marker
	if bytes.Equal(jsonBytes, []byte("[DONE]")) {
		return "", true, 0, nil
	}

	// Parse JSON response
	var resp ChatStreamResponse
	if err := json.Unmarshal(jsonBytes, &resp); err != nil {
		return "", false, 0, fmt.Errorf("fail to unmarshal SSE json: %w", err)
	}

	// Handle usage info (last block)
	if resp.Usage != nil && resp.Usage.TotalTokens != 0 {
		return "", false, resp.Usage.TotalTokens, nil
	}

	// Extract content from choices
	if len(resp.Choices) == 0 {
		return "", false, 0, nil
	}

	choice := resp.Choices[0]

	// Check if stream is finished
	if choice.FinishReason != "" {
		return "", true, 0, nil
	}

	return choice.Delta.Content, false, 0, nil
}
