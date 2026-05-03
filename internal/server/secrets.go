package server

import (
	"encoding/json"
	"net/http"

	"github.com/noesrafa/sunny/internal/secrets"
)

// listSecrets surfaces *which* providers and fields are configured —
// values are NEVER returned. The TUI/CLI use this to render
// "✓ configured" badges without ever holding plaintext.
func (s *server) listSecrets(w http.ResponseWriter, _ *http.Request) {
	if s.secrets == nil {
		writeJSON(w, http.StatusOK, []secrets.ProviderInfo{})
		return
	}
	writeJSON(w, http.StatusOK, s.secrets.List())
}

// putSecrets replaces all fields for a provider with the request body.
//
// Body: {"api_key":"…","base_url":"…"}
//
// Empty fields in the body are dropped. Fields previously set but not
// in the body are removed (replace semantics, not merge — keeps the
// behavior obvious).
//
// Triggers an engine rebuild so the new key takes effect on the next
// turn without a daemon restart.
func (s *server) putSecrets(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	var fields map[string]string
	if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.secrets.SetProvider(provider, fields); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.rebuildEngine != nil {
		s.rebuildEngine()
	}
	writeJSON(w, http.StatusOK, secrets.ProviderInfo{
		Provider: provider,
		Fields:   listKeys(fields),
	})
}

// deleteSecrets removes a provider's section entirely. Idempotent.
// Engine is rebuilt so the now-unavailable provider stops being
// auto-detected.
func (s *server) deleteSecrets(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if err := s.secrets.Delete(provider); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.rebuildEngine != nil {
		s.rebuildEngine()
	}
	w.WriteHeader(http.StatusNoContent)
}

func listKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		if v != "" {
			out = append(out, k)
		}
	}
	return out
}
