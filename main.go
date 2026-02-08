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
	response_controller := http.NewResponseController(w)
	var userReq ChatCompleteionRequest
	if err := BindJSON(r, &userReq); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		errorResponse, _ := json.Marshal(map[string]string{"error": "Failed to parse user request"})
		w.Write(errorResponse)
		fmt.Printf("[Error] Failed to parse user request: %s\n", err)
		return
	}

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

	reader := bufio.NewReader(resp.Body)

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			fmt.Printf("[Error] Fail to read line from reader: %s\n", err)
			break
		}
		w.Write(line)
		response_controller.Flush()
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
