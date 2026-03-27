package factory

import (
	"fmt"
	"llm_gateway/embedding"
	"llm_gateway/embedding/openai"
)

func New(cfg embedding.Config) (embedding.Service, error) {
	if cfg.Provider == "openai" {
		svc, err := openai.NewFromEnv()
		if err != nil {
			return nil, fmt.Errorf("fail to create openai embedding service: %w", err)
		}
		return svc, nil
	}

	return nil, fmt.Errorf("unsupported embedding provider: %s", cfg.Provider)
}
