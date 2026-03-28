package factory

import (
	"fmt"
	"llm_gateway/cache"
	"llm_gateway/cache/qdrant"
	"llm_gateway/embedding"
)

type Dependencies struct {
	Embedding  embedding.Service
	Dimensions int
}

func New(cfg cache.Config, deps Dependencies) (cache.Service, error) {
	capabilities, err := capabilitiesForProvider(cfg.StoreProvider)
	if err != nil {
		return nil, err
	}

	switch cfg.Mode {
	case cache.ModeSemantic:
		if !capabilities.SupportsSemantic {
			return nil, fmt.Errorf("store provider %q does not support cache mode %q", cfg.StoreProvider, cfg.Mode)
		}
		if deps.Embedding == nil {
			return nil, fmt.Errorf("embedding dependency is required for cache mode %q", cfg.Mode)
		}
		if capabilities.RequiresDimensions && deps.Dimensions <= 0 {
			return nil, fmt.Errorf("dimensions must be greater than 0 for store provider %q", cfg.StoreProvider)
		}

		store, err := newStore(cfg, deps)
		if err != nil {
			return nil, err
		}

		svc, err := cache.NewSemanticService(store, deps.Embedding, cfg.BufferSize, cfg.WorkerCount)
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		return svc, nil
	case cache.ModeExact:
		if !capabilities.SupportsExact {
			return nil, fmt.Errorf("store provider %q does not support cache mode %q", cfg.StoreProvider, cfg.Mode)
		}
		return nil, fmt.Errorf("cache mode %q is not implemented yet", cfg.Mode)
	default:
		return nil, fmt.Errorf("unsupported cache mode: %s", cfg.Mode)
	}
}

func newStore(cfg cache.Config, deps Dependencies) (cache.Store, error) {
	switch cfg.StoreProvider {
	case "qdrant":
		store, err := qdrant.NewFromEnv(deps.Dimensions)
		if err != nil {
			return nil, fmt.Errorf("failed to create qdrant store: %w", err)
		}
		return store, nil
	default:
		return nil, fmt.Errorf("unsupported cache store provider: %s", cfg.StoreProvider)
	}
}

func capabilitiesForProvider(provider string) (cache.StoreCapabilities, error) {
	switch provider {
	case "qdrant":
		return qdrant.StaticCapabilities(), nil
	default:
		return cache.StoreCapabilities{}, fmt.Errorf("unsupported cache store provider: %s", provider)
	}
}
