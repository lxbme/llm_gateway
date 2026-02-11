package main

import "context"

type SemanticCacheService interface {
	// Init(args ...interface{}) error
	// Shutdown()
	Get(ctx context.Context, question string, model string) (string, bool, error)
	Set(ctx context.Context, item SemanticCacheTask) error
}

type SemanticCacheTask struct {
	CollectionName string
	UserPrompt     string
	AIResponse     string
	Dimension      int
	ModelName      string
	TokenUsage     int
}

type EmbeddingService interface {
	Get(ctx context.Context, question string) ([]float32, error)
}
