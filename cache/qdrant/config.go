package qdrant

import (
	"fmt"
	"os"
	"strconv"
)

const (
	defaultHost                = "localhost"
	defaultPort                = 6334
	defaultCollectionName      = "llm_semantic_cache"
	defaultSimilarityThreshold = 0.95
)

type Config struct {
	Host                string
	Port                int
	CollectionName      string
	SimilarityThreshold float32
}

func LoadConfigFromEnv() (Config, error) {
	host := os.Getenv("QDRANT_HOST")
	if host == "" {
		host = defaultHost
	}

	port, err := loadIntFromEnv("QDRANT_PORT", defaultPort)
	if err != nil {
		return Config{}, err
	}

	collectionName := os.Getenv("QDRANT_COLLECTION_NAME")
	if collectionName == "" {
		collectionName = defaultCollectionName
	}

	similarityThreshold, err := loadFloat32FromEnv("QDRANT_SIMILARITY_THRESHOLD", defaultSimilarityThreshold)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Host:                host,
		Port:                port,
		CollectionName:      collectionName,
		SimilarityThreshold: similarityThreshold,
	}, nil
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
