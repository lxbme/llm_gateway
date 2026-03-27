package embedding

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Provider string
}

func LoadConfigFromEnv() (Config, error) {
	provider := os.Getenv("EMBED_PROVIDER")
	if provider == "" {
		return Config{}, fmt.Errorf("EMBED_PROVIDER environment variable is required and should not be blank")
	}
	provider = strings.ToLower(provider)

	config := Config{
		Provider: provider,
	}

	return config, nil

}
