package completion

type CompletionRequest struct {
	Model       string
	Question    string
	Temperature float64
	MaxTokens   int
	Stream      bool
}

type CompletionChunk struct {
	Content          string
	Error            error
	Done             bool
	TokenUsage       int // total_tokens; kept as-is so existing readers (cache writeback) stay unchanged
	PromptTokens     int
	CompletionTokens int
}
