package cache

import "context"

// Service defines the interface for semantic cache operations
type Service interface {
	Get(ctx context.Context, question string, model string) (string, bool, error)
	Set(ctx context.Context, item Task) error
	Shutdown()
}

// Task represents a semantic cache task
type Task struct {
	UserPrompt string
	AIResponse string
	ModelName  string
	TokenUsage int
}
