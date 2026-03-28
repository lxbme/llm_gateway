package qdrant

import (
	"context"
	"fmt"
	"time"

	"llm_gateway/cache"

	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
)

// Store implements the cache.Store interface using Qdrant.
type Store struct {
	qdrantClient        *qdrant.Client
	dimensions          int
	collectionName      string
	similarityThreshold float32
}

func StaticCapabilities() cache.StoreCapabilities {
	return cache.StoreCapabilities{
		SupportsSemantic:   true,
		SupportsExact:      false,
		RequiresDimensions: true,
	}
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
		qdrantClient:        qclient,
		dimensions:          dimensions,
		collectionName:      cfg.CollectionName,
		similarityThreshold: cfg.SimilarityThreshold,
	}

	if err := s.createCollection(); err != nil {
		qclient.Close()
		return nil, fmt.Errorf("fail to create qdrant collection: %w", err)
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

func (s *Store) Capabilities() cache.StoreCapabilities {
	return StaticCapabilities()
}

func (s *Store) Close() error {
	s.qdrantClient.Close()
	return nil
}

func (s *Store) Search(ctx context.Context, query cache.Query) (string, bool, error) {
	if len(query.Vector) == 0 {
		return "", false, fmt.Errorf("search vector must not be empty")
	}

	searchResult, err := s.qdrantClient.Query(ctx, &qdrant.QueryPoints{
		CollectionName: s.collectionName,
		Query:          qdrant.NewQueryDense(query.Vector),
		Filter: &qdrant.Filter{
			Must: []*qdrant.Condition{
				qdrant.NewMatch("model", query.Model),
			},
		},
		WithPayload:    qdrant.NewWithPayload(true),
		ScoreThreshold: qdrant.PtrOf(s.similarityThreshold),
	})
	if err != nil {
		return "", false, fmt.Errorf("fail to search qdrant: %w", err)
	}
	if len(searchResult) == 0 {
		return "", false, nil
	}

	answer, ok := searchResult[0].Payload["answer"]
	if !ok {
		return "", false, nil
	}

	fmt.Printf("[Info] Hit cache: %s\n", searchResult[0].Id.GetUuid())
	return answer.GetStringValue(), true, nil
}

func (s *Store) Insert(ctx context.Context, item cache.Record) error {
	if len(item.Vector) == 0 {
		return fmt.Errorf("record vector must not be empty")
	}

	timeStamp := time.Now().Unix()
	pointID := uuid.New().String()
	_, err := s.qdrantClient.Upsert(ctx, &qdrant.UpsertPoints{
		CollectionName: s.collectionName,
		Points: []*qdrant.PointStruct{
			{
				Id:      qdrant.NewID(pointID),
				Vectors: qdrant.NewVectorsDense(item.Vector),
				Payload: qdrant.NewValueMap(map[string]any{
					"question":   item.UserPrompt,
					"answer":     item.AIResponse,
					"model":      item.ModelName,
					"tokenUsage": item.TokenUsage,
					"timestamp":  timeStamp,
				}),
			},
		},
	})
	if err != nil {
		return fmt.Errorf("fail to store qdrant point: %w", err)
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
		fmt.Printf("[Info] Created Qdrant collection: %s\n", s.collectionName)
		return nil
	}

	collectionInfo, err := s.qdrantClient.GetCollectionInfo(ctx, s.collectionName)
	if err != nil {
		return fmt.Errorf("fail to get collection %s info: %w", s.collectionName, err)
	}

	existingDimensions, err := getCollectionDimensions(collectionInfo)
	if err != nil {
		return fmt.Errorf("fail to inspect collection %s dimensions: %w", s.collectionName, err)
	}

	if existingDimensions != s.dimensions {
		return fmt.Errorf(
			"collection %s dimension mismatch: existing=%d expected=%d",
			s.collectionName,
			existingDimensions,
			s.dimensions,
		)
	}

	fmt.Printf("[Info] Reusing Qdrant collection: %s (dimensions=%d)\n", s.collectionName, existingDimensions)
	return nil
}

func getCollectionDimensions(collectionInfo *qdrant.CollectionInfo) (int, error) {
	if collectionInfo == nil {
		return 0, fmt.Errorf("collection info is nil")
	}

	config := collectionInfo.GetConfig()
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
		if vectorsConfig.GetParamsMap() != nil {
			return 0, fmt.Errorf("named vectors are not supported by current cache service")
		}
		return 0, fmt.Errorf("dense vector params are nil")
	}

	size := vectorParams.GetSize()
	if size == 0 {
		return 0, fmt.Errorf("collection vector size must be greater than 0")
	}

	return int(size), nil
}
