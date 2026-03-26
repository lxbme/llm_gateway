package factory

import (
	"fmt"
	"llm_gateway/embedding"
	"llm_gateway/embedding/openai"
)

func New(cfg embedding.Config) (embedding.Service, error) {
	if cfg.Provider == "openai" {
		return openai.New(openai.Config(cfg.OpenAI)), nil
	}

	return nil, fmt.Errorf("unsupported embedding provider: %s", cfg.Provider)
}
