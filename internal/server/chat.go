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

// turnRequest is the body of POST /agents/{id}/conversations/{conv_id}/turns.
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
	current map[string]*activeTurn // key = agentID + "/" + convID
}

type activeTurn struct {
	cancel context.CancelFunc
}

func newActiveTurnsRegistry() *activeTurnsRegistry {
	return &activeTurnsRegistry{current: map[string]*activeTurn{}}
}

// claim tries to register a new turn for (agentID, convID). Returns the
// long-lived turn context, a release func, and ok=true on success.
// On contention returns ok=false — caller should respond 409.
//
// The release func MUST be called in a defer at the end of the turn:
// it removes the entry from the registry AND cancels the context so
// any goroutines hung on it unwind cleanly.
func (r *activeTurnsRegistry) claim(agentID, convID string) (context.Context, func(), bool) {
	key := agentID + "/" + convID
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

// cancel triggers the registered cancel func for (agentID, convID), if
// one is registered. Returns true when a turn was found and cancelled.
func (r *activeTurnsRegistry) cancel(agentID, convID string) bool {
	key := agentID + "/" + convID
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
	AgentID string `json:"agent_id"`
	ConvID  string `json:"conv_id"`
}

func (r *activeTurnsRegistry) snapshot() []TurnRef {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]TurnRef, 0, len(r.current))
	for key := range r.current {
		parts := strings.SplitN(key, "/", 2)
		if len(parts) == 2 {
			out = append(out, TurnRef{AgentID: parts[0], ConvID: parts[1]})
		}
	}
	return out
}

// postTurns enqueues a new turn for processing and returns 202
// immediately. Streaming of deltas/results happens entirely through
// the per-conversation watch endpoint (GET /watch); this handler
// never writes SSE.
//
// Path: POST /agents/{id}/conversations/{conv_id}/turns
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

	agentID := r.PathValue("id")
	convID := r.PathValue("conv_id")
	// Pick up any skill / knowledge files the agent wrote during a
	// previous turn. Reload failures are non-fatal — fall back to the
	// cached state and let the turn proceed.
	if err := s.store.Reload(agentID); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Warn("reload agent before turn", "agent_id", agentID, "err", err)
	}
	a, ok := s.store.Agent(agentID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	meta, _, err := s.conv.Get(agentID, convID)
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
	turnCtx, release, ok := s.activeTurns.claim(agentID, convID)
	if !ok {
		http.Error(w, "another turn is already running on this conversation", http.StatusConflict)
		return
	}

	// Journal the user turn synchronously so a watcher that joins
	// before the engine spits its first delta still sees the prompt.
	userEv, err := s.sink.Append(agentID, convID, "user", map[string]string{"text": last.Content})
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
// Path: DELETE /agents/{id}/conversations/{conv_id}/turn
func (s *server) deleteTurn(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	convID := r.PathValue("conv_id")
	if _, ok := s.store.Agent(agentID); !ok {
		http.NotFound(w, r)
		return
	}
	s.activeTurns.cancel(agentID, convID)
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

	agentID := agent.ID
	convID := meta.ID

	s.publish(evts.TurnStarted, agentID, convID)

	events, err := eng.Turn(ctx, agent, req.Messages, engine.TurnOptions{
		ProviderState: meta.ProviderState,
		Cwd:           req.Cwd,
	})
	if err != nil {
		s.sink.Append(agentID, convID, "error", map[string]string{"message": err.Error()})
		return
	}

	for ev := range events {
		switch v := ev.(type) {
		case provider.TextDelta:
			s.sink.Append(agentID, convID, "text_delta", map[string]string{"text": v.Text})
		case provider.ThinkingDelta:
			s.sink.Append(agentID, convID, "thinking_delta", map[string]string{"text": v.Text})
		case provider.ToolUse:
			s.sink.Append(agentID, convID, "tool_use", map[string]string{
				"id":    v.ID,
				"name":  v.Name,
				"input": v.Input,
			})
		case provider.ToolResult:
			s.sink.Append(agentID, convID, "tool_result", map[string]any{
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
			s.sink.Append(agentID, convID, "done", payload)
			s.finalizeTurn(agentID, convID, v)
			s.publish(evts.ConvTurnAppended, agentID, convID)
			s.publish(evts.TurnDone, agentID, convID)
		case provider.Error:
			// Distinguish "user cancelled" from "real error". The
			// provider drivers surface ctx cancellation as
			// Error{Err: ctx.Err()}; reclassify so the journal
			// reflects intent and the UI can render differently.
			if errors.Is(v.Err, context.Canceled) || ctx.Err() != nil {
				s.sink.Append(agentID, convID, "cancelled", map[string]string{"reason": "client cancelled"})
				s.publish(evts.TurnCancelled, agentID, convID)
			} else {
				s.sink.Append(agentID, convID, "error", map[string]string{"message": v.Err.Error()})
			}
		}
	}
}

// finalizeTurn updates meta.json after a successful turn. Best-effort —
// failures are logged. The journal already has the canonical record.
func (s *server) finalizeTurn(agentID, convID string, done provider.Done) {
	err := s.conv.UpdateMeta(agentID, convID, func(m *conversation.Meta) {
		m.MsgCount += 2 // user + assistant
		m.TotalCost += done.CostUSD
		if done.ProviderState != "" {
			m.ProviderState = done.ProviderState
		}
	})
	if err != nil {
		s.log.Warn("update meta", "err", err, "agent_id", agentID, "conv", convID)
	}
}
