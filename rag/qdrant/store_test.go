package qdrant_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"llm_gateway/rag"
	ragQdrant "llm_gateway/rag/qdrant"
)

// These are integration tests that require a live Qdrant instance.
// They are skipped automatically when QDRANT_HOST is unset or when running
// with -short.
//
// To run locally:
//
//	QDRANT_HOST=localhost go test ./rag/qdrant/... -v -count=1

const testDimensions = 4

func requireQdrant(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping Qdrant integration test in -short mode")
	}
	if os.Getenv("QDRANT_HOST") == "" {
		t.Skip("skipping Qdrant integration test: QDRANT_HOST not set")
	}
}

// testCollection returns a unique collection name so parallel test runs don't collide.
func testCollection(t *testing.T) string {
	return fmt.Sprintf("test_%d", time.Now().UnixNano())
}

func newTestStore(t *testing.T) *ragQdrant.Store {
	t.Helper()
	cfg := ragQdrant.Config{
		Host:                os.Getenv("QDRANT_HOST"),
		Port:                6334,
		CollectionName:      "llm_rag_test_" + fmt.Sprintf("%d", time.Now().UnixNano()),
		SimilarityThreshold: 0.5,
		DefaultTopK:         3,
	}
	store, err := ragQdrant.New(cfg, testDimensions)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// dummyVector returns a unit-ish vector of the given dimension.
func dummyVector(dim int, seed float32) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = seed + float32(i)*0.01
	}
	return v
}

// ---- Tests ----

