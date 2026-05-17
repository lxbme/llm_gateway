package pool

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"llm_gateway/completion"
)

type fakeClient struct {
	name  string
	mu    sync.Mutex
	calls int
	queue []fakeResult // FIFO; repeats last when exhausted
}

type fakeResult struct {
	ch  <-chan *completion.CompletionChunk
	err error
}

func (f *fakeClient) GetStream(ctx context.Context, req *completion.CompletionRequest) (<-chan *completion.CompletionChunk, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.queue) == 0 {
		return nil, errors.New("fake: no result queued")
	}
	r := f.queue[0]
	if len(f.queue) > 1 {
		f.queue = f.queue[1:]
	}
	return r.ch, r.err
}

// testEndpoint builds an Endpoint with Stats pre-initialized (newFromConfig does this in
// production; tests that hand-roll Services need the same).
func testEndpoint(name string, weight int, enabled bool, c upstreamClient) *Endpoint {
	return &Endpoint{
		Cfg:    EndpointConfig{Name: name, Weight: weight, Enabled: enabled},
		Client: c,
		Stats:  &endpointStats{},
	}
}

// orderedSelector picks endpoints in a deterministic order, skipping tried.
type orderedSelector struct {
	order []string
}

func (s *orderedSelector) Name() string { return "ordered_test" }
func (s *orderedSelector) Pick(_ *completion.CompletionRequest, cs []*Endpoint, tried map[string]struct{}) (*Endpoint, bool) {
	for _, name := range s.order {
		if _, t := tried[name]; t {
			continue
		}
		for _, ep := range cs {
			if ep.Cfg.Name == name && ep.Cfg.Enabled {
				return ep, true
			}
		}
	}
	return nil, false
}

func makeChunkChan(content string) <-chan *completion.CompletionChunk {
	ch := make(chan *completion.CompletionChunk, 2)
	ch <- &completion.CompletionChunk{Content: content}
	ch <- &completion.CompletionChunk{Done: true, TokenUsage: 1}
	close(ch)
	return ch
}

func TestPool_RetriesOnSyncError(t *testing.T) {
	a := &fakeClient{name: "a", queue: []fakeResult{{err: errors.New("boom")}}}
	b := &fakeClient{name: "b", queue: []fakeResult{{ch: makeChunkChan("ok"), err: nil}}}

	svc := &Service{
		endpoints: []*Endpoint{
			testEndpoint("a", 1, true, a),
			testEndpoint("b", 1, true, b),
		},
		selector:    &orderedSelector{order: []string{"a", "b"}},
		maxAttempts: 3,
	}

	ch, err := svc.GetStream(context.Background(), &completion.CompletionRequest{Model: "m"})
	if err != nil {
		t.Fatalf("GetStream: %v", err)
	}
	got := <-ch
	if got.Content != "ok" {
		t.Fatalf("expected content 'ok', got %q", got.Content)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Fatalf("expected a=1 b=1, got a=%d b=%d", a.calls, b.calls)
	}
}

func TestPool_ExhaustsAttempts(t *testing.T) {
	mkErr := func() *fakeClient {
		return &fakeClient{queue: []fakeResult{{err: errors.New("always fail")}}}
	}
	a, b := mkErr(), mkErr()

	svc := &Service{
		endpoints: []*Endpoint{
			testEndpoint("a", 1, true, a),
			testEndpoint("b", 1, true, b),
		},
		selector:    &orderedSelector{order: []string{"a", "b"}},
		maxAttempts: 5,
	}

	_, err := svc.GetStream(context.Background(), &completion.CompletionRequest{Model: "m"})
	if err == nil {
		t.Fatal("expected error from exhausted pool")
	}
	if !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("expected error to mention exhausted, got %v", err)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Fatalf("expected each endpoint called once, got a=%d b=%d", a.calls, b.calls)
	}
}

