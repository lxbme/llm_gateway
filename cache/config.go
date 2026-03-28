package cache

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Mode string

const (
	ModeSemantic Mode = "semantic"
	ModeExact    Mode = "exact"

	defaultMode       = ModeSemantic
	defaultBufferSize = 1000
	defaultWorkerSize = 5
)

type Config struct {
	Mode          Mode
	StoreProvider string
	BufferSize    int
	WorkerCount   int
}

func LoadConfigFromEnv() (Config, error) {
	mode := os.Getenv("CACHE_MODE")
	if mode == "" {
		mode = string(defaultMode)
	}
	mode = strings.ToLower(mode)

	storeProvider := os.Getenv("CACHE_STORE_PROVIDER")
	if storeProvider == "" {
		storeProvider = os.Getenv("CACHE_PROVIDER")
	}
	if storeProvider == "" {
		return Config{}, fmt.Errorf("CACHE_STORE_PROVIDER environment variable is required and should not be blank")
	}
	storeProvider = strings.ToLower(storeProvider)

	bufferSize, err := loadIntFromEnv("CACHE_BUFFER_SIZE", defaultBufferSize)
	if err != nil {
		return Config{}, err
	}
	workerCount, err := loadIntFromEnv("CACHE_WORKER_COUNT", defaultWorkerSize)
	if err != nil {
		return Config{}, err
	}

	config := Config{
		Mode:          Mode(mode),
		StoreProvider: storeProvider,
		BufferSize:    bufferSize,
		WorkerCount:   workerCount,
	}

	return config, nil
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
