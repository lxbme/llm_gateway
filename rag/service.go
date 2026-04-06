package rag

import (
	"context"
	"fmt"

	"llm_gateway/embedding"

	"github.com/google/uuid"
)

// ServiceImpl implements the RAG Service interface.
type ServiceImpl struct {
	store     Store
	embedding embedding.Service
}

func NewService(store Store, embeddingService embedding.Service) (*ServiceImpl, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if embeddingService == nil {
		return nil, fmt.Errorf("embedding service is required")
	}
	return &ServiceImpl{
		store:     store,
		embedding: embeddingService,
	}, nil
}

// Ingest embeds each chunk and upserts it into the vector store.
// A shared doc_id is generated if the first chunk has none.
func (s *ServiceImpl) Ingest(ctx context.Context, chunks []Chunk) (string, int, error) {
	if len(chunks) == 0 {
		return "", 0, fmt.Errorf("chunks must not be empty")
	}

	docID := chunks[0].DocID
	if docID == "" {
		docID = uuid.New().String()
	}

	for i := range chunks {
		if chunks[i].DocID == "" {
			chunks[i].DocID = docID
		}
		if chunks[i].ChunkID == "" {
			chunks[i].ChunkID = uuid.New().String()
		}

		vector, err := s.embedding.Get(ctx, chunks[i].Content)
		if err != nil {
			return "", 0, fmt.Errorf("failed to embed chunk %d: %w", i, err)
		}

		if err := s.store.Upsert(ctx, UpsertTask{
			ChunkID:     chunks[i].ChunkID,
			DocID:       chunks[i].DocID,
			Collection:  chunks[i].Collection,
			Content:     chunks[i].Content,
			Source:      chunks[i].Source,
			ChunkIndex:  chunks[i].ChunkIndex,
			TotalChunks: chunks[i].TotalChunks,
			Vector:      vector,
		}); err != nil {
			return "", 0, fmt.Errorf("failed to upsert chunk %d: %w", i, err)
		}
	}

	fmt.Printf("[Info] Ingested %d chunks, doc_id=%s\n", len(chunks), docID)
	return docID, len(chunks), nil
}

// Retrieve returns the top-K semantically similar chunks for a query.
func (s *ServiceImpl) Retrieve(ctx context.Context, query string, collection string, topK int32, threshold float32) ([]RetrievedChunk, error) {
	if topK <= 0 {
		topK = 3
	}
	if threshold <= 0 {
		threshold = 0.6
	}

	vector, err := s.embedding.Get(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	return s.store.Query(ctx, QueryTask{
		Vector:     vector,
		Collection: collection,
		TopK:       topK,
		Threshold:  threshold,
	})
}

// DeleteDoc removes all chunks belonging to the given document from the store.
func (s *ServiceImpl) DeleteDoc(ctx context.Context, docID string, collection string) error {
	return s.store.DeleteByDocID(ctx, docID, collection)
}

func (s *ServiceImpl) Close() error {
	return s.store.Close()
}
