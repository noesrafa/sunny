package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/noesrafa/sunny/internal/conversation"
	"github.com/noesrafa/sunny/internal/engine"
	"github.com/noesrafa/sunny/internal/provider"
)

// turnRequest is the body of POST /agents/{slug}/conversations/{id}/turn.
//
// The client sends the full transcript on every turn — stateless on the
// server side. Skills, knowledge, and the system prompt come from the
// agent's on-disk definition. ProviderState (claude-code session id for
// --resume) is now tracked in the conversation's meta.json, not here.
type turnRequest struct {
	Messages []provider.Message `json:"messages"`
	Cwd      string             `json:"cwd,omitempty"`
}

// postTurn streams one assistant turn back as Server-Sent Events while
// also journaling every event to the conversation's events.jsonl. On
// terminal events (done / error) the conversation's meta.json is
// updated with msg_count, total_cost, and the new provider_state.
//
// Event shapes (SSE):
//
//	event: text_delta       data: {"text": "..."}
//	event: thinking_delta   data: {"text": "..."}
//	event: tool_use         data: {"id":"...","name":"...","input":"..."}
//	event: tool_result      data: {"tool_use_id":"...","content":"...","is_error":bool}
//	event: done             data: {"stop_reason":"...","cost_usd":..., "usage":{...}}
//	event: error            data: {"message":"..."}
//
// The connection closes immediately after `done` or `error`. If the
// client drops mid-stream, a synthetic `cancelled` event is appended to
// the journal (not sent over SSE — the connection is already gone).
func (s *server) postTurn(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		http.Error(w, "engine not configured (set ANTHROPIC_API_KEY and restart)", http.StatusServiceUnavailable)
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

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Journal the user turn before we start streaming. If the daemon
	// crashes mid-turn, the user's input still survives.
	s.journal(slug, convID, "user", map[string]string{"text": last.Content})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	events, err := s.engine.Turn(r.Context(), a, req.Messages, engine.TurnOptions{
		ProviderState: meta.ProviderState,
		Cwd:           req.Cwd,
	})
	if err != nil {
		s.journal(slug, convID, "error", map[string]string{"message": err.Error()})
		writeSSEError(w, flusher, err)
		return
	}

	terminated := false
	for ev := range events {
		switch v := ev.(type) {
		case provider.TextDelta:
			payload := map[string]string{"text": v.Text}
			writeSSE(w, flusher, "text_delta", payload)
			s.journal(slug, convID, "text_delta", payload)
		case provider.ThinkingDelta:
			payload := map[string]string{"text": v.Text}
			writeSSE(w, flusher, "thinking_delta", payload)
			s.journal(slug, convID, "thinking_delta", payload)
		case provider.ToolUse:
			payload := map[string]string{"id": v.ID, "name": v.Name, "input": v.Input}
			writeSSE(w, flusher, "tool_use", payload)
			s.journal(slug, convID, "tool_use", payload)
		case provider.ToolResult:
			payload := map[string]any{
				"tool_use_id": v.ToolUseID,
				"content":     v.Content,
				"is_error":    v.IsError,
			}
			writeSSE(w, flusher, "tool_result", payload)
			s.journal(slug, convID, "tool_result", payload)
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
			writeSSE(w, flusher, "done", payload)
			s.journal(slug, convID, "done", payload)
			s.finalizeTurn(slug, convID, v)
			terminated = true
		case provider.Error:
			s.journal(slug, convID, "error", map[string]string{"message": v.Err.Error()})
			writeSSEError(w, flusher, v.Err)
			terminated = true
		}
	}

	// Stream ended without a terminal event → caller hung up before the
	// turn completed (or the engine closed the channel mid-flight). Mark
	// it cancelled so reload from disk reflects reality.
	if !terminated {
		reason := "stream closed"
		if err := r.Context().Err(); err != nil && errors.Is(err, context.Canceled) {
			reason = "client disconnected"
		}
		s.journal(slug, convID, "cancelled", map[string]string{"reason": reason})
	}
}

// journal appends one event to the conversation's events.jsonl. Logged
// (not propagated) on failure — losing a journal entry shouldn't take
// down a turn that's already streaming to the client.
func (s *server) journal(slug, convID, kind string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		s.log.Warn("journal marshal", "err", err, "kind", kind)
		return
	}
	if err := s.conv.Append(slug, convID, conversation.Event{Kind: kind, Payload: data}); err != nil {
		s.log.Warn("journal append", "err", err, "slug", slug, "conv", convID, "kind", kind)
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

func writeSSE(w http.ResponseWriter, f http.Flusher, event string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
	f.Flush()
}

func writeSSEError(w http.ResponseWriter, f http.Flusher, err error) {
	writeSSE(w, f, "error", map[string]string{"message": err.Error()})
}
