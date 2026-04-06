package qdrant_test

import (
	"testing"

	ragQdrant "llm_gateway/rag/qdrant"
)

// Config tests do not need a live Qdrant instance.

func TestLoadConfigFromEnv_Defaults(t *testing.T) {
	for _, k := range []string{
		"QDRANT_HOST", "QDRANT_PORT", "QDRANT_COLLECTION_NAME",
		"RAG_SIMILARITY_THRESHOLD", "RAG_DEFAULT_TOP_K",
	} {
		t.Setenv(k, "")
	}

	cfg, err := ragQdrant.LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}
	if cfg.Host != "localhost" {
		t.Errorf("Host default: got %q, want %q", cfg.Host, "localhost")
	}
	if cfg.Port != 6334 {
		t.Errorf("Port default: got %d, want 6334", cfg.Port)
	}
	if cfg.CollectionName != "llm_rag_documents" {
		t.Errorf("CollectionName default: got %q, want %q", cfg.CollectionName, "llm_rag_documents")
	}
	if cfg.SimilarityThreshold != 0.6 {
		t.Errorf("SimilarityThreshold default: got %f, want 0.6", cfg.SimilarityThreshold)
	}
	if cfg.DefaultTopK != 3 {
		t.Errorf("DefaultTopK default: got %d, want 3", cfg.DefaultTopK)
	}
}

func TestLoadConfigFromEnv_CustomValues(t *testing.T) {
	t.Setenv("QDRANT_HOST", "qdrant.internal")
	t.Setenv("QDRANT_PORT", "6335")
	t.Setenv("QDRANT_COLLECTION_NAME", "my_rag_store")
	t.Setenv("RAG_SIMILARITY_THRESHOLD", "0.75")
	t.Setenv("RAG_DEFAULT_TOP_K", "5")

	cfg, err := ragQdrant.LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}
	if cfg.Host != "qdrant.internal" {
		t.Errorf("Host: got %q, want %q", cfg.Host, "qdrant.internal")
	}
	if cfg.Port != 6335 {
		t.Errorf("Port: got %d, want 6335", cfg.Port)
	}
	if cfg.CollectionName != "my_rag_store" {
		t.Errorf("CollectionName: got %q, want %q", cfg.CollectionName, "my_rag_store")
	}
	if cfg.SimilarityThreshold != 0.75 {
		t.Errorf("SimilarityThreshold: got %f, want 0.75", cfg.SimilarityThreshold)
	}
	if cfg.DefaultTopK != 5 {
		t.Errorf("DefaultTopK: got %d, want 5", cfg.DefaultTopK)
	}
}

func TestLoadConfigFromEnv_InvalidPort(t *testing.T) {
	t.Setenv("QDRANT_PORT", "not-a-number")
	_, err := ragQdrant.LoadConfigFromEnv()
	if err == nil {
		t.Fatal("expected error for invalid QDRANT_PORT, got nil")
	}
}

func TestLoadConfigFromEnv_InvalidThreshold(t *testing.T) {
	t.Setenv("QDRANT_PORT", "")
	t.Setenv("RAG_SIMILARITY_THRESHOLD", "not-a-float")
	_, err := ragQdrant.LoadConfigFromEnv()
	if err == nil {
		t.Fatal("expected error for invalid RAG_SIMILARITY_THRESHOLD, got nil")
	}
}

func TestLoadConfigFromEnv_InvalidTopK(t *testing.T) {
	t.Setenv("QDRANT_PORT", "")
	t.Setenv("RAG_SIMILARITY_THRESHOLD", "")
	t.Setenv("RAG_DEFAULT_TOP_K", "not-a-number")
	_, err := ragQdrant.LoadConfigFromEnv()
	if err == nil {
		t.Fatal("expected error for invalid RAG_DEFAULT_TOP_K, got nil")
	}
}
