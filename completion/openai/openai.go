package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"llm_gateway/completion"
	"llm_gateway/internal/tracing"
	"net/http"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
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

	// upstream.http span covers only the HTTP request/response-header phase.
	// We end it before the SSE goroutine starts so the trace timeline isn't
	// distorted by a span that lasts for the whole stream (can be 30s+).
	httpCtx, httpSpan := tracing.Tracer("completion.openai").Start(ctx, "completion.upstream.http")
	upstreamReq = upstreamReq.WithContext(httpCtx)
	resp, err := s.client.Do(upstreamReq)
	if err != nil {
		httpSpan.RecordError(err)
		httpSpan.SetStatus(codes.Error, "upstream call failed")
		httpSpan.End()
		return nil, fmt.Errorf("fail to call upstream api: %w", err)
	}
	httpSpan.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))

	if resp.StatusCode != http.StatusOK {
		httpSpan.SetStatus(codes.Error, fmt.Sprintf("upstream status %d", resp.StatusCode))
		httpSpan.End()
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("upstream api returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}
	httpSpan.End()

	// Time-to-first-byte span — measures latency from finishing the HTTP call
	// to first SSE data line. Ends inside the goroutine on first chunk or on
	// error/EOF (whichever comes first).
	_, ttfbSpan := tracing.Tracer("completion.openai").Start(ctx, "completion.upstream.sse.first_byte")

	ch := make(chan *completion.CompletionChunk, 10)

	// parse SSE and add content to channel
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		reader := bufio.NewReader(resp.Body)
		var totalTokens, promptTokens, completionTokens int
		ttfbEnded := false
		endTTFB := func(success bool) {
			if ttfbEnded {
				return
			}
			ttfbEnded = true
			if !success {
				ttfbSpan.SetStatus(codes.Error, "stream ended before first chunk")
			}
			ttfbSpan.End()
		}
		defer endTTFB(false)

		for {
			// Check if context is cancelled
			select {
			case <-ctx.Done():
				ch <- &completion.CompletionChunk{
					Content:          "",
					Error:            ctx.Err(),
					Done:             true,
					TokenUsage:       totalTokens,
					PromptTokens:     promptTokens,
					CompletionTokens: completionTokens,
				}
				return
			default:
			}

			line, err := reader.ReadBytes('\n')
			if err != nil {
				if err == io.EOF {
					// Stream completed successfully
					ch <- &completion.CompletionChunk{
						Content:          "",
						Error:            nil,
						Done:             true,
						TokenUsage:       totalTokens,
						PromptTokens:     promptTokens,
						CompletionTokens: completionTokens,
					}
					return
				}
				// Read error
				ch <- &completion.CompletionChunk{
					Content:          "",
					Error:            fmt.Errorf("failed to read from upstream: %w", err),
					Done:             true,
					TokenUsage:       totalTokens,
					PromptTokens:     promptTokens,
					CompletionTokens: completionTokens,
				}
				return
			}

			// Skip blank lines
			if len(line) == 0 || line[0] == '\n' || line[0] == '\r' {
				continue
			}

			// Parse SSE line
			content, done, pt, ct, tt, err := s.parseSSELine(line)
			if err != nil {
				// Non-fatal parse error, continue
				continue
			}

			// Update token counters as they arrive
			if tt > 0 {
				totalTokens = tt
			}
			if pt > 0 {
				promptTokens = pt
			}
			if ct > 0 {
				completionTokens = ct
			}

			if content != "" {
				endTTFB(true)
				ch <- &completion.CompletionChunk{
					Content:    content,
					Error:      nil,
					Done:       false,
					TokenUsage: 0,
				}
			}

			if done {
				ch <- &completion.CompletionChunk{
					Content:          "",
					Error:            nil,
					Done:             true,
					TokenUsage:       totalTokens,
					PromptTokens:     promptTokens,
					CompletionTokens: completionTokens,
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
		// Ask upstream to emit a final SSE chunk containing usage info.
		// OpenAI's streaming API omits usage by default — without this flag
		// the gateway can never forward token counts to the client.
		StreamOptions: &StreamOptions{IncludeUsage: true},
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

// parseSSELine parses a single SSE line and returns
// (content, isDone, promptTokens, completionTokens, totalTokens, error).
func (s *OpenaiCompletionService) parseSSELine(line []byte) (string, bool, int, int, int, error) {
	if len(line) == 0 {
		return "", false, 0, 0, 0, nil
	}

	// Check for "data: " prefix
	if !bytes.HasPrefix(line, []byte("data: ")) {
		return "", false, 0, 0, 0, fmt.Errorf("invalid SSE line, missing 'data: ' prefix")
	}

	// Extract JSON part
	jsonBytes := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data: ")))

	// Check for [DONE] marker
	if bytes.Equal(jsonBytes, []byte("[DONE]")) {
		return "", true, 0, 0, 0, nil
	}

	// Parse JSON response
	var resp ChatStreamResponse
	if err := json.Unmarshal(jsonBytes, &resp); err != nil {
		return "", false, 0, 0, 0, fmt.Errorf("fail to unmarshal SSE json: %w", err)
	}

	// Handle usage info (last block before [DONE] when stream_options.include_usage is set)
	if resp.Usage != nil && (resp.Usage.TotalTokens != 0 || resp.Usage.PromptTokens != 0 || resp.Usage.CompletionTokens != 0) {
		return "", false, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens, nil
	}

	// Extract content from choices
	if len(resp.Choices) == 0 {
		return "", false, 0, 0, 0, nil
	}

	choice := resp.Choices[0]

	// finish_reason marks the end of generated content, but the upstream may
	// still emit one more chunk carrying usage info when stream_options
	// include_usage=true is set. We rely on either [DONE] or EOF (handled by
	// the caller) to terminate; finish_reason alone is not a terminator.
	if choice.FinishReason != "" {
		return "", false, 0, 0, 0, nil
	}

	return choice.Delta.Content, false, 0, 0, 0, nil
}
