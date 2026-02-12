package completion

type CompletionRequest struct {
	Model       string
	Question    string
	Temperature int
	MaxTokens   int
	Stream      bool
}

type CompletionChunk struct {
	Content    string
	Error      error
	Done       bool
	TokenUsage int
}
