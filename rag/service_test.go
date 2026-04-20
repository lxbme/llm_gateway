package rag_test

import (
	"context"
	"errors"
	"testing"

	"llm_gateway/embedding"
	"llm_gateway/rag"
)

// ---- mock Store ----

type mockStore struct {
	upsertFn      func(ctx context.Context, task rag.UpsertTask) error
	queryFn       func(ctx context.Context, task rag.QueryTask) ([]rag.RetrievedChunk, error)
	deleteByDocFn func(ctx context.Context, docID string, collection string) error

	upsertCalls []rag.UpsertTask
	queryCalls  []rag.QueryTask
}

func (m *mockStore) Upsert(ctx context.Context, task rag.UpsertTask) error {
	m.upsertCalls = append(m.upsertCalls, task)
	if m.upsertFn != nil {
		return m.upsertFn(ctx, task)
	}
	return nil
}

func (m *mockStore) Query(ctx context.Context, task rag.QueryTask) ([]rag.RetrievedChunk, error) {
	m.queryCalls = append(m.queryCalls, task)
	if m.queryFn != nil {
		return m.queryFn(ctx, task)
	}
	return nil, nil
}

func (m *mockStore) DeleteByDocID(ctx context.Context, docID string, collection string) error {
	if m.deleteByDocFn != nil {
		return m.deleteByDocFn(ctx, docID, collection)
	}
	return nil
}

func (m *mockStore) Close() error { return nil }

// ---- mock embedding.Service ----

type mockEmbedding struct {
	getFn func(ctx context.Context, text string) ([]float32, error)
}

func (m *mockEmbedding) Get(ctx context.Context, text string) ([]float32, error) {
	if m.getFn != nil {
		return m.getFn(ctx, text)
	}
	return []float32{0.1, 0.2, 0.3}, nil
}

func (m *mockEmbedding) Info(_ context.Context) (embedding.Info, error) {
	return embedding.Info{Provider: "mock", Model: "mock-emb", Dimensions: 3}, nil
}

// ---- helpers ----

func newService(t *testing.T, store rag.Store, emb embedding.Service) *rag.ServiceImpl {
	t.Helper()
	svc, err := rag.NewService(store, emb, 3, 0.6)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// ---- NewService tests ----

func TestNewService_NilStore(t *testing.T) {
	_, err := rag.NewService(nil, &mockEmbedding{}, 3, 0.6)
	if err == nil {
		t.Fatal("expected error for nil store, got nil")
	}
}

func TestNewService_NilEmbedding(t *testing.T) {
	_, err := rag.NewService(&mockStore{}, nil, 3, 0.6)
	if err == nil {
		t.Fatal("expected error for nil embedding, got nil")
	}
}

// ---- Ingest tests ----

func TestIngest_EmptyChunks(t *testing.T) {
	svc := newService(t, &mockStore{}, &mockEmbedding{})
	_, _, err := svc.Ingest(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for empty chunks, got nil")
	}
}

func TestIngest_AutoGeneratesDocAndChunkIDs(t *testing.T) {
	store := &mockStore{}
	svc := newService(t, store, &mockEmbedding{})

	chunks := []rag.Chunk{
		{Collection: "col", Content: "text A"},
		{Collection: "col", Content: "text B"},
	}

	docID, count, err := svc.Ingest(context.Background(), chunks)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if docID == "" {
		t.Error("expected a non-empty doc_id to be generated")
	}
	if count != 2 {
		t.Errorf("ingested_count: got %d, want 2", count)
	}
	if len(store.upsertCalls) != 2 {
		t.Fatalf("upsert calls: got %d, want 2", len(store.upsertCalls))
	}
	// All chunks must share the same doc_id.
	for i, call := range store.upsertCalls {
		if call.DocID != docID {
			t.Errorf("chunk %d: doc_id %q != generated %q", i, call.DocID, docID)
		}
		if call.ChunkID == "" {
			t.Errorf("chunk %d: chunk_id was not generated", i)
		}
	}
}

func TestIngest_PreservesExistingDocID(t *testing.T) {
	store := &mockStore{}
	svc := newService(t, store, &mockEmbedding{})

	wantDocID := "my-doc-123"
	chunks := []rag.Chunk{
		{DocID: wantDocID, Collection: "col", Content: "hello"},
	}

	gotDocID, _, err := svc.Ingest(context.Background(), chunks)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if gotDocID != wantDocID {
		t.Errorf("doc_id: got %q, want %q", gotDocID, wantDocID)
	}
}

func TestIngest_EmbeddingError(t *testing.T) {
	embErr := errors.New("embedding service down")
	emb := &mockEmbedding{getFn: func(_ context.Context, _ string) ([]float32, error) {
		return nil, embErr
	}}
	svc := newService(t, &mockStore{}, emb)

	_, _, err := svc.Ingest(context.Background(), []rag.Chunk{{Content: "x"}})
	if err == nil {
		t.Fatal("expected error from embedding, got nil")
	}
}

func TestIngest_StoreUpsertError(t *testing.T) {
	storeErr := errors.New("qdrant unavailable")
	store := &mockStore{upsertFn: func(_ context.Context, _ rag.UpsertTask) error {
		return storeErr
	}}
	svc := newService(t, store, &mockEmbedding{})

	_, _, err := svc.Ingest(context.Background(), []rag.Chunk{{Content: "x"}})
	if err == nil {
		t.Fatal("expected error from store, got nil")
	}
}

func TestIngest_VectorPassedToStore(t *testing.T) {
	wantVec := []float32{0.5, 0.6, 0.7}
	emb := &mockEmbedding{getFn: func(_ context.Context, _ string) ([]float32, error) {
		return wantVec, nil
	}}
	store := &mockStore{}
	svc := newService(t, store, emb)

	_, _, err := svc.Ingest(context.Background(), []rag.Chunk{{Content: "check vector"}})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(store.upsertCalls) == 0 {
		t.Fatal("no upsert calls recorded")
	}
	got := store.upsertCalls[0].Vector
	if len(got) != len(wantVec) {
		t.Fatalf("vector len: got %d, want %d", len(got), len(wantVec))
	}
	for i := range wantVec {
		if got[i] != wantVec[i] {
			t.Errorf("vector[%d]: got %f, want %f", i, got[i], wantVec[i])
		}
	}
}

// ---- Retrieve tests ----

func TestRetrieve_Success(t *testing.T) {
	wantChunks := []rag.RetrievedChunk{
		{ChunkID: "c1", Content: "Paris is the capital", Source: "geo.md", Score: 0.9},
	}
	store := &mockStore{queryFn: func(_ context.Context, _ rag.QueryTask) ([]rag.RetrievedChunk, error) {
		return wantChunks, nil
	}}
	svc := newService(t, store, &mockEmbedding{})

	got, err := svc.Retrieve(context.Background(), "capital of France", "geo-col", 3, 0.7)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("retrieved chunks: got %d, want 1", len(got))
	}
	if got[0].ChunkID != "c1" {
		t.Errorf("chunk_id: got %q, want %q", got[0].ChunkID, "c1")
	}
}

