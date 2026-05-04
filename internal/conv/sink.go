// Package conv glues the on-disk conversation journal to an in-memory
// per-conversation pub/sub bus. Every event written to a conversation's
// journal is also fanned out to live subscribers, so multiple TUI
// clients watching the same conversation see deltas as they happen
// (not just the one that initiated the turn).
//
// The Sink is the single write path the chat handler uses. The watch
// endpoint subscribes here and replays from the journal when a client
// asks for ?since=<seq>.
//
// Per-conversation hubs are created lazily on first touch and never
// reaped. The memory footprint is tiny (one mutex + a subscriber set
// per ever-touched conv); a future GC pass can drop hubs with zero
// subs and zero recent activity, but the simplicity wins for v1.
package conv

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/noesrafa/sunny/internal/conversation"
)

// Event is the public type subscribers receive. Re-exported from
// conversation so callers don't import both packages.
type Event = conversation.Event

// Sink writes one event to the journal AND publishes it to live
// subscribers in one call. It is the only thing in the daemon that
// should mutate a conversation after creation — keeping all writes
// behind one type means seq monotonicity is enforced by construction.
type Sink struct {
	store *conversation.Store
	log   *slog.Logger

	mu   sync.Mutex
	hubs map[string]*hub // key = slug + "/" + convID
}

type hub struct {
	mu   sync.Mutex
	seq  int64 // last assigned seq; next event gets seq+1
	subs map[*subscription]struct{}
}

type subscription struct {
	ch chan Event
}

// NewSink wraps an existing conversation.Store. log is used for
// "subscriber dropped event" warnings and may be nil.
func NewSink(store *conversation.Store, log *slog.Logger) *Sink {
	return &Sink{
		store: store,
		log:   log,
		hubs:  map[string]*hub{},
	}
}

// Append assigns the next seq for this conversation, persists the
// event to the journal, and publishes to all subscribers. The
// returned Event carries the assigned Seq so the caller can use it
// for ack / logging.
//
// On journal write failure the seq is still consumed (we don't
// rewind the counter — a gap is less harmful than a duplicate seq).
// The error propagates so the caller can decide whether to retry or
// fail the turn.
func (s *Sink) Append(slug, convID, kind string, payload any) (Event, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("conv.Sink: marshal payload: %w", err)
	}

	h := s.hubFor(slug, convID)
	h.mu.Lock()
	h.seq++
	seq := h.seq
	h.mu.Unlock()

	// Stamp At here so live subscribers see the same timestamp the
	// journal will store. Letting conversation.Store.Append fill it
	// in works for the on-disk journal but leaves the in-memory ev
	// we publish below with a zero time — watchers on the live tail
	// would then see At=0001-01-01.
	ev := Event{Seq: seq, Kind: kind, At: time.Now().UTC(), Payload: raw}
	if err := s.store.Append(slug, convID, ev); err != nil {
		// Still publish: subscribers see the event in real time even
		// if the journal write failed. Watchers won't be able to
		// resume past it via ?since=, but live viewers stay in sync.
		s.publish(h, ev)
		return ev, err
	}
	s.publish(h, ev)
	return ev, nil
}

// Subscribe attaches a new subscriber to a conversation. The returned
// channel receives every Append'd event that lands AFTER subscribe
// returns. The current seq is the last-assigned seq at subscribe
// time — callers use it to read the journal up to current and then
// continue tailing the channel without gaps or duplicates.
//
// The recipe for a resumable watcher:
//
//	ch, current, cancel := sink.Subscribe(slug, convID)
//	defer cancel()
//	for ev := range journal where since < ev.Seq && ev.Seq <= current {
//	    forward(ev)
//	}
//	for ev := range ch {
//	    if ev.Seq > current { forward(ev) }   // skip dups
//	}
//
// Always defer cancel — a leaked subscription holds a buffered chan
// and a slot in the hub forever.
func (s *Sink) Subscribe(slug, convID string) (<-chan Event, int64, func()) {
	h := s.hubFor(slug, convID)
	sub := &subscription{ch: make(chan Event, 256)}
	h.mu.Lock()
	current := h.seq
	h.subs[sub] = struct{}{}
	h.mu.Unlock()
	cancel := func() {
		h.mu.Lock()
		if _, ok := h.subs[sub]; ok {
			delete(h.subs, sub)
			close(sub.ch)
		}
		h.mu.Unlock()
	}
	return sub.ch, current, cancel
}

// HeadSeq returns the last-assigned seq for a conversation, or 0 if
// the conversation has no events yet (or has never been touched by
// this Sink).
func (s *Sink) HeadSeq(slug, convID string) int64 {
	h := s.hubFor(slug, convID)
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seq
}

// hubFor returns the hub for (slug, convID), creating it on first
// touch. First touch primes the seq counter from the on-disk journal
// so freshly-loaded conversations don't start at seq=1 and shadow
// existing events.
func (s *Sink) hubFor(slug, convID string) *hub {
	key := slug + "/" + convID
	s.mu.Lock()
	h, ok := s.hubs[key]
	if ok {
		s.mu.Unlock()
		return h
	}
	h = &hub{subs: map[*subscription]struct{}{}}
	s.hubs[key] = h
	s.mu.Unlock()

	// Prime the counter from the journal. Errors here aren't fatal —
	// a missing or unreadable journal just leaves seq at 0 and new
	// events start at 1. The caller will see the journal error on
	// the first Append/Get if it's a real problem.
	if _, events, err := s.store.Get(slug, convID); err == nil {
		var max int64
		for _, ev := range events {
			if ev.Seq > max {
				max = ev.Seq
			}
		}
		h.mu.Lock()
		if max > h.seq {
			h.seq = max
		}
		h.mu.Unlock()
	}
	return h
}

// publish fan-outs ev to every subscriber non-blockingly. Slow subs
// drop (and log) — never let one stuck client backpressure the
// publisher or every other viewer.
func (s *Sink) publish(h *hub, ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		select {
		case sub.ch <- ev:
		default:
			if s.log != nil {
				s.log.Warn("conv.Sink: subscriber buffer full, dropping event",
					"kind", ev.Kind, "seq", ev.Seq)
			}
		}
	}
}
