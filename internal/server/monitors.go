package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/noesrafa/sunny/internal/monitors"
)

// listMonitors returns every monitor YAML on disk plus its runtime
// state (running/last-fire/last-error).
//
// Path: GET /monitors
func (s *server) listMonitors(w http.ResponseWriter, _ *http.Request) {
	if s.scheduler == nil {
		http.Error(w, "monitors not configured", http.StatusServiceUnavailable)
		return
	}
	out := s.scheduler.List()
	if out == nil {
		out = []monitors.MonitorView{}
	}
	writeJSON(w, http.StatusOK, out)
}

// patchMonitorRequest is the body for the toggle endpoint. Only
// `enabled` is supported — every other field lives in the YAML and
// is owned by the agent.
type patchMonitorRequest struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// patchMonitor flips the enabled flag in the YAML. Idempotent and
// triggers an immediate scheduler rescan so the user sees the
// worker spawn / die without waiting for the next watchdog tick.
//
// Path: PATCH /monitors/{name}
func (s *server) patchMonitor(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		http.Error(w, "monitors not configured", http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("name")
	var req patchMonitorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Enabled == nil {
		http.Error(w, "enabled: required", http.StatusBadRequest)
		return
	}
	if err := s.scheduler.Toggle(name, *req.Enabled); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Return the post-toggle view so callers don't need a follow-up
	// GET to render the new state.
	for _, m := range s.scheduler.List() {
		if m.Name == name {
			writeJSON(w, http.StatusOK, m)
			return
		}
	}
	http.NotFound(w, r)
}

// getMonitorHistory tails the last N entries of a monitor's
// history.jsonl. Default tail is 100; ?tail=0 returns all (heavy
// for long-running monitors — caller should pick a bound).
//
// Path: GET /monitors/{name}/history
func (s *server) getMonitorHistory(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		http.Error(w, "monitors not configured", http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("name")
	tail := 100
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			tail = n
		}
	}
	entries, err := monitors.TailHistory(s.scheduler.HistoryPath(name), tail)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []monitors.HistoryEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}
