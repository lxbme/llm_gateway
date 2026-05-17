package pool

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sony/gobreaker"

	"llm_gateway/completion"
)

var errBoom = errors.New("boom")

func TestBreaker_OpensAfterFailures(t *testing.T) {
	b, err := newBreaker("e", BreakerConfig{
		Enabled:      true,
		MaxRequests:  1,
		FailureRatio: 0.5,
		MinRequests:  5,
		Interval:     "1m",
		Timeout:      "1m",
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		_, _ = b.Execute(func() (any, error) { return nil, errBoom })
		if i < 4 && b.State() == gobreaker.StateOpen {
			t.Fatalf("breaker opened prematurely at iter %d", i)
		}
	}
	if b.State() != gobreaker.StateOpen {
		t.Fatalf("expected open after 5 failures, got %s", b.State())
	}
}

func TestBreaker_HalfOpenAllowsTrial(t *testing.T) {
	b, err := newBreaker("e", BreakerConfig{
		Enabled:      true,
		MaxRequests:  1,
		FailureRatio: 0.1,
		MinRequests:  1,
		Interval:     "1m",
		Timeout:      "100ms",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Trip open.
	_, _ = b.Execute(func() (any, error) { return nil, errBoom })
	if b.State() != gobreaker.StateOpen {
		t.Fatalf("expected open, got %s", b.State())
	}

	// Wait past timeout so breaker transitions open -> half-open on next request.
	time.Sleep(150 * time.Millisecond)

	// Half-open allows a trial. Successful trial closes the breaker.
	_, err = b.Execute(func() (any, error) { return "ok", nil })
	if err != nil {
		t.Fatalf("trial request rejected: %v", err)
	}
	if b.State() != gobreaker.StateClosed {
		t.Fatalf("expected closed after successful trial, got %s", b.State())
	}
}

func TestBreaker_PerEndpointIsolation(t *testing.T) {
	cfg := Config{
		Strategy:    "weighted_random",
		MaxAttempts: 1, // each request tries exactly one endpoint
		Breaker: BreakerConfig{
			Enabled:      true,
			FailureRatio: 0.5,
			MinRequests:  3,
			Interval:     "1m",
			Timeout:      "1m",
		},
		Endpoints: []EndpointConfig{
			{Name: "bad", URL: "http://x", APIKeyEnv: "K", Weight: 1, Enabled: true},
			{Name: "good", URL: "http://x", APIKeyEnv: "K", Weight: 1, Enabled: true},
		},
	}
	badC := &fakeClient{queue: []fakeResult{{err: errBoom}}}
	goodC := &fakeClient{queue: []fakeResult{{ch: makeChunkChan("ok"), err: nil}}}
	factory := func(ec EndpointConfig) upstreamClient {
		if ec.Name == "bad" {
			return badC
		}
		return goodC
	}
	svc, err := newFromConfig(cfg, factory)
	if err != nil {
		t.Fatal(err)
	}
	// Replace selector with ordered to deterministically hit "bad".
	svc.selector = &orderedSelector{order: []string{"bad"}}

	for range 5 {
		_, _ = svc.GetStream(context.Background(), &completion.CompletionRequest{})
	}

	var badEP, goodEP *Endpoint
	for _, ep := range svc.endpoints {
		if ep.Cfg.Name == "bad" {
			badEP = ep
		}
		if ep.Cfg.Name == "good" {
			goodEP = ep
		}
	}
	if badEP.Breaker.State() != gobreaker.StateOpen {
		t.Fatalf("expected bad breaker open, got %s", badEP.Breaker.State())
	}
	if goodEP.Breaker.State() != gobreaker.StateClosed {
		t.Fatalf("expected good breaker closed (untouched), got %s", goodEP.Breaker.State())
	}
}

func TestPool_BreakerOpenFilterExcludes(t *testing.T) {
	cfg := Config{
		Strategy:    "weighted_random",
		MaxAttempts: 3,
		Breaker: BreakerConfig{
			Enabled:      true,
			FailureRatio: 0.1,
			MinRequests:  1,
			Interval:     "1m",
			Timeout:      "1m",
		},
		Endpoints: []EndpointConfig{
			{Name: "a", URL: "http://x", APIKeyEnv: "K", Weight: 1, Enabled: true},
			{Name: "b", URL: "http://x", APIKeyEnv: "K", Weight: 1, Enabled: true},
		},
	}
	aClient := &fakeClient{queue: []fakeResult{{err: errBoom}}}
	bClient := &fakeClient{queue: []fakeResult{
		{ch: makeChunkChan("ok"), err: nil},
		{ch: makeChunkChan("ok"), err: nil},
		{ch: makeChunkChan("ok"), err: nil},
	}}
	factory := func(ec EndpointConfig) upstreamClient {
		if ec.Name == "a" {
			return aClient
		}
		return bClient
	}
	svc, err := newFromConfig(cfg, factory)
	if err != nil {
		t.Fatal(err)
	}
	svc.selector = &orderedSelector{order: []string{"a", "b"}}

	// First call: a fails, retries to b -> success. a's breaker trips because MinRequests=1, ratio=0.1.
	if _, err := svc.GetStream(context.Background(), &completion.CompletionRequest{}); err != nil {
		t.Fatalf("first call expected success after failover, got %v", err)
	}

	var aEP *Endpoint
	for _, ep := range svc.endpoints {
		if ep.Cfg.Name == "a" {
			aEP = ep
		}
	}
	if aEP.Breaker.State() != gobreaker.StateOpen {
		t.Fatalf("expected a's breaker open, got %s", aEP.Breaker.State())
	}

	// Second call: filter should exclude a; b must be picked directly.
	if _, err := svc.GetStream(context.Background(), &completion.CompletionRequest{}); err != nil {
		t.Fatalf("second call expected success, got %v", err)
	}
	// a should NOT be called a second time (breaker open keeps it excluded).
	if aClient.calls != 1 {
		t.Fatalf("expected a called only once (filtered after open), got %d", aClient.calls)
	}
}

func TestConfig_BreakerEnabledValidationCatchesBadDuration(t *testing.T) {
	cfg := Config{
		Endpoints: []EndpointConfig{
			{Name: "a", URL: "http://x", APIKeyEnv: "K", Weight: 1, Enabled: true},
		},
		Breaker: BreakerConfig{Enabled: true, Interval: "not-a-duration"},
	}
	if err := validate(&cfg); err == nil {
		t.Fatal("expected validate to reject bad duration")
	}
}
