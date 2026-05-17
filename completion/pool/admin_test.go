package pool

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/sony/gobreaker"

	"llm_gateway/completion"
)

func newAdminTestSvc(t *testing.T, breakerEnabled bool, clients map[string]upstreamClient) *Service {
	t.Helper()
	cfg := Config{
		Strategy:    "weighted_random",
		MaxAttempts: 1,
		Breaker: BreakerConfig{
			Enabled:      breakerEnabled,
			FailureRatio: 0.5,
			MinRequests:  3,
			Interval:     "1m",
			Timeout:      "1m",
		},
		Endpoints: []EndpointConfig{
			{Name: "a", URL: "http://a", APIKeyEnv: "K", Weight: 1, Enabled: true},
			{Name: "b", URL: "http://b", APIKeyEnv: "K", Weight: 2, Enabled: true},
		},
	}
	svc, err := newFromConfig(cfg, func(ec EndpointConfig) upstreamClient {
		if c, ok := clients[ec.Name]; ok {
			return c
		}
		return &fakeClient{queue: []fakeResult{{ch: makeChunkChan("ok")}}}
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestAdmin_ListEndpoints(t *testing.T) {
	svc := newAdminTestSvc(t, false, nil)
	views, err := svc.ListEndpoints(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("expected 2, got %d", len(views))
	}
}

func TestAdmin_AddEndpoint_PickableImmediately(t *testing.T) {
	svc := newAdminTestSvc(t, false, nil)
	err := svc.AddEndpoint(context.Background(), completion.EndpointSpec{
		Name: "c", URL: "http://c", APIKeyEnv: "K", Weight: 1, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	views, _ := svc.ListEndpoints(context.Background())
	if len(views) != 3 {
		t.Fatalf("expected 3 endpoints after add, got %d", len(views))
	}
	// pick should be willing to return "c" eventually
	svc.selector = &orderedSelector{order: []string{"c"}}
	if _, err := svc.GetStream(context.Background(), &completion.CompletionRequest{}); err != nil {
		t.Fatalf("c not pickable: %v", err)
	}
}

func TestAdmin_AddDuplicateNameRejected(t *testing.T) {
	svc := newAdminTestSvc(t, false, nil)
	err := svc.AddEndpoint(context.Background(), completion.EndpointSpec{
		Name: "a", URL: "http://x", APIKeyEnv: "K", Weight: 1, Enabled: true,
	})
	if err == nil {
		t.Fatal("expected duplicate name error")
	}
}

func TestAdmin_AddInvalidSpecRejected(t *testing.T) {
	svc := newAdminTestSvc(t, false, nil)
	cases := []completion.EndpointSpec{
		{Name: "", URL: "http://x", APIKeyEnv: "K", Weight: 1},
		{Name: "x", URL: "", APIKeyEnv: "K", Weight: 1},
		{Name: "x", URL: "http://x", APIKeyEnv: "", Weight: 1},
		{Name: "x", URL: "http://x", APIKeyEnv: "K", Weight: 0},
	}
	for i, c := range cases {
		if err := svc.AddEndpoint(context.Background(), c); err == nil {
			t.Fatalf("case %d should fail validation: %+v", i, c)
		}
	}
}

func TestAdmin_RemoveEndpoint(t *testing.T) {
	svc := newAdminTestSvc(t, false, nil)
	if err := svc.RemoveEndpoint(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	views, _ := svc.ListEndpoints(context.Background())
	if len(views) != 1 || views[0].Name != "b" {
		t.Fatalf("after remove a expected only b, got %+v", views)
	}
	if err := svc.RemoveEndpoint(context.Background(), "nope"); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestAdmin_Reweight(t *testing.T) {
	svc := newAdminTestSvc(t, false, nil)
	if err := svc.Reweight(context.Background(), "a", 9); err != nil {
		t.Fatal(err)
	}
	views, _ := svc.ListEndpoints(context.Background())
	var wA int
	for _, v := range views {
		if v.Name == "a" {
			wA = v.Weight
		}
	}
	if wA != 9 {
		t.Fatalf("expected weight 9, got %d", wA)
	}
	if err := svc.Reweight(context.Background(), "a", 0); err == nil {
		t.Fatal("expected error for weight <= 0")
	}
}

func TestAdmin_SetEnabled(t *testing.T) {
	svc := newAdminTestSvc(t, false, nil)
	if err := svc.SetEnabled(context.Background(), "a", false); err != nil {
		t.Fatal(err)
	}
	views, _ := svc.ListEndpoints(context.Background())
	for _, v := range views {
		if v.Name == "a" && v.Enabled {
			t.Fatal("a should be disabled")
		}
	}
}

func TestAdmin_ResetBreaker(t *testing.T) {
	bad := &fakeClient{queue: []fakeResult{{err: errors.New("boom")}}}
	svc := newAdminTestSvc(t, true, map[string]upstreamClient{"a": bad})
	svc.selector = &orderedSelector{order: []string{"a"}}

	// Trip the breaker.
	for range 5 {
		_, _ = svc.GetStream(context.Background(), &completion.CompletionRequest{})
	}
	var ep *Endpoint
	for _, e := range svc.endpoints {
		if e.Cfg.Name == "a" {
			ep = e
		}
	}
	if ep.Breaker.State() != gobreaker.StateOpen {
		t.Fatalf("expected breaker open, got %s", ep.Breaker.State())
	}

	if err := svc.ResetBreaker(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	// After reset, locate the new endpoint pointer (COW replacement).
	for _, e := range svc.endpoints {
		if e.Cfg.Name == "a" {
			ep = e
		}
	}
	if ep.Breaker.State() != gobreaker.StateClosed {
		t.Fatalf("expected breaker closed after reset, got %s", ep.Breaker.State())
	}
}

func TestAdmin_ResetBreaker_DisabledReturnsError(t *testing.T) {
	svc := newAdminTestSvc(t, false, nil)
	if err := svc.ResetBreaker(context.Background(), "a"); err == nil {
		t.Fatal("expected error when breaker not enabled")
	}
}

// Mutations during in-flight requests must not affect the snapshot taken at start.
func TestAdmin_RemoveDuringInflightDoesNotPanic(t *testing.T) {
	// Use a blocking client we can release on demand.
	release := make(chan struct{})
	blockedCh := make(chan *completion.CompletionChunk, 1)
	go func() {
		<-release
		blockedCh <- &completion.CompletionChunk{Content: "late"}
		blockedCh <- &completion.CompletionChunk{Done: true}
		close(blockedCh)
	}()
	bClient := &fakeClient{queue: []fakeResult{{ch: blockedCh}}}

	svc := newAdminTestSvc(t, false, map[string]upstreamClient{"a": bClient})
	svc.selector = &orderedSelector{order: []string{"a"}}

	ch, err := svc.GetStream(context.Background(), &completion.CompletionRequest{})
	if err != nil {
		t.Fatal(err)
	}

	// Concurrently remove "a" while the request is in flight.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := svc.RemoveEndpoint(context.Background(), "a"); err != nil {
			t.Errorf("remove failed: %v", err)
		}
	}()

	close(release)
	// Drain the stream — should complete without panic.
	for c := range ch {
		_ = c
	}
	wg.Wait()
}
