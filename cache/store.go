package cache

import "context"

// Store abstracts the underlying cache backend.
type Store interface {
	Capabilities() StoreCapabilities
	Search(ctx context.Context, query Query) (result string, found bool, err error)
	Insert(ctx context.Context, item Record) error
	Close() error
}

type StoreCapabilities struct {
	SupportsSemantic   bool
	SupportsExact      bool
	RequiresDimensions bool
}

type Query struct {
	Vector []float32
	Text   string
	Model  string
}

type Record struct {
	Vector     []float32
	UserPrompt string
	AIResponse string
	ModelName  string
	TokenUsage int
}