func TestRetrieve_EmbeddingError(t *testing.T) {
	emb := &mockEmbedding{getFn: func(_ context.Context, _ string) ([]float32, error) {
		return nil, errors.New("embed fail")
	}}
	svc := newService(t, &mockStore{}, emb)

	_, err := svc.Retrieve(context.Background(), "query", "col", 3, 0.6)
	if err == nil {
		t.Fatal("expected error from embedding, got nil")
	}
}

func TestRetrieve_StoreQueryError(t *testing.T) {
	store := &mockStore{queryFn: func(_ context.Context, _ rag.QueryTask) ([]rag.RetrievedChunk, error) {
		return nil, errors.New("qdrant error")
	}}
	svc := newService(t, store, &mockEmbedding{})

	_, err := svc.Retrieve(context.Background(), "query", "col", 3, 0.6)
	if err == nil {
		t.Fatal("expected error from store, got nil")
	}
}

func TestRetrieve_DefaultsApplied(t *testing.T) {
	store := &mockStore{}
	svc := newService(t, store, &mockEmbedding{})

	// Pass zero values: service should apply defaults internally.
	// gateway/rag.go is the only real caller and always passes 0,0 — so the
	// service-side default (driven by RAG_SIMILARITY_THRESHOLD env) must kick in.
	_, err := svc.Retrieve(context.Background(), "test", "col", 0, 0)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(store.queryCalls) == 0 {
		t.Fatal("no query calls recorded")
	}
	q := store.queryCalls[0]
	if q.TopK != 3 {
		t.Errorf("TopK default: got %d, want 3", q.TopK)
	}
	if q.Threshold != 0.6 {
		t.Errorf("Threshold default: got %f, want 0.6", q.Threshold)
	}
}

// When the operator configures defaultThreshold=0 (RAG_SIMILARITY_THRESHOLD=0),
// the service must propagate 0 to the store, where it signals "skip score filter".
func TestRetrieve_ZeroDefaultThreshold_PropagatesAsZero(t *testing.T) {
	store := &mockStore{}
	svc, err := rag.NewService(store, &mockEmbedding{}, 3, 0)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if _, err := svc.Retrieve(context.Background(), "test", "col", 0, 0); err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(store.queryCalls) == 0 {
		t.Fatal("no query calls recorded")
	}
	if got := store.queryCalls[0].Threshold; got != 0 {
		t.Errorf("Threshold: got %f, want 0 (env=0 must reach store as 0)", got)
	}
}

func TestRetrieve_CollectionPassedAsFilter(t *testing.T) {
	store := &mockStore{}
	svc := newService(t, store, &mockEmbedding{})

	wantCollection := "project-docs"
	_, err := svc.Retrieve(context.Background(), "query", wantCollection, 5, 0.7)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(store.queryCalls) == 0 {
		t.Fatal("no query calls recorded")
	}
	if store.queryCalls[0].Collection != wantCollection {
		t.Errorf("collection: got %q, want %q", store.queryCalls[0].Collection, wantCollection)
	}
}

// ---- DeleteDoc tests ----

func TestDeleteDoc_DelegatesToStore(t *testing.T) {
	var gotDocID, gotCollection string
	store := &mockStore{deleteByDocFn: func(_ context.Context, docID string, collection string) error {
		gotDocID = docID
		gotCollection = collection
		return nil
	}}
	svc := newService(t, store, &mockEmbedding{})

	err := svc.DeleteDoc(context.Background(), "doc-42", "my-col")
	if err != nil {
		t.Fatalf("DeleteDoc: %v", err)
	}
	if gotDocID != "doc-42" {
		t.Errorf("doc_id: got %q, want %q", gotDocID, "doc-42")
	}
	if gotCollection != "my-col" {
		t.Errorf("collection: got %q, want %q", gotCollection, "my-col")
	}
}

func TestDeleteDoc_PropagatesStoreError(t *testing.T) {
	store := &mockStore{deleteByDocFn: func(_ context.Context, _ string, _ string) error {
		return errors.New("store error")
	}}
	svc := newService(t, store, &mockEmbedding{})

	if err := svc.DeleteDoc(context.Background(), "x", "y"); err == nil {
		t.Fatal("expected store error to be propagated, got nil")
	}
}
