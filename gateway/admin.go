package gateway

import (
	"encoding/json"
	"net/http"

	"llm_gateway/rag"
)

func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	s.RegisterAdminRoutes(mux)
	return chain(mux, adminCheckMiddleware)
}

func (s *Server) RegisterAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /admin/create", s.handleRedisCreate)
	mux.HandleFunc("POST /admin/get", s.handleRedisGet)
	mux.HandleFunc("POST /admin/delete", s.handleRedisDelete)
	mux.HandleFunc("POST /admin/rag/ingest", s.handleRAGIngest)
	mux.HandleFunc("DELETE /admin/rag/doc", s.handleRAGDeleteDoc)
}

func (s *Server) handleRedisCreate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req map[string]interface{}
	if err := bindJSON(r, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to parse request"})
		return
	}

	alias, ok := req["alias"].(string)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid alias type"})
		return
	}

	token, err := s.services.Auth.Create(alias)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		logError("Fail to create auth token: %w", err)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Fail to create auth token"})
		return
	}

	w.WriteHeader(http.StatusOK)
	logDebug("Created token: %s, alias: %s", token, alias)
	_ = json.NewEncoder(w).Encode(map[string]string{"token": token, "alias": alias})
}

func (s *Server) handleRedisGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req map[string]interface{}
	if err := bindJSON(r, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to parse request"})
		return
	}

	token, ok := req["token"].(string)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid token type"})
		return
	}

	valid, alias, err := s.services.Auth.Get(token)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		logError("Fail to query token from auth service: %w", err)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Fail to query token"})
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"valide": valid, "token": token, "alias": alias})
}

func (s *Server) handleRedisDelete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req map[string]interface{}
	if err := bindJSON(r, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to parse request"})
		return
	}

	token, ok := req["token"].(string)
	if !ok || token == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid or missing token"})
		return
	}

	if err := s.services.Auth.Delete(token); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		logError("Fail to delete token from auth service: %s", err)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Fail to delete token"})
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"token": token, "status": "deleted"})
}

// handleRAGIngest accepts document chunks and ingests them into the RAG vector store.
//
// Request body:
//
//	{
//	  "collection": "team-a",
//	  "source": "docs/faq.md",
//	  "chunks": [
//	    {"content": "...", "chunk_index": 0, "total_chunks": 3},
//	    ...
//	  ]
//	}
func (s *Server) handleRAGIngest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if s.services.RAG == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "RAG service not configured"})
		return
	}

	var req struct {
		Collection string `json:"collection"`
		Source     string `json:"source"`
		Chunks     []struct {
			Content     string `json:"content"`
			ChunkIndex  int32  `json:"chunk_index"`
			TotalChunks int32  `json:"total_chunks"`
		} `json:"chunks"`
	}
	if err := bindJSON(r, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to parse request"})
		return
	}
	if req.Collection == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "collection is required"})
		return
	}
	if len(req.Chunks) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "chunks must not be empty"})
		return
	}

	ragChunks := make([]rag.Chunk, 0, len(req.Chunks))
	for _, c := range req.Chunks {
		ragChunks = append(ragChunks, rag.Chunk{
			Collection:  req.Collection,
			Content:     c.Content,
			Source:      req.Source,
			ChunkIndex:  c.ChunkIndex,
			TotalChunks: c.TotalChunks,
		})
	}

	docID, count, err := s.services.RAG.Ingest(r.Context(), ragChunks)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		logError("RAG ingest failed: %s", err)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Ingest failed"})
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"doc_id": docID, "ingested_count": count})
}

// handleRAGDeleteDoc deletes all chunks of a document from the RAG vector store.
//
// Request body: {"doc_id": "uuid", "collection": "team-a"}
func (s *Server) handleRAGDeleteDoc(w http.ResponseWriter, r *http.Request) {
	if s.services.RAG == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "RAG service not configured"})
		return
	}

	var req struct {
		DocID      string `json:"doc_id"`
		Collection string `json:"collection"`
	}
	if err := bindJSON(r, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Failed to parse request"})
		return
	}
	if req.DocID == "" || req.Collection == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "doc_id and collection are required"})
		return
	}

	if err := s.services.RAG.DeleteDoc(r.Context(), req.DocID, req.Collection); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		logError("RAG delete doc failed: %s", err)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Delete failed"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
