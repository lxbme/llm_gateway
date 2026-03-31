package gateway

import (
	"encoding/json"
	"net/http"
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
