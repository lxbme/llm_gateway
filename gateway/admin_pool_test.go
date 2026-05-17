package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"llm_gateway/completion"
)

// mockCompletionAdmin records calls and replays canned responses.
type mockCompletionAdmin struct {
	mu             sync.Mutex
	listResp       []completion.EndpointView
	listErr        error
	addCalls       []completion.EndpointSpec
	addErr         error
	removeCalls    []string
	removeErr      error
	reweightCalls  []struct {
		Name   string
		Weight int
	}
	reweightErr      error
	setEnabledCalls  []struct {
		Name    string
		Enabled bool
	}
	setEnabledErr  error
	resetCalls     []string
	resetErr       error
}

func (m *mockCompletionAdmin) ListEndpoints(_ context.Context) ([]completion.EndpointView, error) {
	return m.listResp, m.listErr
}
func (m *mockCompletionAdmin) AddEndpoint(_ context.Context, s completion.EndpointSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addCalls = append(m.addCalls, s)
	return m.addErr
}
func (m *mockCompletionAdmin) RemoveEndpoint(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeCalls = append(m.removeCalls, name)
	return m.removeErr
}
func (m *mockCompletionAdmin) Reweight(_ context.Context, name string, w int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reweightCalls = append(m.reweightCalls, struct {
		Name   string
		Weight int
	}{name, w})
	return m.reweightErr
}
func (m *mockCompletionAdmin) SetEnabled(_ context.Context, name string, en bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setEnabledCalls = append(m.setEnabledCalls, struct {
		Name    string
		Enabled bool
	}{name, en})
	return m.setEnabledErr
}
func (m *mockCompletionAdmin) ResetBreaker(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resetCalls = append(m.resetCalls, name)
	return m.resetErr
}

func newAdminTestServer(t *testing.T, deps Dependencies) (*Server, *http.ServeMux) {
	t.Helper()
	srv := NewServer(deps)
	mux := http.NewServeMux()
	srv.RegisterAdminRoutes(mux)
	return srv, mux
}

func TestAdminPool_List(t *testing.T) {
	m := &mockCompletionAdmin{
		listResp: []completion.EndpointView{
			{Name: "a", URL: "http://a", APIKeyEnv: "K", Weight: 3, Enabled: true, BreakerState: "closed"},
		},
	}
	_, mux := newAdminTestServer(t, Dependencies{CompletionAdmin: m})

	req := httptest.NewRequest("GET", "/admin/completion/endpoints", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Endpoints []completion.EndpointView `json:"endpoints"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Endpoints) != 1 || body.Endpoints[0].Name != "a" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestAdminPool_Add(t *testing.T) {
	m := &mockCompletionAdmin{}
	_, mux := newAdminTestServer(t, Dependencies{CompletionAdmin: m})

	body := `{"name":"c","url":"http://c","api_key_env":"K","weight":2,"enabled":true,"models":["gpt-4o"]}`
	req := httptest.NewRequest("POST", "/admin/completion/endpoint", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(m.addCalls) != 1 || m.addCalls[0].Name != "c" || m.addCalls[0].Weight != 2 {
		t.Fatalf("unexpected add calls: %+v", m.addCalls)
	}
	if !m.addCalls[0].Enabled || m.addCalls[0].Models[0] != "gpt-4o" {
		t.Fatalf("unexpected spec: %+v", m.addCalls[0])
	}
}

func TestAdminPool_Reweight(t *testing.T) {
	m := &mockCompletionAdmin{}
	_, mux := newAdminTestServer(t, Dependencies{CompletionAdmin: m})

	req := httptest.NewRequest("POST", "/admin/completion/endpoint/weight", strings.NewReader(`{"name":"a","weight":10}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(m.reweightCalls) != 1 || m.reweightCalls[0].Name != "a" || m.reweightCalls[0].Weight != 10 {
		t.Fatalf("unexpected reweight: %+v", m.reweightCalls)
	}
}

func TestAdminPool_SetEnabled(t *testing.T) {
	m := &mockCompletionAdmin{}
	_, mux := newAdminTestServer(t, Dependencies{CompletionAdmin: m})

	req := httptest.NewRequest("POST", "/admin/completion/endpoint/enabled", strings.NewReader(`{"name":"a","enabled":false}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(m.setEnabledCalls) != 1 || m.setEnabledCalls[0].Name != "a" || m.setEnabledCalls[0].Enabled {
		t.Fatalf("unexpected set-enabled: %+v", m.setEnabledCalls)
	}
}

func TestAdminPool_Remove(t *testing.T) {
	m := &mockCompletionAdmin{}
	_, mux := newAdminTestServer(t, Dependencies{CompletionAdmin: m})

	req := httptest.NewRequest("DELETE", "/admin/completion/endpoint", strings.NewReader(`{"name":"a"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(m.removeCalls) != 1 || m.removeCalls[0] != "a" {
		t.Fatalf("unexpected remove: %+v", m.removeCalls)
	}
}

func TestAdminPool_BreakerReset(t *testing.T) {
	m := &mockCompletionAdmin{}
	_, mux := newAdminTestServer(t, Dependencies{CompletionAdmin: m})

	req := httptest.NewRequest("POST", "/admin/completion/breaker/reset", strings.NewReader(`{"name":"a"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(m.resetCalls) != 1 || m.resetCalls[0] != "a" {
		t.Fatalf("unexpected reset: %+v", m.resetCalls)
	}
}

func TestAdminPool_NilAdminReturns503(t *testing.T) {
	_, mux := newAdminTestServer(t, Dependencies{}) // CompletionAdmin == nil
	req := httptest.NewRequest("GET", "/admin/completion/endpoints", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestAdminPool_PropagatesError(t *testing.T) {
	m := &mockCompletionAdmin{removeErr: errors.New("not found")}
	_, mux := newAdminTestServer(t, Dependencies{CompletionAdmin: m})

	req := httptest.NewRequest("DELETE", "/admin/completion/endpoint", strings.NewReader(`{"name":"missing"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "not found") {
		t.Fatalf("error not propagated: %s", w.Body.String())
	}
}

func TestAdminPool_MissingNameRejected(t *testing.T) {
	m := &mockCompletionAdmin{}
	_, mux := newAdminTestServer(t, Dependencies{CompletionAdmin: m})

	req := httptest.NewRequest("POST", "/admin/completion/endpoint/weight", strings.NewReader(`{"weight":5}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}
