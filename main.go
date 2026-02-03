package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type apiResponse struct {
	Id      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []any  `json:"choices"`
	Usage   any    `json:"usage"`
}

type sseResponse struct {
	Id      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []any  `json:"choices"`
}

type choiceItem struct {
	Index        int    `json:"index"`
	Message      any    `json:"message"`
	FinishReason string `json:"finish_reason"`
}

type sseChoiceItem struct {
	Index        int               `json:"index"`
	Delta        map[string]string `json:"delta"`
	FinishReason any               `json:"finish_reason"`
}

type usageItem struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func hello(w http.ResponseWriter, r *http.Request) {
	usage_item := usageItem{
		PromptTokens:     9,
		CompletionTokens: 12,
		TotalTokens:      21,
	}

	choice := choiceItem{
		Index: 0,
		Message: map[string]string{
			"role":    "assistant",
			"content": "Generate content here.",
		},
		FinishReason: "stop",
	}

	response := apiResponse{
		Id:      "chatcmpl-123",
		Object:  "chat.completion",
		Created: 1677652288,
		Model:   "gpt-4o",
		Choices: []any{choice},
		Usage:   usage_item,
	}

	response_byte, err := json.Marshal(response)
	if err != nil {
		fmt.Println("[Err] Fail to marshal response")
	}
	w.Header().Set("content-type", "application/json")
	w.Write(response_byte)
}

func sseHandle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	client_disconnect := r.Context().Done()
	response_controller := http.NewResponseController(w)

	response_msg := "Hello, this is a response from go."
	tokens := strings.Split(response_msg, " ")
	for _, token := range tokens {
		select {
		case <-client_disconnect:
			fmt.Println("[Info] Client Disconnected.")
			return
		default:
			single_choice := sseChoiceItem{
				Index: 0,
				Delta: map[string]string{
					"content": token,
				},
				FinishReason: nil,
			}
			single_response := sseResponse{
				Id:      "chatcmpl-123",
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   "gpt-4o",
				Choices: []any{single_choice},
			}
			single_response_byte, err := json.Marshal(single_response)
			if err != nil {
				fmt.Println("[Err] Fail to marshel sse response")
			}
			fmt.Fprintf(w, "data: %s\n\n", single_response_byte)
			response_controller.Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}
	fmt.Fprintf(w, "data: [DONE]\n\n")
	response_controller.Flush()
}

func main() {
	http.HandleFunc("/", hello)
	http.HandleFunc("/stream", sseHandle)
	fmt.Println("[Info] Starting server at 8080")
	http.ListenAndServe(":8080", nil)
}
