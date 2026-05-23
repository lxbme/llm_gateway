package ollama

// EmbedRequest represents the request body for Ollama /api/embed.
type EmbedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

// EmbedResponse represents the response from Ollama /api/embed.
// Note: Ollama returns embeddings as a 2D array (one vector per input),
// not the OpenAI-style `data[].embedding` shape.
type EmbedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float32 `json:"embeddings"`
}
