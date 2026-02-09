package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
)

func BuildUpstreamRequest(ctx context.Context, original_req *ChatCompleteionRequest) (*http.Request, error) {
	original_req.Stream = true
	// original_req.StreamOptions = map[string]bool{"include_usage": true}
	reqBodyBytes, err := json.Marshal(original_req)
	if err != nil {
		return nil, fmt.Errorf("Fail to marshal original_req: %s", err)
	}
	gptUpstreamURL := "https://api.openai-proxy.org/v1/chat/completions"

	req, err := http.NewRequestWithContext(ctx, "POST", gptUpstreamURL, bytes.NewBuffer(reqBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("Fail to create request: %s", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	apiKey := os.Getenv("OPENAI_API_KEY")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	return req, nil
}

func BindJSON(r *http.Request, obj interface{}) error {
	if r.Body == nil {
		return errors.New("request body is empty")
	}
	defer r.Body.Close()

	decoder := json.NewDecoder(r.Body)
	// 暂时禁用此选项以便更宽松地解析
	// decoder.DisallowUnknownFields()

	err := decoder.Decode(obj)
	if err != nil {
		return fmt.Errorf("json decode error: %w", err)
	}

	return nil
}

func ParseSSELine(line []byte) (string, error) {
	if len(line) == 0 {
		return "", nil
	}
	if !bytes.HasPrefix(line, []byte("data: ")) {
		return "", fmt.Errorf("invalid SSE line, has no prefix \"data: \"")
	}
	jsonBytes := bytes.TrimPrefix(line, []byte("data: "))
	if bytes.Equal(jsonBytes, []byte("[DONE]")) {
		return "", nil
	}
	var resp_struct ChatStreamResponse
	if err := json.Unmarshal(jsonBytes, &resp_struct); err != nil {
		return "", fmt.Errorf("fail to unmarshal SSE json part: %s", err)
	}
	if len(resp_struct.Choices) == 0 {
		return "", nil
	}
	if resp_struct.Choices[0].FinishReason != "" {
		return "", nil
	}
	return resp_struct.Choices[0].Delta.Content, nil
}
