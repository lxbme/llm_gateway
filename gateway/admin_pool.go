package gateway

import (
	"encoding/json"
	"net/http"

	"llm_gateway/completion"
)

// handleCompletionStats returns a JSON snapshot of the completion-service upstream pool.
// Backed by completion.StatsProvider (typically the gRPC client talking to one replica;
// stats are scoped to that replica only — see docs/gateway_working_mechanism.md).
func (s *Server) handleCompletionStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if s.services.CompletionStats == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "completion stats not available"})
		return
	}

	snapshots, err := s.services.CompletionStats.PoolStats(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		logError("PoolStats failed: %s", err)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "PoolStats failed"})
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"endpoints": snapshots})
}

func (s *Server) adminPoolAvailable(w http.ResponseWriter) bool {
	if s.services.CompletionAdmin == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "completion admin not available"})
		return false
	}
	return true
}

func writeAdminError(w http.ResponseWriter, code int, err error) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func writeAdminOK(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// GET /admin/completion/endpoints
func (s *Server) handleListCompletionEndpoints(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.adminPoolAvailable(w) {
		return
	}
	views, err := s.services.CompletionAdmin.ListEndpoints(r.Context())
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, err)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"endpoints": views})
}

// POST /admin/completion/endpoint  -- body: EndpointSpec
func (s *Server) handleAddCompletionEndpoint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.adminPoolAvailable(w) {
		return
	}
	var spec completion.EndpointSpec
	if err := bindJSON(r, &spec); err != nil {
		writeAdminError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.services.CompletionAdmin.AddEndpoint(r.Context(), spec); err != nil {
		writeAdminError(w, http.StatusBadRequest, err)
		return
	}
	writeAdminOK(w)
}

// DELETE /admin/completion/endpoint  -- body: {"name":"..."}
func (s *Server) handleRemoveCompletionEndpoint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.adminPoolAvailable(w) {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := bindJSON(r, &body); err != nil || body.Name == "" {
		writeAdminError(w, http.StatusBadRequest, errBadJSON("name required"))
		return
	}
	if err := s.services.CompletionAdmin.RemoveEndpoint(r.Context(), body.Name); err != nil {
		writeAdminError(w, http.StatusNotFound, err)
		return
	}
	writeAdminOK(w)
}

// POST /admin/completion/endpoint/weight  -- body: {"name":"...","weight":N}
func (s *Server) handleReweightCompletionEndpoint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.adminPoolAvailable(w) {
		return
	}
	var body struct {
		Name   string `json:"name"`
		Weight int    `json:"weight"`
	}
	if err := bindJSON(r, &body); err != nil || body.Name == "" {
		writeAdminError(w, http.StatusBadRequest, errBadJSON("name required"))
		return
	}
	if err := s.services.CompletionAdmin.Reweight(r.Context(), body.Name, body.Weight); err != nil {
		writeAdminError(w, http.StatusBadRequest, err)
		return
	}
	writeAdminOK(w)
}

// POST /admin/completion/endpoint/enabled  -- body: {"name":"...","enabled":true|false}
func (s *Server) handleSetCompletionEndpointEnabled(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.adminPoolAvailable(w) {
		return
	}
	var body struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := bindJSON(r, &body); err != nil || body.Name == "" {
		writeAdminError(w, http.StatusBadRequest, errBadJSON("name required"))
		return
	}
	if err := s.services.CompletionAdmin.SetEnabled(r.Context(), body.Name, body.Enabled); err != nil {
		writeAdminError(w, http.StatusNotFound, err)
		return
	}
	writeAdminOK(w)
}

// POST /admin/completion/breaker/reset  -- body: {"name":"..."}
func (s *Server) handleResetCompletionBreaker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.adminPoolAvailable(w) {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := bindJSON(r, &body); err != nil || body.Name == "" {
		writeAdminError(w, http.StatusBadRequest, errBadJSON("name required"))
		return
	}
	if err := s.services.CompletionAdmin.ResetBreaker(r.Context(), body.Name); err != nil {
		writeAdminError(w, http.StatusFailedDependency, err)
		return
	}
	writeAdminOK(w)
}

type errBadJSON string

func (e errBadJSON) Error() string { return string(e) }
