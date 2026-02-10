package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
			answerString, err := ParseSSELine(line)
			if err != nil {
				fmt.Printf("[Error] Fail to parse sse line: %s\n", err)
			} else {
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
	if len(userPrompt) > 100 {
		fmt.Printf("user: ...%s\n", userPrompt[len(userPrompt)-100:])
	} else {
		fmt.Printf("user: %s\n", userPrompt)
	}
	fmt.Printf("ai: %s\n", fullAnswerBuffer.String())
}