func TestPool_ContextCancelled(t *testing.T) {
	a := &fakeClient{queue: []fakeResult{{ch: makeChunkChan("x"), err: nil}}}
	cfg := Config{
		Strategy:    "weighted_random",
		MaxAttempts: 3,
		Endpoints: []EndpointConfig{
			{Name: "a", URL: "http://a", APIKeyEnv: "K", Weight: 1, Enabled: true},
		},
	}
	factory := func(EndpointConfig) upstreamClient { return a }
	svc, err := newFromConfig(cfg, factory)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = svc.GetStream(ctx, &completion.CompletionRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if a.calls != 0 {
		t.Fatalf("expected no client call on cancelled ctx, got %d", a.calls)
	}
}

func TestPool_NoEligibleEndpoint(t *testing.T) {
	cfg := Config{
		Strategy:    "weighted_random",
		MaxAttempts: 3,
		Endpoints: []EndpointConfig{
			{Name: "a", URL: "http://a", APIKeyEnv: "K", Weight: 1, Enabled: false},
			{Name: "b", URL: "http://b", APIKeyEnv: "K", Weight: 1, Enabled: true},
		},
	}
	factory := func(EndpointConfig) upstreamClient {
		return &fakeClient{queue: []fakeResult{{ch: makeChunkChan("x")}}}
	}
	svc, err := newFromConfig(cfg, factory)
	if err != nil {
		t.Fatal(err)
	}
	// only "b" is eligible; succeeds.
	if _, err := svc.GetStream(context.Background(), &completion.CompletionRequest{}); err != nil {
		t.Fatalf("expected success when one endpoint enabled, got %v", err)
	}
}

func TestPool_LegacyConfigSynthesis(t *testing.T) {
	t.Setenv(envPoolConfigFile, "")
	t.Setenv(envPoolConfig, "")
	t.Setenv(envLegacyEndpoint, "https://example.test/v1/chat/completions")
	t.Setenv(envLegacyAPIKey, "sk-test")
	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}
	if len(cfg.Endpoints) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(cfg.Endpoints))
	}
	ep := cfg.Endpoints[0]
	if ep.Name != legacyEndpointName || ep.APIKeyEnv != envLegacyAPIKey {
		t.Fatalf("legacy endpoint not synthesized correctly: %+v", ep)
	}
}

func TestPool_NoConfigErrors(t *testing.T) {
	t.Setenv(envPoolConfigFile, "")
	t.Setenv(envPoolConfig, "")
	t.Setenv(envLegacyEndpoint, "")
	t.Setenv(envLegacyAPIKey, "")
	if _, err := LoadConfigFromEnv(); err == nil {
		t.Fatal("expected error when no config env present")
	}
}

func TestPool_InlineJSONParsed(t *testing.T) {
	t.Setenv(envPoolConfigFile, "")
	t.Setenv(envPoolConfig, `{
        "strategy": "weighted_random",
        "max_attempts": 4,
        "endpoints": [
            {"name":"a","url":"http://a","api_key_env":"K1","weight":3,"enabled":true},
            {"name":"b","url":"http://b","api_key_env":"K2","weight":1,"enabled":true}
        ]
    }`)
	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}
	if cfg.MaxAttempts != 4 || len(cfg.Endpoints) != 2 {
		t.Fatalf("unexpected cfg: %+v", cfg)
	}
}

func TestPool_DuplicateNameRejected(t *testing.T) {
	t.Setenv(envPoolConfigFile, "")
	t.Setenv(envPoolConfig, `{
        "endpoints":[
            {"name":"x","url":"http://a","api_key_env":"K","weight":1,"enabled":true},
            {"name":"x","url":"http://b","api_key_env":"K","weight":1,"enabled":true}
        ]
    }`)
	if _, err := LoadConfigFromEnv(); err == nil {
		t.Fatal("expected duplicate-name error")
	}
}

// Ensure concurrent GetStream calls don't race on snapshot/selector access.
func TestPool_ConcurrentSafe(t *testing.T) {
	supplier := &fakeClient{queue: []fakeResult{{
		ch: makeChunkChan("ok"), err: nil,
	}}}
	// Queue more to satisfy 20 concurrent callers (fakeClient returns last entry once exhausted).
	for range 30 {
		supplier.queue = append(supplier.queue, fakeResult{ch: makeChunkChan("ok")})
	}
	svc := &Service{
		endpoints: []*Endpoint{
			testEndpoint("a", 1, true, supplier),
		},
		selector:    &orderedSelector{order: []string{"a"}},
		maxAttempts: 1,
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = svc.GetStream(context.Background(), &completion.CompletionRequest{})
		}()
	}
	wg.Wait()
}