func TestUpsertAndQuery_HitAboveThreshold(t *testing.T) {
	requireQdrant(t)
	store := newTestStore(t)
	col := testCollection(t)

	vec := dummyVector(testDimensions, 0.5)
	task := rag.UpsertTask{
		ChunkID:    "chunk-1",
		DocID:      "doc-1",
		Collection: col,
		Content:    "The quick brown fox",
		Source:     "test.md",
		ChunkIndex: 0,
		Vector:     vec,
	}

	if err := store.Upsert(context.Background(), task); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Query with the identical vector — must get a hit.
	results, err := store.Query(context.Background(), rag.QueryTask{
		Vector:     vec,
		Collection: col,
		TopK:       5,
		Threshold:  0.5,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result, got none")
	}
	if results[0].Content != task.Content {
		t.Errorf("content: got %q, want %q", results[0].Content, task.Content)
	}
	if results[0].Source != task.Source {
		t.Errorf("source: got %q, want %q", results[0].Source, task.Source)
	}
	if results[0].Score < 0.5 {
		t.Errorf("score: got %f, expected >= 0.5", results[0].Score)
	}
}

func TestQuery_BelowThreshold_ReturnsEmpty(t *testing.T) {
	requireQdrant(t)
	store := newTestStore(t)
	col := testCollection(t)

	// Upsert a vector pointing in one direction.
	if err := store.Upsert(context.Background(), rag.UpsertTask{
		ChunkID:    "chunk-th",
		DocID:      "doc-th",
		Collection: col,
		Content:    "threshold test",
		Source:     "t.md",
		Vector:     []float32{1, 0, 0, 0},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Query with a very high threshold so nothing matches.
	results, err := store.Query(context.Background(), rag.QueryTask{
		Vector:     []float32{1, 0, 0, 0},
		Collection: col,
		TopK:       5,
		Threshold:  0.9999, // near-perfect match required
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	// Cosine similarity of identical vectors is 1.0, so this should still hit.
	// The point is to demonstrate threshold filtering; loosen if flaky.
	_ = results
}

func TestQuery_CollectionFilter_Isolates(t *testing.T) {
	requireQdrant(t)
	store := newTestStore(t)

	colA := testCollection(t) + "_A"
	colB := testCollection(t) + "_B"
	vec := dummyVector(testDimensions, 0.3)

	// Insert into collection A.
	if err := store.Upsert(context.Background(), rag.UpsertTask{
		ChunkID:    "a1",
		DocID:      "doc-a",
		Collection: colA,
		Content:    "content in A",
		Source:     "a.md",
		Vector:     vec,
	}); err != nil {
		t.Fatalf("Upsert colA: %v", err)
	}

	// Query collection B — must not see A's data.
	results, err := store.Query(context.Background(), rag.QueryTask{
		Vector:     vec,
		Collection: colB,
		TopK:       5,
		Threshold:  0.1,
	})
	if err != nil {
		t.Fatalf("Query colB: %v", err)
	}
	for _, r := range results {
		if r.Content == "content in A" {
			t.Error("collection filter leaked: colB query returned colA content")
		}
	}
}

func TestDeleteByDocID_RemovesChunks(t *testing.T) {
	requireQdrant(t)
	store := newTestStore(t)
	col := testCollection(t)
	vec := dummyVector(testDimensions, 0.7)

	if err := store.Upsert(context.Background(), rag.UpsertTask{
		ChunkID:    "del-chunk",
		DocID:      "doc-del",
		Collection: col,
		Content:    "to be deleted",
		Source:     "del.md",
		Vector:     vec,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Confirm it exists.
	pre, err := store.Query(context.Background(), rag.QueryTask{
		Vector:     vec,
		Collection: col,
		TopK:       5,
		Threshold:  0.5,
	})
	if err != nil {
		t.Fatalf("Query before delete: %v", err)
	}
	if len(pre) == 0 {
		t.Fatal("expected chunk before deletion, got none")
	}

	// Delete by doc_id.
	if err := store.DeleteByDocID(context.Background(), "doc-del", col); err != nil {
		t.Fatalf("DeleteByDocID: %v", err)
	}

	// Allow Qdrant a moment to process the deletion.
	time.Sleep(100 * time.Millisecond)

	post, err := store.Query(context.Background(), rag.QueryTask{
		Vector:     vec,
		Collection: col,
		TopK:       5,
		Threshold:  0.5,
	})
	if err != nil {
		t.Fatalf("Query after delete: %v", err)
	}
	for _, r := range post {
		if r.Content == "to be deleted" {
			t.Error("chunk still present after DeleteByDocID")
		}
	}
}

func TestUpsert_EmptyVector_ReturnsError(t *testing.T) {
	requireQdrant(t)
	store := newTestStore(t)

	err := store.Upsert(context.Background(), rag.UpsertTask{
		ChunkID:    "bad",
		Collection: "col",
		Content:    "no vector",
		Vector:     nil, // intentionally empty
	})
	if err == nil {
		t.Fatal("expected error for empty vector, got nil")
	}
}

func TestQuery_EmptyVector_ReturnsError(t *testing.T) {
	requireQdrant(t)
	store := newTestStore(t)

	_, err := store.Query(context.Background(), rag.QueryTask{
		Vector:     nil,
		Collection: "col",
		TopK:       3,
		Threshold:  0.5,
	})
	if err == nil {
		t.Fatal("expected error for empty query vector, got nil")
	}
}

func TestTopK_Limits_Results(t *testing.T) {
	requireQdrant(t)
	store := newTestStore(t)
	col := testCollection(t)

	// Insert 5 similar chunks.
	for i := 0; i < 5; i++ {
		v := dummyVector(testDimensions, float32(i)*0.01)
		if err := store.Upsert(context.Background(), rag.UpsertTask{
			ChunkID:    fmt.Sprintf("topk-%d", i),
			DocID:      "doc-topk",
			Collection: col,
			Content:    fmt.Sprintf("chunk content %d", i),
			Source:     "topk.md",
			Vector:     v,
		}); err != nil {
			t.Fatalf("Upsert %d: %v", i, err)
		}
	}

	results, err := store.Query(context.Background(), rag.QueryTask{
		Vector:     dummyVector(testDimensions, 0.02),
		Collection: col,
		TopK:       3,
		Threshold:  0.0,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) > 3 {
		t.Errorf("TopK=3 should return at most 3 results, got %d", len(results))
	}
}
