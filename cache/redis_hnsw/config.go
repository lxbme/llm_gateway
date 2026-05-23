package redis_hnsw

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	defaultAddr                = "localhost:6379"
	defaultDB                  = 0
	defaultIndexName           = "llm_semantic_cache_idx"
	defaultKeyPrefix           = "llm_semantic_cache"
	defaultSimilarityThreshold = 0.95
	defaultDistanceMetric      = "COSINE"
	defaultHNSWM               = 16
	defaultHNSWEFConstruction  = 200
	defaultHNSWEFRuntime       = 10
	defaultRecordTTLSeconds    = 0
	defaultDialTimeoutMs       = 5000
)

type Config struct {
	Addr                string
	Password            string
	DB                  int
	IndexName           string
	KeyPrefix           string
	SimilarityThreshold float32
	DistanceMetric      string
	HNSWM               int
	HNSWEFConstruction  int
	HNSWEFRuntime       int
	RecordTTLSeconds    int
	DialTimeoutMs       int
}

func LoadConfigFromEnv() (Config, error) {
	addr := loadStringFromEnv("REDIS_HNSW_ADDR", defaultAddr)
	password := os.Getenv("REDIS_HNSW_PASSWORD")

	db, err := loadIntFromEnv("REDIS_HNSW_DB", defaultDB)
	if err != nil {
		return Config{}, err
	}

	indexName := loadStringFromEnv("REDIS_HNSW_INDEX_NAME", defaultIndexName)
	keyPrefix := loadStringFromEnv("REDIS_HNSW_KEY_PREFIX", defaultKeyPrefix)

	similarityThreshold, err := loadFloat32FromEnv("REDIS_HNSW_SIMILARITY_THRESHOLD", defaultSimilarityThreshold)
	if err != nil {
		return Config{}, err
	}

	distanceMetric := strings.ToUpper(loadStringFromEnv("REDIS_HNSW_DISTANCE_METRIC", defaultDistanceMetric))
	switch distanceMetric {
	case "COSINE", "L2", "IP":
	default:
		return Config{}, fmt.Errorf("REDIS_HNSW_DISTANCE_METRIC must be one of COSINE/L2/IP, got %q", distanceMetric)
	}

	hnswM, err := loadIntFromEnv("REDIS_HNSW_M", defaultHNSWM)
	if err != nil {
		return Config{}, err
	}
	hnswEFConstruction, err := loadIntFromEnv("REDIS_HNSW_EF_CONSTRUCTION", defaultHNSWEFConstruction)
	if err != nil {
		return Config{}, err
	}
	hnswEFRuntime, err := loadIntFromEnv("REDIS_HNSW_EF_RUNTIME", defaultHNSWEFRuntime)
	if err != nil {
		return Config{}, err
	}

	recordTTLSeconds, err := loadIntFromEnvAllowZero("REDIS_HNSW_RECORD_TTL_SECONDS", defaultRecordTTLSeconds)
	if err != nil {
		return Config{}, err
	}

	dialTimeoutMs, err := loadIntFromEnv("REDIS_HNSW_DIAL_TIMEOUT_MS", defaultDialTimeoutMs)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Addr:                addr,
		Password:            password,
		DB:                  db,
		IndexName:           indexName,
		KeyPrefix:           keyPrefix,
		SimilarityThreshold: similarityThreshold,
		DistanceMetric:      distanceMetric,
		HNSWM:               hnswM,
		HNSWEFConstruction:  hnswEFConstruction,
		HNSWEFRuntime:       hnswEFRuntime,
		RecordTTLSeconds:    recordTTLSeconds,
		DialTimeoutMs:       dialTimeoutMs,
	}, nil
}

func loadStringFromEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func loadIntFromEnv(key string, defaultValue int) (int, error) {
	rawValue := os.Getenv(key)
	if rawValue == "" {
		return defaultValue, nil
	}

	parsedValue, err := strconv.Atoi(rawValue)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid integer: %w", key, err)
	}
	if parsedValue <= 0 {
		return 0, fmt.Errorf("%s must be greater than 0", key)
	}
	return parsedValue, nil
}

func loadIntFromEnvAllowZero(key string, defaultValue int) (int, error) {
	rawValue := os.Getenv(key)
	if rawValue == "" {
		return defaultValue, nil
	}

	parsedValue, err := strconv.Atoi(rawValue)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid integer: %w", key, err)
	}
	if parsedValue < 0 {
		return 0, fmt.Errorf("%s must be >= 0", key)
	}
	return parsedValue, nil
}

func loadFloat32FromEnv(key string, defaultValue float32) (float32, error) {
	rawValue := os.Getenv(key)
	if rawValue == "" {
		return defaultValue, nil
	}

	parsedValue, err := strconv.ParseFloat(rawValue, 32)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid float: %w", key, err)
	}
	return float32(parsedValue), nil
}
