package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/noesrafa/sunny/internal/conversation"
)

// listConversations responds with metas (newest first) for an agent.
func (s *server) listConversations(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if _, ok := s.store.Agent(slug); !ok {
		http.NotFound(w, r)
		return
	}
	metas, err := s.conv.List(slug)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if metas == nil {
		metas = []*conversation.Meta{}
	}
	writeJSON(w, http.StatusOK, metas)
}

// createConversation allocates a new conversation under an agent.
//
// Body (all optional): {"title": "...", "model": "..."}
//
// Response: the freshly written meta.json.
func (s *server) createConversation(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	a, ok := s.store.Agent(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Title string `json:"title"`
		Model string `json:"model"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	model := body.Model
	if model == "" {
		model = a.Config.Model
	}
	meta, err := s.conv.Create(slug, body.Title, model)
	if err != nil {
		if errors.Is(err, conversation.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, meta)
}

// getConversation returns the meta + the full event journal for a conv.
func (s *server) getConversation(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	id := r.PathValue("id")
	if _, ok := s.store.Agent(slug); !ok {
		http.NotFound(w, r)
		return
	}
	meta, events, err := s.conv.Get(slug, id)
	if err != nil {
		if errors.Is(err, conversation.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if events == nil {
		events = []conversation.Event{}
	}
	writeJSON(w, http.StatusOK, struct {
		Meta   *conversation.Meta   `json:"meta"`
		Events []conversation.Event `json:"events"`
	}{meta, events})
}

// deleteConversation moves a conversation to ~/.sunny/.trash/. Idempotent.
func (s *server) deleteConversation(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	id := r.PathValue("id")
	if _, ok := s.store.Agent(slug); !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.conv.Delete(slug, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
