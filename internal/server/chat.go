package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/noesrafa/sunny/internal/conversation"
	"github.com/noesrafa/sunny/internal/engine"
	evts "github.com/noesrafa/sunny/internal/events"
	"github.com/noesrafa/sunny/internal/provider"
	"github.com/noesrafa/sunny/internal/store"
)

// turnRequest is the body of POST /agents/{slug}/conversations/{id}/turns.
//
// The client sends the full transcript on every turn — stateless on the
// server side. Skills, knowledge, and the system prompt come from the
// agent's on-disk definition. ProviderState (claude-code session id for
// --resume) is tracked in the conversation's meta.json.
type turnRequest struct {
	Messages []provider.Message `json:"messages"`
	Cwd      string             `json:"cwd,omitempty"`
}

// activeTurnsRegistry tracks the in-flight turn (at most one) per
// conversation. POST /turns refuses with 409 when a turn is already
// running on the same conv; DELETE /turn looks up the cancel func
// here and triggers it.
//
// Per-conversation serialization (rather than a global mutex) lets
// independent conversations make progress in parallel — the model is
// "one model worker per chat", which matches user expectations.
type activeTurnsRegistry struct {
	mu      sync.Mutex
	current map[string]*activeTurn // key = slug + "/" + convID
}

type activeTurn struct {
	cancel context.CancelFunc
}

func newActiveTurnsRegistry() *activeTurnsRegistry {
	return &activeTurnsRegistry{current: map[string]*activeTurn{}}
}

// claim tries to register a new turn for (slug, convID). Returns the
// long-lived turn context, a release func, and ok=true on success.
// On contention returns ok=false — caller should respond 409.
//
// The release func MUST be called in a defer at the end of the turn:
// it removes the entry from the registry AND cancels the context so
// any goroutines hung on it unwind cleanly.
func (r *activeTurnsRegistry) claim(slug, convID string) (context.Context, func(), bool) {
	key := slug + "/" + convID
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, busy := r.current[key]; busy {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	at := &activeTurn{cancel: cancel}
	r.current[key] = at
	release := func() {
		r.mu.Lock()
		// Identity check guards against the race where a brand-new
		// turn replaced this entry between our defer firing and the
		// lock — we must only delete OUR own entry.
		if cur, ok := r.current[key]; ok && cur == at {
			delete(r.current, key)
		}
		r.mu.Unlock()
		cancel()
	}
	return ctx, release, true
}

// cancel triggers the registered cancel func for (slug, convID), if
// one is registered. Returns true when a turn was found and cancelled.
func (r *activeTurnsRegistry) cancel(slug, convID string) bool {
	key := slug + "/" + convID
	r.mu.Lock()
	at, ok := r.current[key]
	r.mu.Unlock()
	if !ok {
		return false
	}
	at.cancel()
	return true
}

// TurnRef identifies one in-flight turn.
type TurnRef struct {
	Slug   string `json:"slug"`
	ConvID string `json:"conv_id"`
}

func (r *activeTurnsRegistry) snapshot() []TurnRef {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]TurnRef, 0, len(r.current))
	for key := range r.current {
		parts := strings.SplitN(key, "/", 2)
		if len(parts) == 2 {
			out = append(out, TurnRef{Slug: parts[0], ConvID: parts[1]})
		}
	}
	return out
}

