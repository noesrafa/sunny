package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/noesrafa/sunny/internal/agent"
	"github.com/noesrafa/sunny/internal/store"
)

// createAgent allocates a new agent on disk + in the in-memory store.
//
// Body: {"slug","name","description","model","prompt"}
//   slug:        required; [a-z0-9][a-z0-9-]*
//   name, model: required
//   description, prompt: optional
type createAgentRequest struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Model       string `json:"model"`
	Prompt      string `json:"prompt"`
}

func (s *server) createAgent(w http.ResponseWriter, r *http.Request) {
	var req createAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	a, err := s.store.Create(req.Slug, agent.Config{
		Name:        req.Name,
		Description: req.Description,
		Model:       req.Model,
	}, req.Prompt)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, summarize(a))
}

// updateAgent applies a partial patch to an existing agent. Any field
// omitted from the JSON body is left unchanged.
type updateAgentRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Model       *string `json:"model,omitempty"`
	Prompt      *string `json:"prompt,omitempty"`
}

func (s *server) updateAgent(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	var req updateAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	a, err := s.store.Update(slug, store.AgentPatch{
		Name:        req.Name,
		Description: req.Description,
		Model:       req.Model,
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
	writeJSON(w, http.StatusOK, summarize(a))
}

// deleteAgent moves the agent's directory to ~/.sunny/.trash/.
// Idempotent — already-missing slug returns 204.
func (s *server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if err := s.store.Delete(slug); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
