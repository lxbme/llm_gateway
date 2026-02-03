package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type apiResponse struct {
	Id      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []any  `json:"choices"`
	Usage   any    `json:"usage"`
}

type choiceItem struct {
	Index        int    `json:"index"`
	Message      any    `json:"message"`
	FinishReason string `json:"finish_reason"`
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

func main() {
	http.HandleFunc("/", hello)
	fmt.Println("[Info] Starting server at 8080")
	http.ListenAndServe(":8080", nil)
}