// postTurns enqueues a new turn for processing and returns 202
// immediately. Streaming of deltas/results happens entirely through
// the per-conversation watch endpoint (GET /watch); this handler
// never writes SSE.
//
// Path: POST /agents/{slug}/conversations/{id}/turns
//
// Body: {"messages": [...], "cwd": "..."}
//
// Responses:
//   - 202 {"conv_id": "..."} — turn accepted, watch the conv for events
//   - 400                    — malformed body
//   - 404                    — agent or conversation missing
//   - 409                    — another turn is already running on this conv
//   - 503                    — no provider configured
func (s *server) postTurns(w http.ResponseWriter, r *http.Request) {
	var eng *engine.Engine
	if s.engine != nil {
		eng = s.engine.Load()
	}
	if eng == nil || !eng.HasProviders() {
		http.Error(w, "engine not configured — add a provider key via /secrets or restart with one in env", http.StatusServiceUnavailable)
		return
	}
	if s.sink == nil {
		http.Error(w, "sink not configured", http.StatusServiceUnavailable)
		return
	}

	slug := r.PathValue("slug")
	convID := r.PathValue("id")
	a, ok := s.store.Agent(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	meta, _, err := s.conv.Get(slug, convID)
	if err != nil {
		if errors.Is(err, conversation.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var req turnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		http.Error(w, "messages: required", http.StatusBadRequest)
		return
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "user" {
		http.Error(w, "messages: last entry must have role=user", http.StatusBadRequest)
		return
	}

	// Reserve the per-conv slot. Two clients hitting POST against the
	// same conv in close succession see the second one fail with 409
	// — the TUI shouldn't allow this, but the daemon enforces.
	turnCtx, release, ok := s.activeTurns.claim(slug, convID)
	if !ok {
		http.Error(w, "another turn is already running on this conversation", http.StatusConflict)
		return
	}

	// Journal the user turn synchronously so a watcher that joins
	// before the engine spits its first delta still sees the prompt.
	userEv, err := s.sink.Append(slug, convID, "user", map[string]string{"text": last.Content})
	if err != nil {
		release()
		http.Error(w, "journal user turn: "+err.Error(), http.StatusInternalServerError)
		return
	}

	go s.runTurn(turnCtx, release, a, eng, req, meta)

	// Returning the user_seq lets the sender's TUI dedup the
	// self-echo when the same event lands via its watch stream
	// (the sender already rendered the message optimistically).
	writeJSON(w, http.StatusAccepted, map[string]any{
		"conv_id":  convID,
		"user_seq": userEv.Seq,
	})
}

// deleteTurn cancels the in-flight turn on a conversation, if any.
// Idempotent: 204 either way (the caller doesn't need to know
// whether there was something to cancel).
//
// Path: DELETE /agents/{slug}/conversations/{id}/turn
func (s *server) deleteTurn(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	convID := r.PathValue("id")
	if _, ok := s.store.Agent(slug); !ok {
		http.NotFound(w, r)
		return
	}
	s.activeTurns.cancel(slug, convID)
	w.WriteHeader(http.StatusNoContent)
}

// runTurn drives engine.Turn to completion, mirroring every event
// into the Sink. Errors become `error` events; cancellations (via
// DELETE /turn) become `cancelled`. Always calls release at the end
// so the next POST against this conv can claim the slot.
func (s *server) runTurn(
	ctx context.Context,
	release func(),
	agent *store.Agent,
	eng *engine.Engine,
	req turnRequest,
	meta *conversation.Meta,
) {
	defer release()

	slug := agent.Slug
	convID := meta.ID

	events, err := eng.Turn(ctx, agent, req.Messages, engine.TurnOptions{
		ProviderState: meta.ProviderState,
		Cwd:           req.Cwd,
	})
	if err != nil {
		s.sink.Append(slug, convID, "error", map[string]string{"message": err.Error()})
		return
	}

	for ev := range events {
		switch v := ev.(type) {
		case provider.TextDelta:
			s.sink.Append(slug, convID, "text_delta", map[string]string{"text": v.Text})
		case provider.ThinkingDelta:
			s.sink.Append(slug, convID, "thinking_delta", map[string]string{"text": v.Text})
		case provider.ToolUse:
			s.sink.Append(slug, convID, "tool_use", map[string]string{
				"id":    v.ID,
				"name":  v.Name,
				"input": v.Input,
			})
		case provider.ToolResult:
			s.sink.Append(slug, convID, "tool_result", map[string]any{
				"tool_use_id": v.ToolUseID,
				"content":     v.Content,
				"is_error":    v.IsError,
			})
		case provider.Done:
			payload := map[string]any{
				"stop_reason": v.StopReason,
				"cost_usd":    v.CostUSD,
				"usage": map[string]int64{
					"input_tokens":          v.InputTokens,
					"output_tokens":         v.OutputTokens,
					"cache_creation_tokens": v.CacheCreationTokens,
					"cache_read_tokens":     v.CacheReadTokens,
				},
			}
			s.sink.Append(slug, convID, "done", payload)
			s.finalizeTurn(slug, convID, v)
			s.publish(evts.ConvTurnAppended, slug, convID)
		case provider.Error:
			// Distinguish "user cancelled" from "real error". The
			// provider drivers surface ctx cancellation as
			// Error{Err: ctx.Err()}; reclassify so the journal
			// reflects intent and the UI can render differently.
			if errors.Is(v.Err, context.Canceled) || ctx.Err() != nil {
				s.sink.Append(slug, convID, "cancelled", map[string]string{"reason": "client cancelled"})
			} else {
				s.sink.Append(slug, convID, "error", map[string]string{"message": v.Err.Error()})
			}
		}
	}
}

// finalizeTurn updates meta.json after a successful turn. Best-effort —
// failures are logged. The journal already has the canonical record.
func (s *server) finalizeTurn(slug, convID string, done provider.Done) {
	err := s.conv.UpdateMeta(slug, convID, func(m *conversation.Meta) {
		m.MsgCount += 2 // user + assistant
		m.TotalCost += done.CostUSD
		if done.ProviderState != "" {
			m.ProviderState = done.ProviderState
		}
	})
	if err != nil {
		s.log.Warn("update meta", "err", err, "slug", slug, "conv", convID)
	}
}
