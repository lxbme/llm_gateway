package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const serverPort = 8080

func CompletionHandle(w http.ResponseWriter, r *http.Request) {
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

	var userReq ChatCompleteionRequest
	if err := BindJSON(r, &userReq); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		errorResponse, _ := json.Marshal(map[string]string{"error": "Failed to parse user request"})
		w.Write(errorResponse)
		fmt.Printf("[Error] Failed to parse user request: %s\n", err)
		return
	}

	fmt.Printf("[Info] Parsed request: model=%s, stream=%v, messages=%d\n", userReq.Model, userReq.Stream, len(userReq.Messages))

	upstreamReq, err := BuildUpstreamRequest(r.Context(), &userReq)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		errorResponse, _ := json.Marshal(map[string]string{"error": "Failed to build request"})
		w.Write(errorResponse)
		fmt.Printf("[Error] Failed to build request: %s\n", err)
		return
	}

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
		// 读取上游错误响应
		bodyBytes, _ := io.ReadAll(resp.Body)
		w.WriteHeader(resp.StatusCode)
		w.Write(bodyBytes)
		fmt.Printf("[Error] Upstream returned status %d: %s\n", resp.StatusCode, string(bodyBytes))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

	reader := bufio.NewReader(resp.Body)
	writer, ok := w.(http.Flusher)

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
		_, writeErr := w.Write(line)
		if writeErr != nil {
			fmt.Printf("[Info] Client disconnected while writing: %s\n", writeErr)
			return
		}
		if ok {
			writer.Flush()
		}
	}
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", CompletionHandle)

	server := http.Server{
		Addr:    fmt.Sprintf(":%d", serverPort),
		Handler: mux,
	}
	fmt.Printf("[Info] Starting server at %d\n", serverPort)
	err := server.ListenAndServe()
	if err != nil {
		fmt.Printf("[Error] Error running http server: %s\n", err)
	}
}
