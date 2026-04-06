package gateway

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"llm_gateway/rag"
)

// mockRAGService implements rag.Service for handler tests.
type mockRAGService struct {
	retrieveFn func(ctx context.Context, query, collection string, topK int32, threshold float32) ([]rag.RetrievedChunk, error)
}

func (m *mockRAGService) Ingest(_ context.Context, _ []rag.Chunk) (string, int, error) {
	return "", 0, nil
}

func (m *mockRAGService) Retrieve(ctx context.Context, query, collection string, topK int32, threshold float32) ([]rag.RetrievedChunk, error) {
	if m.retrieveFn != nil {
		return m.retrieveFn(ctx, query, collection, topK, threshold)
	}
	return nil, nil
}

func (m *mockRAGService) DeleteDoc(_ context.Context, _, _ string) error { return nil }
func (m *mockRAGService) Close() error                                    { return nil }

// newTestGatewayContext builds a minimal GatewayContext suitable for handler unit tests.
func newTestGatewayContext(deps Dependencies) *GatewayContext {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	return newGatewayContext(w, r, deps)
}

// ---- handleRAGRetrieveStage tests ----

func TestRAGRetrieve_NilService_Continues(t *testing.T) {
	gw := newTestGatewayContext(Dependencies{}) // RAG == nil
	gw.Request.PromptText = "original prompt"

	result := handleRAGRetrieveStage(gw)

	if result.Action != ActionContinue {
		t.Errorf("action: got %q, want %q", result.Action, ActionContinue)
	}
	if gw.Request.PromptText != "original prompt" {
		t.Error("prompt must not be modified when RAG service is nil")
	}
}

func TestRAGRetrieve_NoCollection_Continues(t *testing.T) {
	// Neither X-RAG-Collection header nor Auth.Subject is set.
	deps := Dependencies{RAG: &mockRAGService{}}
	gw := newTestGatewayContext(deps)
	gw.Auth.Subject = ""
	gw.Request.PromptText = "original"

	result := handleRAGRetrieveStage(gw)

	if result.Action != ActionContinue {
		t.Errorf("action: got %q, want %q", result.Action, ActionContinue)
	}
	if gw.Request.PromptText != "original" {
		t.Error("prompt must not be modified when collection is empty")
	}
}

func TestRAGRetrieve_CollectionFromHeader(t *testing.T) {
	var gotCollection string
	deps := Dependencies{
		RAG: &mockRAGService{
			retrieveFn: func(_ context.Context, _, collection string, _ int32, _ float32) ([]rag.RetrievedChunk, error) {
				gotCollection = collection
				return []rag.RetrievedChunk{{Content: "ctx", Source: "src"}}, nil
			},
		},
	}
	gw := newTestGatewayContext(deps)
	gw.Request.Header.Set("X-RAG-Collection", "project-docs")
	gw.Auth.Subject = "some-user" // should be ignored in favour of header
	gw.Request.PromptText = "query"

	result := handleRAGRetrieveStage(gw)

	if result.Action != ActionContinue {
		t.Errorf("action: got %q, want %q", result.Action, ActionContinue)
	}
	if gotCollection != "project-docs" {
		t.Errorf("collection: got %q, want %q", gotCollection, "project-docs")
	}
}

func TestRAGRetrieve_CollectionFallsBackToAuthSubject(t *testing.T) {
	var gotCollection string
	deps := Dependencies{
		RAG: &mockRAGService{
			retrieveFn: func(_ context.Context, _, collection string, _ int32, _ float32) ([]rag.RetrievedChunk, error) {
				gotCollection = collection
				return []rag.RetrievedChunk{{Content: "ctx", Source: "src"}}, nil
			},
		},
	}
	gw := newTestGatewayContext(deps)
	// No header set; Auth.Subject is the fallback.
	gw.Auth.Subject = "alice"
	gw.Request.PromptText = "query"

	handleRAGRetrieveStage(gw)

	if gotCollection != "alice" {
		t.Errorf("collection fallback: got %q, want %q", gotCollection, "alice")
	}
}

