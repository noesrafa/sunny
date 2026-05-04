package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/noesrafa/sunny/internal/conversation"
	evts "github.com/noesrafa/sunny/internal/events"
	"github.com/noesrafa/sunny/internal/tabs"
)

// listTabs returns every open tab on this daemon. Order is the
// insertion order maintained by tabs.Store.
//
// Path: GET /tabs
func (s *server) listTabs(w http.ResponseWriter, _ *http.Request) {
	if s.tabs == nil {
		http.Error(w, "tabs not configured", http.StatusServiceUnavailable)
		return
	}
	out := s.tabs.List()
	if out == nil {
		out = []*tabs.Tab{}
	}
	writeJSON(w, http.StatusOK, out)
}

// openTabRequest is the body of POST /tabs.
//
// ConvID is optional — when empty, the daemon creates a new
// conversation under the agent and ties the tab to it. The TUI's
// "open chat with agent X" flow leaves ConvID empty; a future
// "join existing conversation" picker would set it.
type openTabRequest struct {
	AgentSlug string `json:"agent_slug"`
	ConvID    string `json:"conv_id,omitempty"`
	Title     string `json:"title,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
}

// openTab creates a new tab. If conv_id is omitted, the daemon also
// creates a fresh conversation so the TUI doesn't have to
// orchestrate two round-trips.
//
// Path: POST /tabs
func (s *server) openTab(w http.ResponseWriter, r *http.Request) {
	if s.tabs == nil {
		http.Error(w, "tabs not configured", http.StatusServiceUnavailable)
		return
	}
	var req openTabRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.AgentSlug == "" {
		http.Error(w, "agent_slug: required", http.StatusBadRequest)
		return
	}
	a, ok := s.store.Agent(req.AgentSlug)
	if !ok {
		http.NotFound(w, r)
		return
	}

	convID := req.ConvID
	title := req.Title
	if convID == "" {
		// Spawn a fresh conv so the tab has somewhere to write
		// from the very first turn.
		meta, err := s.conv.Create(req.AgentSlug, title, a.Config.Model)
		if err != nil {
			if errors.Is(err, conversation.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		convID = meta.ID
		if title == "" {
			title = meta.Title
		}
		s.publish(evts.ConvCreated, req.AgentSlug, convID)
	} else {
		// Verify the conv exists; refuse to open a tab pointing
		// at a non-existent journal.
		meta, _, err := s.conv.Get(req.AgentSlug, convID)
		if err != nil {
			if errors.Is(err, conversation.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if title == "" {
			title = meta.Title
		}
	}
	if title == "" {
		title = a.Config.Name
	}

	stored, err := s.tabs.Add(&tabs.Tab{
		AgentSlug: req.AgentSlug,
		ConvID:    convID,
		Title:     title,
		Cwd:       req.Cwd,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishTab(evts.TabOpened, stored)
	writeJSON(w, http.StatusCreated, stored)
}

// rebindTabConv creates a fresh conversation under the tab's agent
// and points the tab at it. Used by the TUI's "Nueva conversación"
// flow: the tab keeps its id (so other viewers don't see it churn),
// but the underlying journal is replaced with an empty one. The old
// conversation stays on disk under
// ~/.sunny/agents/<slug>/conversations/<old_id>/ — only the tab's
// pointer changes.
//
// Path: POST /tabs/{id}/conversation
func (s *server) rebindTabConv(w http.ResponseWriter, r *http.Request) {
	if s.tabs == nil {
		http.Error(w, "tabs not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	existing, err := s.tabs.Get(id)
	if err != nil {
		if errors.Is(err, tabs.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a, ok := s.store.Agent(existing.AgentSlug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	meta, err := s.conv.Create(existing.AgentSlug, existing.Title, a.Config.Model)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publish(evts.ConvCreated, existing.AgentSlug, meta.ID)
	updated, err := s.tabs.Update(id, func(t *tabs.Tab) {
		t.ConvID = meta.ID
	})
	if err != nil {
		if errors.Is(err, tabs.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishTab(evts.TabUpdated, updated)
	writeJSON(w, http.StatusOK, updated)
}

// closeTab removes a tab from the daemon. The underlying
// conversation is NOT deleted — the tab is just a "this is open in
// the UI" pointer. Idempotent.
//
// Path: DELETE /tabs/{id}
func (s *server) closeTab(w http.ResponseWriter, r *http.Request) {
	if s.tabs == nil {
		http.Error(w, "tabs not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	if err := s.tabs.Remove(id); err != nil {
		if errors.Is(err, tabs.ErrNotFound) {
			w.WriteHeader(http.StatusNoContent) // idempotent
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishTab(evts.TabClosed, &tabs.Tab{ID: id})
	w.WriteHeader(http.StatusNoContent)
}

// patchTabRequest carries the fields a client may update on an
// existing tab. nil pointers leave the field untouched (as opposed
// to clearing it). Title and Cwd are the only mutable fields today.
type patchTabRequest struct {
	Title *string `json:"title,omitempty"`
	Cwd   *string `json:"cwd,omitempty"`
}

// patchTab applies a partial update to a tab.
//
// Path: PATCH /tabs/{id}
func (s *server) patchTab(w http.ResponseWriter, r *http.Request) {
	if s.tabs == nil {
		http.Error(w, "tabs not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	var req patchTabRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	updated, err := s.tabs.Update(id, func(t *tabs.Tab) {
		if req.Title != nil {
			t.Title = *req.Title
		}
		if req.Cwd != nil {
			t.Cwd = *req.Cwd
		}
	})
	if err != nil {
		if errors.Is(err, tabs.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishTab(evts.TabUpdated, updated)
	writeJSON(w, http.StatusOK, updated)
}

// publishTab broadcasts a tab.* event on the global bus. Events
// carry the tab id, the agent slug, and the conv id so subscribers
// can update local UI without an extra GET /tabs round-trip.
func (s *server) publishTab(t evts.Type, tab *tabs.Tab) {
	if s.hub == nil || tab == nil {
		return
	}
	s.hub.Publish(evts.Event{
		Type:   t,
		Slug:   tab.AgentSlug,
		ConvID: tab.ConvID,
		TabID:  tab.ID,
	})
}
