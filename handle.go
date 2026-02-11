package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"llm_gateway/cache"
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

	cacheAnswer, isHit, err := semanticCacheService.Get(r.Context(), userPrompt, userReq.Model)
	if err != nil {
		fmt.Printf("[Error] Failed to search similar vector in qdrant: %s", err)
	}
	if isHit {
		returnCachedAnswer(w, cacheAnswer, userReq.Model)
		PrintDialog(userPrompt, cacheAnswer)
		return
	}

	// build upstream request body from user request
	upstreamReq, err := BuildUpstreamRequest(r.Context(), &userReq)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		errorResponse, _ := json.Marshal(map[string]string{"error": "Failed to build request"})
		w.Write(errorResponse)
		fmt.Printf("[Error] Failed to build request: %s\n", err)
		return
	}

	// make upstream request
	client := &http.Client{}
	resp, err := client.Do(upstreamReq)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		errorResponse, _ := json.Marshal(map[string]string{"error": "Upstream unavailable"})
		w.Write(errorResponse)
		fmt.Printf("[Error] Upstream unavailable: %s\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Read Upstream error response
		bodyBytes, _ := io.ReadAll(resp.Body)
		w.WriteHeader(resp.StatusCode)
		w.Write(bodyBytes)
		fmt.Printf("[Error] Upstream returned status %d: %s\n", resp.StatusCode, string(bodyBytes))
		return
	}

	// send back upstream response to client
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

	reader := bufio.NewReader(resp.Body)
	writer, ok := w.(http.Flusher)

	fullAnswerBuffer := strings.Builder{}
	var totalTokens int = 0

	for {
		// Check if client has disconnected
		select {
		case <-r.Context().Done():
			fmt.Printf("[Info] Client disconnected, stopping stream\n")
			return
		default:
		}

		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				fmt.Printf("[Info] Stream completed successfully\n")
				break
			}
			fmt.Printf("[Error] Failed to read from upstream: %s\n", err)
			break
		}

		// skip parsing blank line
		if len(line) > 0 && line[0] != '\n' && line[0] != '\r' {
			answerString, totalTokensReceiver, err := ParseSSELine(line)
			if err != nil {
				fmt.Printf("[Error] Fail to parse sse line: %s\n", err)
			} else {
				if totalTokensReceiver != 0 {
					totalTokens = totalTokensReceiver
				}
				fullAnswerBuffer.Write([]byte(answerString))
			}
		}

		_, writeErr := w.Write(line)
		if writeErr != nil {
			fmt.Printf("[Info] Client disconnected while writing: %s\n", writeErr)
			return
		}
		if ok {
			writer.Flush()
		}
	}

	PrintDialog(userPrompt, fullAnswerBuffer.String())
	// cache
	semanticCacheService.Set(r.Context(), cache.Task{
		CollectionName: qdrantCollectionName,
		UserPrompt:     userPrompt,
		AIResponse:     fullAnswerBuffer.String(),
		Dimension:      embeddingDimensions,
		ModelName:      userReq.Model,
		TokenUsage:     totalTokens,
	})
}

func PrintDialog(userText string, answerText string) {
	if len(userText) > 100 {
		fmt.Printf("user: ...%s\n", strings.ReplaceAll(strings.ReplaceAll(userText[len(userText)-100:], "\n", ""), " ", ""))
	} else {
		fmt.Printf("user: %s\n", userText)
	}
	fmt.Printf("ai: %.100s\n", answerText)
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
