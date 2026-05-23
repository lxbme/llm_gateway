package redis_hnsw

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"llm_gateway/cache"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type Store struct {
	client     *redis.Client
	config     Config
	dimensions int
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

	client := redis.NewClient(&redis.Options{
		Addr:        cfg.Addr,
		Password:    cfg.Password,
		DB:          cfg.DB,
		DialTimeout: time.Duration(cfg.DialTimeoutMs) * time.Millisecond,
		// Force RESP2 so FT.INFO and other modules commands return flat []any
		// arrays instead of RESP3 maps, matching the parsers in index.go.
		// FTSearchWithArgs (used in Search) is protocol-agnostic.
		Protocol: 2,
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.DialTimeoutMs)*time.Millisecond)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis_hnsw: failed to connect: %w", err)
	}

	s := &Store{
		client:     client,
		config:     cfg,
		dimensions: dimensions,
	}

	if err := s.createOrVerifyIndex(context.Background()); err != nil {
		_ = client.Close()
		return nil, err
	}

	if cfg.DistanceMetric != "COSINE" {
		fmt.Printf("[Warning] redis_hnsw: non-COSINE metric %s in use; SimilarityThreshold (%.4f) semantics differ from cosine — calibrate accordingly\n",
			cfg.DistanceMetric, cfg.SimilarityThreshold)
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
	return s.client.Close()
}

func (s *Store) Search(ctx context.Context, query cache.Query) (string, bool, error) {
	if len(query.Vector) == 0 {
		return "", false, fmt.Errorf("search vector must not be empty")
	}
	if len(query.Vector) != s.dimensions {
		return "", false, fmt.Errorf("search vector dim mismatch: got=%d expected=%d", len(query.Vector), s.dimensions)
	}

	q := "(@model:{" + escapeTag(query.Model) + "})=>[KNN 1 @vector $vec AS score]"
	result, err := s.client.FTSearchWithArgs(ctx, s.config.IndexName, q, &redis.FTSearchOptions{
		DialectVersion: 2,
		Params: map[string]any{
			"vec": float32SliceToBytes(query.Vector),
		},
		Return: []redis.FTSearchReturn{
			{FieldName: "answer"},
			{FieldName: "score"},
		},
		LimitOffset: 0,
		Limit:       1,
	}).Result()
	if err != nil {
		return "", false, fmt.Errorf("fail to search RediSearch: %w", err)
	}

	if result.Total == 0 || len(result.Docs) == 0 {
		return "", false, nil
	}

	doc := result.Docs[0]
	answer, ok := doc.Fields["answer"]
	if !ok {
		return "", false, nil
	}

	scoreStr, ok := doc.Fields["score"]
	if !ok {
		return "", false, nil
	}
	score64, err := strconv.ParseFloat(scoreStr, 32)
	if err != nil {
		return "", false, fmt.Errorf("invalid score %q: %w", scoreStr, err)
	}
	score := float32(score64)

	if !s.scoreMeetsThreshold(score) {
		return "", false, nil
	}

	fmt.Printf("[Info] Hit cache: %s\n", doc.ID)
	return answer, true, nil
}

func (s *Store) Insert(ctx context.Context, item cache.Record) error {
	if len(item.Vector) == 0 {
		return fmt.Errorf("record vector must not be empty")
	}
	if len(item.Vector) != s.dimensions {
		return fmt.Errorf("record vector dim mismatch: got=%d expected=%d", len(item.Vector), s.dimensions)
	}

	key := s.config.KeyPrefix + ":" + uuid.New().String()
	timestamp := time.Now().Unix()

	if err := s.client.HSet(ctx, key, map[string]any{
		"question":   item.UserPrompt,
		"answer":     item.AIResponse,
		"model":      item.ModelName,
		"tokenUsage": item.TokenUsage,
		"timestamp":  timestamp,
		"vector":     float32SliceToBytes(item.Vector),
	}).Err(); err != nil {
		return fmt.Errorf("fail to HSET cache record: %w", err)
	}

	if s.config.RecordTTLSeconds > 0 {
		ttl := time.Duration(s.config.RecordTTLSeconds) * time.Second
		if err := s.client.Expire(ctx, key, ttl).Err(); err != nil {
			fmt.Printf("[Warning] failed to set TTL on %s: %s\n", key, err)
		}
	}

	return nil
}

func (s *Store) scoreMeetsThreshold(score float32) bool {
	threshold := s.config.SimilarityThreshold
	switch s.config.DistanceMetric {
	case "COSINE":
		// RediSearch returns cosine distance in [0, 2]; similarity = 1 - distance.
		similarity := 1.0 - score
		return similarity >= threshold
	case "IP":
		// Inner product: larger score means more similar.
		return score >= threshold
	case "L2":
		// Squared L2 distance: smaller is closer. Threshold is interpreted as
		// a max allowed distance — caller calibrates.
		return score <= threshold
	default:
		// Defensive fallback: treat like cosine.
		return (1.0 - score) >= threshold
	}
}

