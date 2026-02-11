package openai

// EmbeddingRequest represents the request body for OpenAI embedding API
type EmbeddingRequest struct {
	Model          string `json:"model"`
	Input          string `json:"input"`
	EncodingFormat string `json:"encoding_format"`
	Dimensions     int32  `json:"dimensions"`
}

// EmbeddingResponse represents the response from OpenAI embedding API
type EmbeddingResponse struct {
	Object string `json:"object"`
	Data   []struct {
		Object    string    `json:"object"`
		Index     int32     `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens int32 `json:"prompt_tokens"`
		TotalTokens  int32 `json:"total_tokens"`
	} `json:"usage"`
}
