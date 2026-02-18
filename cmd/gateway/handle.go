package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"llm_gateway/cache"
	"llm_gateway/completion"
)

func CompletionHandle(w http.ResponseWriter, r *http.Request) {
	// handle browser CORS Preflight Request
	if r.Method == http.MethodOptions {
		requestedHeaders := r.Header.Get("Access-Control-Request-Headers")

		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")

		if requestedHeaders != "" {
			w.Header().Set("Access-Control-Allow-Headers", requestedHeaders)
		} else {
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}

		w.Header().Set("Access-Control-Max-Age", "86400")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	fmt.Printf("[Info] Received request: %s %s\n", r.Method, r.URL.Path)
	fmt.Printf("[Info] Content-Type: %s\n", r.Header.Get("Content-Type"))
	// fmt.Printf("[Info] Content-Length: %d\n", r.ContentLength)

	// process mock
	if r.Header.Get("x-mock") == "true" {
		fmt.Printf("[Info] x-mock: true\n")
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming not supported", http.StatusInternalServerError)
			return
		}
		for i := 0; i < 10; i++ {
			w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"mock\"}}]}\n\n"))
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
		return
	}

	// parse user request (TODO: json.Unmarshal might bottleneck performance here)
	var userReq ChatCompleteionRequest
	if err := BindJSON(r, &userReq); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		errorResponse, _ := json.Marshal(map[string]string{"error": "Failed to parse user request"})
		w.Write(errorResponse)
		fmt.Printf("[Error] Failed to parse user request: %s\n", err)
		return
	}

	// record user prompt for terminal printing
	var userPrompt string
	for _, message := range userReq.Messages {
		userPrompt += message.Content + " "
	}
	fmt.Printf("[Info] Parsed request: model=%s, stream=%v, messages=%d\n", userReq.Model, userReq.Stream, len(userReq.Messages))

	// queue for cache answer
	cacheAnswer, isHit, err := semanticCacheService.Get(r.Context(), userPrompt, userReq.Model)
	if err != nil {
		fmt.Printf("[Error] Failed to search similar vector in qdrant: %s\n", err)
	}
	if isHit {
		returnCachedAnswer(w, cacheAnswer, userReq.Model)
		PrintDialog(userPrompt, cacheAnswer)
		return
	}

	// Build completion request
	completionReq := &completion.CompletionRequest{
		Model:       userReq.Model,
		Question:    userPrompt,
		Temperature: userReq.Temperature,
		MaxTokens:   userReq.MaxTokens,
	}

	// Get stream from completion service
	chunks, err := completionService.GetStream(r.Context(), completionReq)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		errorResponse, _ := json.Marshal(map[string]string{"error": "Failed to get stream"})
		w.Write(errorResponse)
		fmt.Printf("[Error] Failed to get stream: %s\n", err)
		return
	}

	// Send back streaming response to client
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	fullAnswerBuffer := strings.Builder{}
	var totalTokens int

	// Read from channel and send to client
	for chunk := range chunks {
		// Check if client has disconnected
		select {
		case <-r.Context().Done():
			fmt.Printf("[Info] Client disconnected, stopping stream\n")
			return
		default:
		}

		// Handle errors
		if chunk.Error != nil {
			fmt.Printf("[Error] Stream error: %s\n", chunk.Error)
			break
		}

		// Accumulate content
		if chunk.Content != "" {
			fullAnswerBuffer.WriteString(chunk.Content)

			// Build and send SSE response
			response := ChatStreamResponse{
				Choices: []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				}{
					{
						Delta: struct {
							Content string `json:"content"`
						}{Content: chunk.Content},
						FinishReason: "",
					},
				},
				Usage: nil,
			}

			jsonBytes, _ := json.Marshal(response)
			fmt.Fprintf(w, "data: %s\n\n", string(jsonBytes))
			flusher.Flush()
		}

		// Handle stream completion
		if chunk.Done {
			totalTokens = chunk.TokenUsage
			fmt.Printf("[Info] Stream completed successfully\n")

			// Send finish message
			finishResponse := map[string]interface{}{
				"id":      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   userReq.Model,
				"choices": []map[string]interface{}{
					{
						"index":         0,
						"delta":         map[string]string{},
						"finish_reason": "stop",
					},
				},
			}
			finishJSON, _ := json.Marshal(finishResponse)
			fmt.Fprintf(w, "data: %s\n\n", string(finishJSON))
			fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			break
		}
	}

	PrintDialog(userPrompt, fullAnswerBuffer.String())
	// cache
	semanticCacheService.Set(r.Context(), cache.Task{
		UserPrompt: userPrompt,
		AIResponse: fullAnswerBuffer.String(),
		ModelName:  userReq.Model,
		TokenUsage: totalTokens,
	})
}

func PrintDialog(userText string, answerText string) {
	if len(userText) > 100 {
		fmt.Printf("user: ...%s\n", strings.ReplaceAll(strings.ReplaceAll(userText[len(userText)-100:], "\n", ""), " ", ""))
	} else {
		fmt.Printf("user: %s\n", userText)
	}
	fmt.Printf("ai: %.100s...\n", answerText)
}

// returnCachedAnswer simulate sse
func returnCachedAnswer(w http.ResponseWriter, cachedAnswer string, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// generate dialog id
	chatID := fmt.Sprintf("chatcmpl-cached-%d", time.Now().UnixNano())
	createdTime := time.Now().Unix()

	// split cache answer
	chunkSize := 20
	runes := []rune(cachedAnswer)

	for i := 0; i < len(runes); i += chunkSize {
		end := i + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunk := string(runes[i:end])

		// make response
		response := ChatStreamResponse{
			Choices: []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			}{
				{
					Delta: struct {
						Content string `json:"content"`
					}{Content: chunk},
					FinishReason: "",
				},
			},
			Usage: nil,
		}

		jsonBytes, _ := json.Marshal(response)
		fmt.Fprintf(w, "data: %s\n\n", string(jsonBytes))
		flusher.Flush()

		// time.Sleep(10 * time.Millisecond)
	}

	// stop
	finishResponse := map[string]interface{}{
		"id":      chatID,
		"object":  "chat.completion.chunk",
		"created": createdTime,
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"delta":         map[string]string{},
				"finish_reason": "stop",
			},
		},
	}
	finishJSON, _ := json.Marshal(finishResponse)
	fmt.Fprintf(w, "data: %s\n\n", string(finishJSON))
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}
