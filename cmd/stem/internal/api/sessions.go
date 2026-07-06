package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/historydb"
	"github.com/opentendril/core/cmd/stem/internal/session"
)

// SessionsHandler exposes the unified SessionManager over REST so the OS of
// OT frontend can create, inspect, and configure Tendril sessions.
type SessionsHandler struct {
	manager *session.Manager
	history *historydb.Store
}

// NewSessionsHandler creates the sessions REST surface. history may be nil
// when SQLite logging is disabled.
func NewSessionsHandler(manager *session.Manager, history *historydb.Store) *SessionsHandler {
	return &SessionsHandler{manager: manager, history: history}
}

// Register mounts the session routes onto the mux, wrapping each handler
// with the provided auth middleware.
func (h *SessionsHandler) Register(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	if auth == nil {
		auth = func(next http.HandlerFunc) http.HandlerFunc { return next }
	}

	mux.HandleFunc("POST /v1/sessions", auth(h.create))
	mux.HandleFunc("GET /v1/sessions", auth(h.list))
	mux.HandleFunc("GET /v1/sessions/{sessionId}", auth(h.get))
	mux.HandleFunc("PATCH /v1/sessions/{sessionId}", auth(h.updatePreferences))
	mux.HandleFunc("DELETE /v1/sessions/{sessionId}", auth(h.remove))
	mux.HandleFunc("GET /v1/sessions/{sessionId}/history", auth(h.messages))
	mux.HandleFunc("GET /v1/sessions/{sessionId}/events", auth(h.events))
	mux.HandleFunc("GET /v1/sessions/{sessionId}/sprout-runs", auth(h.sproutRuns))
}

type createSessionRequest struct {
	Origin      string              `json:"origin,omitempty"`
	Preferences session.Preferences `json:"preferences"`
}

func (h *SessionsHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if req.Origin == "" {
		req.Origin = session.OriginREST
	}

	sess, err := h.manager.Sprout(r.Context(), req.Origin, req.Preferences)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

func (h *SessionsHandler) list(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sessions": h.manager.List(),
	})
}

func (h *SessionsHandler) get(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.manager.Get(r.PathValue("sessionId"))
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

type updatePreferencesRequest struct {
	Preferences session.Preferences `json:"preferences"`
}

func (h *SessionsHandler) updatePreferences(w http.ResponseWriter, r *http.Request) {
	var req updatePreferencesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	sess, err := h.manager.UpdatePreferences(r.Context(), r.PathValue("sessionId"), req.Preferences)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (h *SessionsHandler) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.manager.Prune(r.Context(), r.PathValue("sessionId")); err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *SessionsHandler) messages(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionId")
	if _, ok := h.manager.Get(sessionID); !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	messages, err := h.manager.History(r.Context(), sessionID, queryLimit(r, 50))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sessionId": sessionID,
		"messages":  messages,
	})
}

func (h *SessionsHandler) events(w http.ResponseWriter, r *http.Request) {
	if h.history == nil {
		http.Error(w, "persistent history is disabled (OPENTENDRIL_DB_LOGGING=false)", http.StatusNotImplemented)
		return
	}

	sessionID := r.PathValue("sessionId")
	records, err := h.history.LoadEvents(r.Context(), sessionID, queryLimit(r, 100))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sessionId": sessionID,
		"events":    records,
	})
}

func (h *SessionsHandler) sproutRuns(w http.ResponseWriter, r *http.Request) {
	if h.history == nil {
		http.Error(w, "persistent history is disabled (OPENTENDRIL_DB_LOGGING=false)", http.StatusNotImplemented)
		return
	}

	sessionID := r.PathValue("sessionId")
	runs, err := h.history.LoadSproutRuns(r.Context(), sessionID, queryLimit(r, 50))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sessionId":  sessionID,
		"sproutRuns": runs,
	})
}

func queryLimit(r *http.Request, fallback int) int {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return fallback
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return fallback
	}
	return limit
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
