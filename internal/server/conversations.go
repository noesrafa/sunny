package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/noesrafa/sunny/internal/conversation"
	evts "github.com/noesrafa/sunny/internal/events"
)

// listConversations responds with metas (newest first) for an agent.
func (s *server) listConversations(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.store.Agent(agentID); !ok {
		http.NotFound(w, r)
		return
	}
	metas, err := s.conv.List(agentID)
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
// Body (all optional): {"title": "...", "model": "...", "cwd": "..."}
//
// Response: the freshly written meta.json.
func (s *server) createConversation(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	a, ok := s.store.Agent(agentID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Title string `json:"title"`
		Model string `json:"model"`
		Cwd   string `json:"cwd"`
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
	meta, err := s.conv.Create(agentID, body.Title, model, body.Cwd)
	if err != nil {
		if errors.Is(err, conversation.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publish(evts.ConvCreated, agentID, meta.ID)
	writeJSON(w, http.StatusCreated, meta)
}

// getConversation returns the meta + a window of events from the
// journal.
//
// Query params (all optional):
//   - limit=N        cap the slice to N events.
//   - until_seq=X    only events with seq < X (lazy-load older history).
//   - since_seq=Y    only events with seq > Y (forward catch-up; the
//                    /watch SSE endpoint covers the streaming case,
//                    this is for one-shot polling).
//
// With no params the response is the full journal (back-compat).
// With `limit` alone the response is the LATEST `limit` events —
// pair with `until_seq` to page backwards through long transcripts.
//
// total_events is the size of the unfiltered journal so the client
// knows whether more history exists past the returned slice.
func (s *server) getConversation(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	convID := r.PathValue("conv_id")
	if _, ok := s.store.Agent(agentID); !ok {
		http.NotFound(w, r)
		return
	}
	meta, events, err := s.conv.Get(agentID, convID)
	if err != nil {
		if errors.Is(err, conversation.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	totalEvents := len(events)
	events = filterEvents(events,
		parseSeq(r, "since_seq"),
		parseSeq(r, "until_seq"),
		parsePositive(r, "limit"),
	)
	if events == nil {
		events = []conversation.Event{}
	}
	writeJSON(w, http.StatusOK, struct {
		Meta        *conversation.Meta   `json:"meta"`
		Events      []conversation.Event `json:"events"`
		TotalEvents int                  `json:"total_events"`
	}{meta, events, totalEvents})
}

// filterEvents applies (since_seq, until_seq, limit) to a journal
// slice. Events arrive monotonically by seq; we keep that ordering and
// always return the LATEST `limit` events when limit shrinks the
// window — the typical client wants "most recent" by default.
func filterEvents(events []conversation.Event, since, until int64, limit int) []conversation.Event {
	if since <= 0 && until <= 0 && limit <= 0 {
		return events
	}
	out := events
	if since > 0 || until > 0 {
		filtered := make([]conversation.Event, 0, len(events))
		for _, ev := range events {
			if since > 0 && ev.Seq <= since {
				continue
			}
			if until > 0 && ev.Seq >= until {
				continue
			}
			filtered = append(filtered, ev)
		}
		out = filtered
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// parseSeq pulls a non-negative int64 from a query param. Returns 0
// for missing or malformed values so callers can treat them as
// "filter off".
func parseSeq(r *http.Request, key string) int64 {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// parsePositive pulls a positive int from a query param. Returns 0
// for missing/malformed/<=0 so callers can treat them as "no limit".
func parsePositive(r *http.Request, key string) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// deleteConversation moves a conversation to ~/.sunny/.trash/. Idempotent.
func (s *server) deleteConversation(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	convID := r.PathValue("conv_id")
	if _, ok := s.store.Agent(agentID); !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.conv.Delete(agentID, convID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publish(evts.ConvDeleted, agentID, convID)
	w.WriteHeader(http.StatusNoContent)
}
