package rag

import "context"

// Store abstracts the underlying RAG vector store backend.
type Store interface {
	Upsert(ctx context.Context, task UpsertTask) error
	Query(ctx context.Context, task QueryTask) ([]RetrievedChunk, error)
	DeleteByDocID(ctx context.Context, docID string, collection string) error
	Close() error
}

// UpsertTask carries all data needed to store a chunk vector.
type UpsertTask struct {
	ChunkID     string
	DocID       string
	Collection  string
	Content     string
	Source      string
	ChunkIndex  int32
	TotalChunks int32
	Vector      []float32
}

// QueryTask carries the search parameters.
type QueryTask struct {
	Vector     []float32
	Collection string
	TopK       int32
	Threshold  float32
}
