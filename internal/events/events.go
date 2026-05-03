// Package events is the in-process pub/sub bus the daemon uses to
// notify subscribers (mainly the SSE handler at GET /events) about
// state changes. The TUI on another machine consumes those over the
// wire so its picker refreshes when an agent is created remotely,
// without polling.
//
// Design:
//   - Hub.Publish is non-blocking. A subscriber whose channel buffer
//     is full simply misses the event (logged once). We never let a
//     slow client backpressure the publisher — that would couple the
//     daemon's mutation latency to the slowest connected viewer.
//   - Subscribe returns the channel and a cancel func. Always defer
//     the cancel: leaking a sub leaks a goroutine inside the Hub.
//   - Events are values, not pointers. The struct is small (string
//     fields only); copying lets fan-out be allocation-light and
//     subscribers can mutate their copy without affecting peers.
package events

import (
	"log/slog"
	"sync"
	"sync/atomic"
)

// Type is the discriminator for an Event. Strings make the wire
// payload stable across versions and easy to grep for in handlers.
type Type string

const (
	AgentCreated      Type = "agent.created"
	AgentUpdated      Type = "agent.updated"
	AgentDeleted      Type = "agent.deleted"
	ConvCreated       Type = "conversation.created"
	ConvDeleted       Type = "conversation.deleted"
	ConvTurnAppended  Type = "conversation.turn"
	SecretsChanged    Type = "secrets.changed"
)

// Event is one bus message. Slug + ConvID + Provider are union-typed
// per Type — empty when not applicable. We deliberately avoid an
// `any` payload field so the wire shape stays predictable.
type Event struct {
	Type     Type   `json:"type"`
	Slug     string `json:"slug,omitempty"`
	ConvID   string `json:"conv_id,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// Hub is the singleton bus. The daemon constructs one in serve.go
// and hands it to anything that needs to Publish (agents handler,
// conversations handler, secrets handler).
type Hub struct {
	mu     sync.RWMutex
	subs   map[*subscription]struct{}
	log    *slog.Logger
	closed atomic.Bool
}

type subscription struct {
	ch chan Event
}

// New returns a Hub. log is used to record "subscriber dropped event
// (buffer full)" — without a logger those misses are silent and
// hard to debug. nil log silently no-ops the warning.
func New(log *slog.Logger) *Hub {
	return &Hub{
		subs: map[*subscription]struct{}{},
		log:  log,
	}
}

// Subscribe returns an event channel plus a cancel func. The buffer
// is sized so a typical TUI client (one event every few seconds at
// most) never sees drops; a chatty broadcast would queue briefly
// during a render frame and drain.
//
// Always defer the cancel — the Hub keeps a reference to your channel
// until you do, which means every dropped subscription leaks one
// goroutine in the consumer plus a slot in the subs map.
func (h *Hub) Subscribe() (<-chan Event, func()) {
	if h.closed.Load() {
		ch := make(chan Event)
		close(ch)
		return ch, func() {}
	}
	s := &subscription{ch: make(chan Event, 64)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	cancel := func() {
		h.mu.Lock()
		if _, ok := h.subs[s]; ok {
			delete(h.subs, s)
			close(s.ch)
		}
		h.mu.Unlock()
	}
	return s.ch, cancel
}

// Publish broadcasts ev to every current subscriber. Non-blocking
// per subscriber: if its channel buffer is full we drop (and log)
// rather than wait. This is the right tradeoff for an interactive
// system — better to miss one render than to make every other
// subscriber wait on the slowest one.
func (h *Hub) Publish(ev Event) {
	if h.closed.Load() {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.subs {
		select {
		case s.ch <- ev:
		default:
			if h.log != nil {
				h.log.Warn("events: subscriber buffer full, dropping event",
					"type", ev.Type, "slug", ev.Slug)
			}
		}
	}
}

// Close drains all subscriptions. After Close, Subscribe returns a
// pre-closed channel and Publish becomes a no-op. Call from server
// shutdown so SSE goroutines unwind cleanly.
func (h *Hub) Close() {
	h.closed.Store(true)
	h.mu.Lock()
	for s := range h.subs {
		delete(h.subs, s)
		close(s.ch)
	}
	h.mu.Unlock()
}

// SubCount is for tests + diagnostics ("how many viewers are
// connected?"). Snapshots under read lock; do not use in a hot loop.
func (h *Hub) SubCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}