func TestRAGRetrieve_ServiceError_DegradesContinue(t *testing.T) {
	deps := Dependencies{
		RAG: &mockRAGService{
			retrieveFn: func(_ context.Context, _, _ string, _ int32, _ float32) ([]rag.RetrievedChunk, error) {
				return nil, errors.New("rag service unavailable")
			},
		},
	}
	gw := newTestGatewayContext(deps)
	gw.Auth.Subject = "user"
	original := "my question"
	gw.Request.PromptText = original

	result := handleRAGRetrieveStage(gw)

	if result.Action != ActionContinue {
		t.Errorf("action: got %q, want %q (must degrade)", result.Action, ActionContinue)
	}
	if gw.Request.PromptText != original {
		t.Error("prompt must not be modified on RAG error")
	}
}

func TestRAGRetrieve_EmptyResults_PromptUnchanged(t *testing.T) {
	deps := Dependencies{
		RAG: &mockRAGService{
			retrieveFn: func(_ context.Context, _, _ string, _ int32, _ float32) ([]rag.RetrievedChunk, error) {
				return []rag.RetrievedChunk{}, nil // zero hits
			},
		},
	}
	gw := newTestGatewayContext(deps)
	gw.Auth.Subject = "user"
	original := "my question"
	gw.Request.PromptText = original

	result := handleRAGRetrieveStage(gw)

	if result.Action != ActionContinue {
		t.Errorf("action: got %q, want %q", result.Action, ActionContinue)
	}
	if gw.Request.PromptText != original {
		t.Error("prompt must not be modified when no chunks are retrieved")
	}
}

func TestRAGRetrieve_Success_PromptAugmented(t *testing.T) {
	chunks := []rag.RetrievedChunk{
		{ChunkID: "c1", Content: "Paris is the capital of France", Source: "geo.md", Score: 0.92},
		{ChunkID: "c2", Content: "France is in Western Europe", Source: "geo.md", Score: 0.85},
	}
	deps := Dependencies{
		RAG: &mockRAGService{
			retrieveFn: func(_ context.Context, _, _ string, _ int32, _ float32) ([]rag.RetrievedChunk, error) {
				return chunks, nil
			},
		},
	}
	gw := newTestGatewayContext(deps)
	gw.Auth.Subject = "user"
	gw.Request.PromptText = "What is the capital of France?"

	result := handleRAGRetrieveStage(gw)

	if result.Action != ActionContinue {
		t.Errorf("action: got %q, want %q", result.Action, ActionContinue)
	}

	prompt := gw.Request.PromptText

	// Prompt must contain the retrieved contents and the original question.
	for _, c := range chunks {
		if !strings.Contains(prompt, c.Content) {
			t.Errorf("augmented prompt missing chunk content %q", c.Content)
		}
		if !strings.Contains(prompt, c.Source) {
			t.Errorf("augmented prompt missing source %q", c.Source)
		}
	}
	if !strings.Contains(prompt, "What is the capital of France?") {
		t.Error("augmented prompt must contain the original user question")
	}

	// Data fields must be set.
	if count, ok := gw.Data["rag_chunks_count"]; !ok || count != 2 {
		t.Errorf("rag_chunks_count: got %v, want 2", gw.Data["rag_chunks_count"])
	}
	if col, ok := gw.Data["rag_collection"]; !ok || col != "user" {
		t.Errorf("rag_collection: got %v, want %q", gw.Data["rag_collection"], "user")
	}
}

func TestRAGRetrieve_QueryPassedToService(t *testing.T) {
	var gotQuery string
	deps := Dependencies{
		RAG: &mockRAGService{
			retrieveFn: func(_ context.Context, query, _ string, _ int32, _ float32) ([]rag.RetrievedChunk, error) {
				gotQuery = query
				return nil, nil
			},
		},
	}
	gw := newTestGatewayContext(deps)
	gw.Auth.Subject = "user"
	gw.Request.PromptText = "tell me about Go channels"

	handleRAGRetrieveStage(gw)

	if gotQuery != "tell me about Go channels" {
		t.Errorf("query passed to RAG: got %q, want %q", gotQuery, "tell me about Go channels")
	}
}
