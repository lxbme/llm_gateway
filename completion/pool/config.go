package pool

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
)

const (
	envPoolConfigFile = "COMPL_POOL_CONFIG_FILE"
	envPoolConfig     = "COMPL_POOL_CONFIG"
	envLegacyEndpoint = "COMPL_ENDPOINT"
	envLegacyAPIKey   = "COMPL_API_KEY"

	defaultMaxAttempts = 3
	defaultStrategy    = "weighted_random"
	legacyEndpointName = "legacy"
)

type Config struct {
	Strategy    string           `json:"strategy"`
	MaxAttempts int              `json:"max_attempts"`
	Breaker     BreakerConfig    `json:"breaker"`
	Endpoints   []EndpointConfig `json:"endpoints"`
}

func LoadConfigFromEnv() (Config, error) {
	if path := strings.TrimSpace(os.Getenv(envPoolConfigFile)); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("pool: read config file %s: %w", path, err)
		}
		cfg, err := parseJSON(raw)
		if err != nil {
			return Config{}, fmt.Errorf("pool: parse config file %s: %w", path, err)
		}
		return cfg, validate(&cfg)
	}

	if inline := strings.TrimSpace(os.Getenv(envPoolConfig)); inline != "" {
		cfg, err := parseJSON([]byte(inline))
		if err != nil {
			return Config{}, fmt.Errorf("pool: parse %s: %w", envPoolConfig, err)
		}
		return cfg, validate(&cfg)
	}

	if legacy := strings.TrimSpace(os.Getenv(envLegacyEndpoint)); legacy != "" {
		log.Printf("[Info] pool: %s/%s not set, falling back to legacy single-endpoint mode (COMPL_ENDPOINT)", envPoolConfigFile, envPoolConfig)
		cfg := Config{
			Strategy:    defaultStrategy,
			MaxAttempts: 1,
			Endpoints: []EndpointConfig{{
				Name:      legacyEndpointName,
				URL:       legacy,
				APIKeyEnv: envLegacyAPIKey,
				Weight:    1,
				Enabled:   true,
			}},
		}
		return cfg, validate(&cfg)
	}

	return Config{}, errors.New("pool: no configuration found (set COMPL_POOL_CONFIG_FILE, COMPL_POOL_CONFIG, or legacy COMPL_ENDPOINT)")
}

func parseJSON(raw []byte) (Config, error) {
	var cfg Config
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validate(cfg *Config) error {
	if cfg.Strategy == "" {
		cfg.Strategy = defaultStrategy
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = defaultMaxAttempts
	}
	if len(cfg.Endpoints) == 0 {
		return errors.New("pool: at least one endpoint is required")
	}

	names := make(map[string]struct{}, len(cfg.Endpoints))
	enabledCount := 0
	for i := range cfg.Endpoints {
		ep := &cfg.Endpoints[i]
		if ep.Name == "" {
			return fmt.Errorf("pool: endpoint[%d] missing name", i)
		}
		if _, dup := names[ep.Name]; dup {
			return fmt.Errorf("pool: duplicate endpoint name %q", ep.Name)
		}
		names[ep.Name] = struct{}{}

		if ep.URL == "" {
			return fmt.Errorf("pool: endpoint %q missing url", ep.Name)
		}
		if _, err := url.Parse(ep.URL); err != nil {
			return fmt.Errorf("pool: endpoint %q url invalid: %w", ep.Name, err)
		}
		if ep.APIKeyEnv == "" {
			return fmt.Errorf("pool: endpoint %q missing api_key_env", ep.Name)
		}
		if ep.Weight <= 0 {
			ep.Weight = 1
		}
		if ep.Enabled {
			enabledCount++
		}
	}
	if enabledCount == 0 {
		return errors.New("pool: at least one endpoint must be enabled")
	}

	switch cfg.Strategy {
	case "weighted_random", "least_pending", "ewma_latency":
		// supported
	default:
		return fmt.Errorf("pool: unsupported strategy %q (supported: weighted_random, least_pending, ewma_latency)", cfg.Strategy)
	}

	if cfg.Breaker.Enabled {
		if _, _, _, _, _, err := cfg.Breaker.resolved(); err != nil {
			return fmt.Errorf("pool: invalid breaker config: %w", err)
		}
	}
	return nil
}
