package embedding

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Provider string
	OpenAI   OpenAIConfig
}

type OpenAIConfig struct {
	Endpoint   string
	Model      string
	APIKey     string
	Dimensions int
}

func LoadConfigFromEnv() (Config, error) {
	provider := os.Getenv("EMBED_PROVIDER")
	if provider == "" {
		return Config{}, fmt.Errorf("EMBED_PROVIDER environment variable is required and should not be blank")
	}
	provider = strings.ToLower(provider)

	var openaiConfig OpenAIConfig
	if provider == "openai" {
		endpoint := os.Getenv("EMBED_ENDPOINT")
		if endpoint == "" {
			return Config{}, fmt.Errorf("EMBED_ENDPOINT environment variable is required and should not be blank")
		}

		model := os.Getenv("EMBED_MODEL")
		if model == "" {
			return Config{}, fmt.Errorf("EMBED_MODEL environment variable is required and should not be blank")
		}

		apiKey := os.Getenv("EMBED_API_KEY")
		if apiKey == "" {
			return Config{}, fmt.Errorf("EMBED_API_KEY environment variable is required and should not be blank")
		}

		dimensions := os.Getenv("EMBED_DIMENSIONS")
		if dimensions == "" {
			return Config{}, fmt.Errorf("EMBED_DIMENSIONS environment variable is required and should not be blank")
		}
		dimInt, err := strconv.Atoi(dimensions)
		if err != nil {
			return Config{}, fmt.Errorf("EMBED_DIMENSIONS must be a valid integer: %w", err)
		}
		openaiConfig = OpenAIConfig{
			Endpoint:   endpoint,
			Model:      model,
			APIKey:     apiKey,
			Dimensions: dimInt,
		}
	}

	config := Config{
		Provider: provider,
		OpenAI:   openaiConfig,
	}

	return config, nil

}
