package rag

import "context"

// Service defines the interface for RAG operations exposed to the gateway and admin API.
type Service interface {
	Ingest(ctx context.Context, chunks []Chunk) (docID string, count int, err error)
	Retrieve(ctx context.Context, query string, collection string, topK int32, threshold float32) ([]RetrievedChunk, error)
	DeleteDoc(ctx context.Context, docID string, collection string) error
	Close() error
}

// Chunk represents a document chunk for ingestion.
type Chunk struct {
	ChunkID     string
	DocID       string
	Collection  string
	Content     string
	Source      string
	ChunkIndex  int32
	TotalChunks int32
}

// RetrievedChunk is a chunk returned from semantic search.
type RetrievedChunk struct {
	ChunkID string
	Content string
	Source  string
	Score   float32
}
