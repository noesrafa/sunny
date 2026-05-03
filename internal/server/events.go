package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/noesrafa/sunny/internal/events"
)

// streamEvents handles GET /events. It opens a Server-Sent-Events
// pipe, subscribes to the in-process Hub, and forwards every event
// as `event: <type>\ndata: <json>\n\n` until the client disconnects
// or the daemon shuts down.
//
// A 30-second heartbeat (an SSE comment line) keeps proxies and
// reverse-tunnels from closing the idle connection. Without it,
// nginx/cloudfront/tailscale-funnel will sometimes drop a connection
// that's been silent for too long.
func (s *server) streamEvents(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		http.Error(w, "events not configured", http.StatusServiceUnavailable)
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
	w.Header().Set("X-Accel-Buffering", "no") // tell nginx not to buffer

	// Send an initial hello so the client knows the stream is live.
	// Without this, the first heartbeat can take 30s, which feels
	// broken when debugging by curl.
	fmt.Fprintf(w, ": stream open\n\n")
	flusher.Flush()

	ch, cancel := s.hub.Subscribe()
	defer cancel()

	hb := time.NewTicker(30 * time.Second)
	defer hb.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-hb.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return // hub closed
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, payload)
			flusher.Flush()
		}
	}
}

// publish is a tiny helper so handler bodies stay readable. nil-safe
// for tests that construct a server without a hub.
func (s *server) publish(t events.Type, slug, convID string) {
	if s.hub == nil {
		return
	}
	s.hub.Publish(events.Event{Type: t, Slug: slug, ConvID: convID})
}

// publishProvider mirrors publish for secret events where Slug is
// not the right concept; provider name carries the same uniqueness
// role.
func (s *server) publishProvider(t events.Type, provider string) {
	if s.hub == nil {
		return
	}
	s.hub.Publish(events.Event{Type: t, Provider: provider})
}
