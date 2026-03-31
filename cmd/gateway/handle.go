package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"llm_gateway/completion"
)

var gatewayPipeline = defaultGatewayPipeline()

func CompletionHandle(w http.ResponseWriter, r *http.Request) {
	logDebug("Received request: %s %s", r.Method, r.URL.Path)

	gw := NewGatewayContext(w, r, GatewayServices{
		Auth:       authService,
		Cache:      semanticCacheService,
		Completion: completionService,
	})
	defer finishGatewayRequest(gw)

	if result, terminal := runPreUpstreamStages(gw); terminal {
		writeTerminalStageResponse(gw, result)
		return
	}

	chunks, err := gw.Services.Completion.GetStream(gw.Context, gw.Upstream.Request)
	if err != nil {
		logError("Failed to get stream: %s", err)
		gw.Upstream.Error = err
		gw.Response.DirectResponse = newJSONDirectResponse(
			http.StatusBadGateway,
			map[string]string{"error": "Failed to get stream"},
		)
		writeTerminalStageResponse(gw, StageResult{
			Action:     ActionReject,
			StatusCode: http.StatusBadGateway,
			Message:    "Failed to get stream",
			Err:        err,
		})
		return
	}

	if err := streamUpstreamResponse(gw, chunks); err != nil {
		if !gw.Response.StreamStarted {
			gw.Response.DirectResponse = newJSONDirectResponse(
				http.StatusInternalServerError,
				map[string]string{"error": err.Error()},
			)
			writeTerminalStageResponse(gw, StageResult{
				Action:     ActionReject,
				StatusCode: http.StatusInternalServerError,
				Message:    err.Error(),
				Err:        err,
			})
		}
	}
}

func runPreUpstreamStages(gw *GatewayContext) (StageResult, bool) {
	stages := []StageName{
		StageRequestReceived,
		StageRequestDecoded,
		StageBeforeUpstream,
	}
	for _, stage := range stages {
		result, terminal := gatewayPipeline.RunStage(stage, gw)
		if terminal {
			return result, true
		}
	}
	return StageResult{Action: ActionContinue}, false
}

func finishGatewayRequest(gw *GatewayContext) {
	gatewayPipeline.RunStage(StageResponseComplete, gw)
	if gw.Runtime.ParallelSlotAcquired {
		<-parallelSemaphore
		gw.Runtime.ParallelSlotAcquired = false
	}
}

func writeTerminalStageResponse(gw *GatewayContext, result StageResult) {
	if gw.Response.DirectResponse == nil {
		statusCode := result.StatusCode
		if statusCode == 0 {
			statusCode = http.StatusInternalServerError
		}
		message := result.Message
		if message == "" {
			message = http.StatusText(statusCode)
		}
		gw.Response.DirectResponse = newJSONDirectResponse(statusCode, map[string]string{"error": message})
	}

	writeDirectResponse(gw)
}

func writeDirectResponse(gw *GatewayContext) {
	direct := gw.Response.DirectResponse
	if direct == nil {
		return
	}

	mergeHeaders(gw.Response.Writer.Header(), gw.Response.Header)
	mergeHeaders(gw.Response.Writer.Header(), direct.Headers)

	switch direct.Kind {
	case DirectResponseCachedStream:
		returnCachedAnswer(gw.Response.Writer, direct.CachedAnswer, direct.Model)
	case DirectResponseMockStream:
		returnMockAnswer(gw.Response.Writer)
	default:
		statusCode := direct.StatusCode
		if statusCode == 0 {
			statusCode = http.StatusOK
		}
		gw.Response.Writer.WriteHeader(statusCode)
		if len(direct.Body) > 0 {
			_, _ = gw.Response.Writer.Write(direct.Body)
		}
	}
}

