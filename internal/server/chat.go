package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/noesrafa/sunny/internal/provider"
)

// turnRequest is the body of POST /agents/{slug}/turn.
//
// The client sends the full conversation transcript on every turn —
// stateless on the server side. Skills, knowledge, and the system prompt
// come from the agent's on-disk definition; the client only carries the
// user/assistant exchange.
type turnRequest struct {
	Messages []provider.Message `json:"messages"`
}

// postTurn streams one assistant turn back as Server-Sent Events.
//
// Event shapes:
//
//	event: text_delta
//	data: {"text": "Hi"}
//
//	event: thinking_delta
//	data: {"text": "Let me think about that…"}
//
//	event: done
//	data: {"stop_reason": "end_turn", "usage": {...}}
//
//	event: error
//	data: {"message": "..."}
//
// The connection closes immediately after `done` or `error`.
func (s *server) postTurn(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		http.Error(w, "engine not configured (set ANTHROPIC_API_KEY and restart)", http.StatusServiceUnavailable)
		return
	}

	slug := r.PathValue("slug")
	a, ok := s.store.Agent(slug)
	if !ok {
		http.NotFound(w, r)
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

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	events, err := s.engine.Turn(r.Context(), a, req.Messages)
	if err != nil {
		writeSSEError(w, flusher, err)
		return
	}

	for ev := range events {
		switch v := ev.(type) {
		case provider.TextDelta:
			writeSSE(w, flusher, "text_delta", map[string]string{"text": v.Text})
		case provider.ThinkingDelta:
			writeSSE(w, flusher, "thinking_delta", map[string]string{"text": v.Text})
		case provider.Done:
			writeSSE(w, flusher, "done", map[string]any{
				"stop_reason": v.StopReason,
				"usage": map[string]int64{
					"input_tokens":           v.InputTokens,
					"output_tokens":          v.OutputTokens,
					"cache_creation_tokens":  v.CacheCreationTokens,
					"cache_read_tokens":      v.CacheReadTokens,
				},
			})
		case provider.Error:
			writeSSEError(w, flusher, v.Err)
		}
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
