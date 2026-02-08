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
	decoder.DisallowUnknownFields()

	err := decoder.Decode(obj)
	if err != nil {
		return err
	}

	return nil
}
