package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/noesrafa/sunny/internal/conversation"
)

// watchConversation streams every event of one conversation as it
// happens, plus a backfill of everything since the client's last
// known seq.
//
// Path: GET /agents/{slug}/conversations/{id}/watch?since=<seq>
//
// Wire format: one SSE frame per event, no `event:` tag —
//
//	data: {"seq":42,"kind":"text_delta","at":"...","payload":{...}}
//
// Resumability is by seq: a client that disconnected at seq=10
// reconnects with ?since=10 and receives every event with seq > 10
// (no duplicates, no gaps). A fresh subscriber uses since=0 to get
// the full conversation history followed by live tailing.
//
// Race-free subscribe-then-replay protocol:
//
//  1. Subscribe to the per-conv hub. The Sink hands us back the
//     current head seq atomically — anything with that seq or lower
//     is already in the journal we're about to read.
//  2. Read the journal. Stream events with since < seq <= current.
//     The journal may have grown past current between (1) and (2)
//     — we filter those out and let the live tail deliver them.
//  3. Tail the channel. Skip events with seq <= current (already
//     sent from the journal scan); forward the rest.
//
// 30s heartbeat keeps proxies/tailnet-funnels from killing idle
// connections. The connection stays open until the client closes
// or the daemon shuts down — long-lived conversations naturally
// hold one of these per active viewer.
func (s *server) watchConversation(w http.ResponseWriter, r *http.Request) {
	if s.sink == nil {
		http.Error(w, "watch not configured", http.StatusServiceUnavailable)
		return
	}
	slug := r.PathValue("slug")
	convID := r.PathValue("id")
	if _, ok := s.store.Agent(slug); !ok {
		http.NotFound(w, r)
		return
	}
	// Existence check — Get returns ErrNotFound when the conv dir
	// is missing. We don't actually use the events list yet (we'll
	// re-fetch after subscribing) because we need the subscribe
	// race protection.
	if _, _, err := s.conv.Get(slug, convID); err != nil {
		if errors.Is(err, conversation.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	since, err := parseSince(r.URL.Query().Get("since"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Initial hello so curl-style debuggers see the stream is live
	// before the first heartbeat (30s) or first event.
	fmt.Fprintf(w, ": stream open\n\n")
	flusher.Flush()

	// (1) Subscribe FIRST. Anything appended after this returns goes
	//     into ch; anything appended before this returns has seq
	//     <= current.
	ch, current, cancel := s.sink.Subscribe(slug, convID)
	defer cancel()

	// (2) Replay the journal up to current.
	_, journal, err := s.conv.Get(slug, convID)
	if err == nil {
		for _, ev := range journal {
			if ev.Seq <= since {
				continue
			}
			if ev.Seq > current {
				// Past the snapshot point — let the live tail handle
				// these (it has them buffered).
				break
			}
			if err := writeWatchEvent(w, flusher, ev); err != nil {
				return // client gone
			}
		}
	}

	// (3) Tail live events, skipping any seq we already streamed
	//     during the journal replay.
	hb := time.NewTicker(30 * time.Second)
	defer hb.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-hb.C:
			if _, err := fmt.Fprintf(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return // hub closed (daemon shutdown)
			}
			if ev.Seq <= current {
				continue // already streamed via journal replay
			}
			if err := writeWatchEvent(w, flusher, ev); err != nil {
				return
			}
		}
	}
}

// writeWatchEvent serializes one journal event as a single SSE
// data frame. Returns an error when the underlying writer fails so
// the caller can abort the stream.
func writeWatchEvent(w http.ResponseWriter, f http.Flusher, ev conversation.Event) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return nil // skip un-marshalable events; should never happen
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	f.Flush()
	return nil
}

// parseSince validates the ?since= query param. Empty / missing
// returns 0 (= "give me everything"). Negative or non-numeric
// returns an error so the client knows it sent garbage.
func parseSince(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("since: not an integer")
	}
	if n < 0 {
		return 0, fmt.Errorf("since: must be >= 0")
	}
	return n, nil
}
