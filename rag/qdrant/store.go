package qdrant

import (
	"context"
	"fmt"
	"time"

	"llm_gateway/rag"

	"github.com/qdrant/go-client/qdrant"
)

// Store implements the rag.Store interface using Qdrant.
// Each document chunk is stored as a separate point with its own payload for
// multi-tenant isolation via the "collection" payload field.
type Store struct {
	qdrantClient   *qdrant.Client
	dimensions     int
	collectionName string
}

func New(cfg Config, dimensions int) (*Store, error) {
	if dimensions <= 0 {
		return nil, fmt.Errorf("dimensions must be greater than 0")
	}

	qclient, err := qdrant.NewClient(&qdrant.Config{
		Host: cfg.Host,
		Port: cfg.Port,
	})
	if err != nil {
		return nil, fmt.Errorf("fail to create qdrant client: %w", err)
	}

	s := &Store{
		qdrantClient:   qclient,
		dimensions:     dimensions,
		collectionName: cfg.CollectionName,
	}

	if err := s.createCollection(); err != nil {
		qclient.Close()
		return nil, fmt.Errorf("fail to ensure qdrant collection: %w", err)
	}

	return s, nil
}

func NewFromEnv(dimensions int) (*Store, error) {
	cfg, err := LoadConfigFromEnv()
	if err != nil {
		return nil, err
	}
	return New(cfg, dimensions)
}

func (s *Store) Close() error {
	s.qdrantClient.Close()
	return nil
}

// Upsert stores a single document chunk vector with its metadata payload.
func (s *Store) Upsert(ctx context.Context, task rag.UpsertTask) error {
	if len(task.Vector) == 0 {
		return fmt.Errorf("upsert vector must not be empty")
	}

	_, err := s.qdrantClient.Upsert(ctx, &qdrant.UpsertPoints{
		CollectionName: s.collectionName,
		Points: []*qdrant.PointStruct{
			{
				Id:      qdrant.NewID(task.ChunkID),
				Vectors: qdrant.NewVectorsDense(task.Vector),
				Payload: qdrant.NewValueMap(map[string]any{
					"chunk_id":     task.ChunkID,
					"doc_id":       task.DocID,
					"collection":   task.Collection,
					"content":      task.Content,
					"source":       task.Source,
					"chunk_index":  task.ChunkIndex,
					"total_chunks": task.TotalChunks,
					"created_at":   time.Now().Unix(),
				}),
			},
		},
	})
	if err != nil {
		return fmt.Errorf("fail to upsert qdrant point: %w", err)
	}

	return nil
}

// Query performs a cosine similarity search filtered by the logical collection name.
func (s *Store) Query(ctx context.Context, task rag.QueryTask) ([]rag.RetrievedChunk, error) {
	if len(task.Vector) == 0 {
		return nil, fmt.Errorf("query vector must not be empty")
	}

	limit := uint64(task.TopK)
	if limit == 0 {
		limit = 3
	}

	threshold := task.Threshold
	if threshold <= 0 {
		threshold = 0.6
	}

	results, err := s.qdrantClient.Query(ctx, &qdrant.QueryPoints{
		CollectionName: s.collectionName,
		Query:          qdrant.NewQueryDense(task.Vector),
		Filter: &qdrant.Filter{
			Must: []*qdrant.Condition{
				qdrant.NewMatch("collection", task.Collection),
			},
		},
		WithPayload:    qdrant.NewWithPayload(true),
		ScoreThreshold: qdrant.PtrOf(threshold),
		Limit:          qdrant.PtrOf(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("fail to query qdrant: %w", err)
	}

	chunks := make([]rag.RetrievedChunk, 0, len(results))
	for _, point := range results {
		payload := point.Payload
		chunk := rag.RetrievedChunk{
			Score: point.Score,
		}
		if v, ok := payload["chunk_id"]; ok {
			chunk.ChunkID = v.GetStringValue()
		}
		if v, ok := payload["content"]; ok {
			chunk.Content = v.GetStringValue()
		}
		if v, ok := payload["source"]; ok {
			chunk.Source = v.GetStringValue()
		}
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}

// DeleteByDocID removes all chunks belonging to the given document from the logical collection.
func (s *Store) DeleteByDocID(ctx context.Context, docID string, collection string) error {
	_, err := s.qdrantClient.Delete(ctx, &qdrant.DeletePoints{
		CollectionName: s.collectionName,
		Points: &qdrant.PointsSelector{
			PointsSelectorOneOf: &qdrant.PointsSelector_Filter{
				Filter: &qdrant.Filter{
					Must: []*qdrant.Condition{
						qdrant.NewMatch("doc_id", docID),
						qdrant.NewMatch("collection", collection),
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("fail to delete doc %s from qdrant: %w", docID, err)
	}
	return nil
}

func (s *Store) createCollection() error {
	ctx := context.Background()

	isExist, err := s.qdrantClient.CollectionExists(ctx, s.collectionName)
	if err != nil {
		return fmt.Errorf("fail to check if collection %s exists: %w", s.collectionName, err)
	}

	if !isExist {
		err = s.qdrantClient.CreateCollection(ctx, &qdrant.CreateCollection{
			CollectionName: s.collectionName,
			VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{
				Size:     uint64(s.dimensions),
				Distance: qdrant.Distance_Cosine,
			}),
		})
		if err != nil {
			return fmt.Errorf("fail to create collection: %w", err)
		}
		fmt.Printf("[Info] Created Qdrant RAG collection: %s\n", s.collectionName)
		return nil
	}

	collectionInfo, err := s.qdrantClient.GetCollectionInfo(ctx, s.collectionName)
	if err != nil {
		return fmt.Errorf("fail to get collection info: %w", err)
	}

	existingDim, err := getCollectionDimensions(collectionInfo)
	if err != nil {
		return fmt.Errorf("fail to inspect collection dimensions: %w", err)
	}

	if existingDim != s.dimensions {
		return fmt.Errorf(
			"collection %s dimension mismatch: existing=%d expected=%d",
			s.collectionName, existingDim, s.dimensions,
		)
	}

	fmt.Printf("[Info] Reusing Qdrant RAG collection: %s (dimensions=%d)\n", s.collectionName, existingDim)
	return nil
}

func getCollectionDimensions(info *qdrant.CollectionInfo) (int, error) {
	if info == nil {
		return 0, fmt.Errorf("collection info is nil")
	}
	config := info.GetConfig()
	if config == nil {
		return 0, fmt.Errorf("collection config is nil")
	}
	params := config.GetParams()
	if params == nil {
		return 0, fmt.Errorf("collection params are nil")
	}
	vectorsConfig := params.GetVectorsConfig()
	if vectorsConfig == nil {
		return 0, fmt.Errorf("collection vectors config is nil")
	}
	vectorParams := vectorsConfig.GetParams()
	if vectorParams == nil {
		return 0, fmt.Errorf("dense vector params are nil")
	}
	size := vectorParams.GetSize()
	if size == 0 {
		return 0, fmt.Errorf("collection vector size must be greater than 0")
	}
	return int(size), nil
}