func streamUpstreamResponse(gw *GatewayContext, chunks <-chan *completion.CompletionChunk) error {
	setSSEHeaders(gw.Response.Writer, gw.Response.Header)

	flusher, ok := gw.Response.Writer.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	gw.Response.StreamStarted = true
	gw.Upstream.Started = true

	for chunk := range chunks {
		select {
		case <-gw.Context.Done():
			logDebug("Client disconnected, stopping stream")
			gw.Upstream.Error = gw.Context.Err()
			return gw.Context.Err()
		default:
		}

		gw.Stream.CurrentChunk = chunk
		gw.Stream.ChunkIndex++
		_, _ = gatewayPipeline.RunStage(StageStreamChunk, gw)

		if chunk.Error != nil {
			break
		}

		if chunk.Content != "" {
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
			_, _ = fmt.Fprintf(gw.Response.Writer, "data: %s\n\n", string(jsonBytes))
			flusher.Flush()
		}

		if chunk.Done {
			gw.Upstream.Finished = true
			finishResponse := map[string]interface{}{
				"id":      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   gw.Route.Model,
				"choices": []map[string]interface{}{
					{
						"index":         0,
						"delta":         map[string]string{},
						"finish_reason": "stop",
					},
				},
			}
			finishJSON, _ := json.Marshal(finishResponse)
			_, _ = fmt.Fprintf(gw.Response.Writer, "data: %s\n\n", string(finishJSON))
			_, _ = fmt.Fprintf(gw.Response.Writer, "data: [DONE]\n\n")
			flusher.Flush()
			return nil
		}
	}

	return gw.Upstream.Error
}

func PrintDialog(userText string, answerText string) {
	if currentLogLevel < LogLevelDebug {
		return
	}
	if strings.TrimSpace(userText) == "" && strings.TrimSpace(answerText) == "" {
		return
	}
	if len(userText) > 100 {
		fmt.Printf("user: ...%s\n", strings.ReplaceAll(strings.ReplaceAll(userText[len(userText)-100:], "\n", ""), " ", ""))
	} else {
		fmt.Printf("user: %s\n", userText)
	}
	fmt.Printf("ai: %.100s...\n", answerText)
}

// returnCachedAnswer simulate sse
func returnCachedAnswer(w http.ResponseWriter, cachedAnswer string, model string) {
	setSSEHeaders(w, nil)

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

func returnMockAnswer(w http.ResponseWriter) {
	setSSEHeaders(w, nil)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	mockResponse := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"mock\"}}]}\n\n")
	for i := 0; i < 10; i++ {
		_, _ = w.Write(mockResponse)
		flusher.Flush()
	}
	_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func setSSEHeaders(w http.ResponseWriter, base http.Header) {
	mergeHeaders(w.Header(), base)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func mergeHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

// Admin handlers
func handleRedisCreate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req map[string]interface{}
	if err := BindJSON(r, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to parse request"})
		return
	}

	alias, ok := req["alias"].(string)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid alias type"})
		return
	}

	token, err := authService.Create(alias)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		logError("Fail to create auth token: %w", err)
		json.NewEncoder(w).Encode(map[string]string{"error": "Fail to create auth token"})
		return
	}

	w.WriteHeader(http.StatusOK)
	logDebug("Created token: %s, alias: %s", token, alias)
	json.NewEncoder(w).Encode(map[string]string{"token": token, "alias": alias})
}

func handleRedisGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req map[string]interface{}
	if err := BindJSON(r, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to parse request"})
		return
	}

	token, ok := req["token"].(string)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid token type"})
		return
	}

	valide, alias, err := authService.Get(token)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		logError("Fail to query token from auth service: %w", err)
		json.NewEncoder(w).Encode(map[string]string{"error": "Fail to query token"})
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{"valide": valide, "token": token, "alias": alias})
}

func handleRedisDelete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req map[string]interface{}
	if err := BindJSON(r, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to parse request"})
		return
	}

	token, ok := req["token"].(string)
	if !ok || token == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid or missing token"})
		return
	}

	if err := authService.Delete(token); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		logError("Fail to delete token from auth service: %s", err)
		json.NewEncoder(w).Encode(map[string]string{"error": "Fail to delete token"})
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"token": token, "status": "deleted"})
}
