package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/noesrafa/sunny/internal/agent"
	"github.com/noesrafa/sunny/internal/events"
	"github.com/noesrafa/sunny/internal/store"
)

// createAgentRequest is the JSON body of POST /agents.
//
// The id is generated server-side; clients don't supply one. Only
// name + model are required; everything else is optional.
type createAgentRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Model       string `json:"model"`
	Effort      string `json:"effort"`
	Provider    string `json:"provider"`
	Prompt      string `json:"prompt"`
}

func (s *server) createAgent(w http.ResponseWriter, r *http.Request) {
	var req createAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	a, err := s.store.Create(agent.Config{
		Name:        req.Name,
		Description: req.Description,
		Model:       req.Model,
		Effort:      req.Effort,
		Provider:    req.Provider,
	}, req.Prompt)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.publish(events.AgentCreated, a.ID, "")
	writeJSON(w, http.StatusCreated, summarize(a))
}

// updateAgentRequest is the body of PATCH /agents/{id}. Any field
// omitted (left as nil) is preserved on the agent.
type updateAgentRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Model       *string `json:"model,omitempty"`
	Effort      *string `json:"effort,omitempty"`
	Provider    *string `json:"provider,omitempty"`
	Prompt      *string `json:"prompt,omitempty"`
}

func (s *server) updateAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updateAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	a, err := s.store.Update(id, store.AgentPatch{
		Name:        req.Name,
		Description: req.Description,
		Model:       req.Model,
		Effort:      req.Effort,
		Provider:    req.Provider,
		Prompt:      req.Prompt,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.publish(events.AgentUpdated, a.ID, "")
	writeJSON(w, http.StatusOK, summarize(a))
}

// deleteAgent archives the agent's directory under ~/.sunny/.archive/.
// Idempotent — already-missing id returns 204.
func (s *server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publish(events.AgentDeleted, id, "")
	w.WriteHeader(http.StatusNoContent)
}
