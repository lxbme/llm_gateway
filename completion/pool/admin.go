package pool

import (
	"context"
	"fmt"
	"log"
	"net/url"

	"llm_gateway/completion"
)

// ListEndpoints returns the current pool membership. Read-only.
// Implements completion.Admin.
func (s *Service) ListEndpoints(_ context.Context) ([]completion.EndpointView, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]completion.EndpointView, 0, len(s.endpoints))
	for _, ep := range s.endpoints {
		out = append(out, completion.EndpointView{
			Name:         ep.Cfg.Name,
			URL:          ep.Cfg.URL,
			APIKeyEnv:    ep.Cfg.APIKeyEnv,
			Weight:       ep.Cfg.Weight,
			Models:       append([]string(nil), ep.Cfg.Models...),
			Enabled:      ep.Cfg.Enabled,
			BreakerState: breakerStateName(ep.Breaker),
		})
	}
	return out, nil
}

func (s *Service) AddEndpoint(_ context.Context, spec completion.EndpointSpec) error {
	ec := EndpointConfig{
		Name:      spec.Name,
		URL:       spec.URL,
		APIKeyEnv: spec.APIKeyEnv,
		Weight:    spec.Weight,
		Models:    spec.Models,
		Enabled:   spec.Enabled,
	}
	if err := validateEndpoint(ec); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ep := range s.endpoints {
		if ep.Cfg.Name == ec.Name {
			return fmt.Errorf("pool: endpoint %q already exists", ec.Name)
		}
	}
	ep := &Endpoint{
		Cfg:    ec,
		Client: s.factory(ec),
		Stats:  &endpointStats{},
	}
	if s.breakerCfg.Enabled {
		b, err := newBreaker(ec.Name, s.breakerCfg)
		if err != nil {
			return fmt.Errorf("pool: breaker for %s: %w", ec.Name, err)
		}
		ep.Breaker = b
	}
	s.endpoints = append(append([]*Endpoint{}, s.endpoints...), ep)
	log.Printf("[Info] pool: admin added endpoint %s (weight=%d enabled=%t)", ec.Name, ec.Weight, ec.Enabled)
	return nil
}

func (s *Service) RemoveEndpoint(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, ep := range s.endpoints {
		if ep.Cfg.Name == name {
			next := make([]*Endpoint, 0, len(s.endpoints)-1)
			next = append(next, s.endpoints[:i]...)
			next = append(next, s.endpoints[i+1:]...)
			s.endpoints = next
			log.Printf("[Info] pool: admin removed endpoint %s", name)
			return nil
		}
	}
	return fmt.Errorf("pool: endpoint %q not found", name)
}

func (s *Service) Reweight(_ context.Context, name string, weight int) error {
	if weight <= 0 {
		return fmt.Errorf("pool: weight must be > 0, got %d", weight)
	}
	return s.replaceCfg(name, func(cfg *EndpointConfig) { cfg.Weight = weight })
}

func (s *Service) SetEnabled(_ context.Context, name string, enabled bool) error {
	return s.replaceCfg(name, func(cfg *EndpointConfig) { cfg.Enabled = enabled })
}

func (s *Service) ResetBreaker(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, ep := range s.endpoints {
		if ep.Cfg.Name == name {
			if !s.breakerCfg.Enabled {
				return fmt.Errorf("pool: breaker is not enabled")
			}
			b, err := newBreaker(name, s.breakerCfg)
			if err != nil {
				return fmt.Errorf("pool: breaker rebuild: %w", err)
			}
			replacement := &Endpoint{
				Cfg:     ep.Cfg,
				Client:  ep.Client,
				Stats:   ep.Stats,
				Breaker: b,
			}
			next := append([]*Endpoint{}, s.endpoints...)
			next[i] = replacement
			s.endpoints = next
			log.Printf("[Info] pool: admin reset breaker for %s", name)
			return nil
		}
	}
	return fmt.Errorf("pool: endpoint %q not found", name)
}

// replaceCfg performs a copy-on-write replacement of one endpoint with a fresh Cfg.
// Stats/Breaker pointers are preserved so counters and circuit state survive.
func (s *Service) replaceCfg(name string, mutate func(*EndpointConfig)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, ep := range s.endpoints {
		if ep.Cfg.Name == name {
			newCfg := ep.Cfg
			newCfg.Models = append([]string(nil), ep.Cfg.Models...)
			mutate(&newCfg)
			replacement := &Endpoint{
				Cfg:     newCfg,
				Client:  ep.Client,
				Stats:   ep.Stats,
				Breaker: ep.Breaker,
			}
			next := append([]*Endpoint{}, s.endpoints...)
			next[i] = replacement
			s.endpoints = next
			log.Printf("[Info] pool: admin updated endpoint %s (weight=%d enabled=%t)", newCfg.Name, newCfg.Weight, newCfg.Enabled)
			return nil
		}
	}
	return fmt.Errorf("pool: endpoint %q not found", name)
}

func validateEndpoint(ec EndpointConfig) error {
	if ec.Name == "" {
		return fmt.Errorf("pool: endpoint name required")
	}
	if ec.URL == "" {
		return fmt.Errorf("pool: endpoint %q url required", ec.Name)
	}
	if _, err := url.Parse(ec.URL); err != nil {
		return fmt.Errorf("pool: endpoint %q url invalid: %w", ec.Name, err)
	}
	if ec.APIKeyEnv == "" {
		return fmt.Errorf("pool: endpoint %q api_key_env required", ec.Name)
	}
	if ec.Weight <= 0 {
		return fmt.Errorf("pool: endpoint %q weight must be > 0", ec.Name)
	}
	return nil
}
