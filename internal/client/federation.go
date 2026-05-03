package client

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/noesrafa/sunny/internal/peers"
)

// Federation is the TUI's view of a roster of sunny daemons (one
// local, zero-or-more remote). Each peer gets one *Client; routing
// and fan-out live here so callers don't manage the map themselves.
//
// Concurrency: clients are only added at construction; reads are
// safe without a lock. The mutex protects future hot-reload of
// peers.yaml (not implemented yet).
type Federation struct {
	mu      sync.RWMutex
	clients map[string]*Client // peer name → client
	order   []string           // declaration order, local first
}

// NewFederationFromClient is a convenience for legacy code paths
// that already constructed a single-daemon Client and want to opt
// into Federation-shaped APIs without juggling a roster object.
func NewFederationFromClient(name string, c *Client) *Federation {
	return &Federation{
		clients: map[string]*Client{name: c},
		order:   []string{name},
	}
}

// NewFederation builds a federation from a peers.Roster. Local is
// always the first entry; remote peers follow in roster order
// (peers.Save sorts them alphabetically).
func NewFederation(r peers.Roster) *Federation {
	f := &Federation{
		clients: map[string]*Client{},
		order:   []string{},
	}
	add := func(p peers.Peer) {
		f.clients[p.Name] = NewFromBase(p.URL, p.Token)
		f.order = append(f.order, p.Name)
	}
	add(r.Local)
	for _, p := range r.Remote {
		add(p)
	}
	return f
}

// For returns the client for the named peer. Missing names return
// nil — callers should treat that as "host not in this federation".
// An empty name resolves to Local for ergonomic backward compat
// (legacy code paths that didn't carry a host).
func (f *Federation) For(name string) *Client {
	if name == "" {
		name = peers.LocalName
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.clients[name]
}

// Local is a shortcut for the implicit local peer.
func (f *Federation) Local() *Client { return f.For(peers.LocalName) }

// Names returns peer names in display order (local first).
func (f *Federation) Names() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]string, len(f.order))
	copy(out, f.order)
	return out
}

// FederatedAgent is one agent prefixed with the peer it lives on.
// The TUI uses this as the row in the agent picker; the Host field
// tells the routing layer which client to dispatch follow-up calls
// against.
type FederatedAgent struct {
	Host string
	AgentSummary
}

// FederatedEvent is a BusEvent tagged with the peer it came from.
// Lets the TUI route refresh decisions back to the right host
// (e.g. only refresh the agent picker when an event from `local`
// arrives if the picker is showing local agents).
type FederatedEvent struct {
	Host string
	BusEvent
}

// FederatedListResult bundles the fan-out outcome: agents flatten
// into one list, errors are kept per-peer so the caller can render
// "vps unreachable" without losing the peers that did respond.
type FederatedListResult struct {
	Agents []FederatedAgent
	Errors map[string]error // peer name → error; nil when peer succeeded
}

// ListAgents fans out GET /agents to every peer in parallel. A peer
// failing doesn't fail the whole call — its error lands in
// result.Errors and the rest of the federation still surfaces.
//
// Output is sorted by (host, slug) so the TUI gets stable row order
// without having to sort downstream.
func (f *Federation) ListAgents(ctx context.Context) FederatedListResult {
	f.mu.RLock()
	names := append([]string(nil), f.order...)
	f.mu.RUnlock()

	type peerOut struct {
		name   string
		agents []AgentSummary
		err    error
	}
	results := make(chan peerOut, len(names))
	var wg sync.WaitGroup
	for _, name := range names {
		c := f.For(name)
		if c == nil {
			continue
		}
		wg.Add(1)
		go func(name string, c *Client) {
			defer wg.Done()
			ag, err := c.ListAgents(ctx)
			results <- peerOut{name: name, agents: ag, err: err}
		}(name, c)
	}
	wg.Wait()
	close(results)

	out := FederatedListResult{Errors: map[string]error{}}
	for r := range results {
		if r.err != nil {
			out.Errors[r.name] = r.err
			continue
		}
		for _, a := range r.agents {
			out.Agents = append(out.Agents, FederatedAgent{Host: r.name, AgentSummary: a})
		}
	}
	sort.Slice(out.Agents, func(i, j int) bool {
		if out.Agents[i].Host != out.Agents[j].Host {
			return out.Agents[i].Host < out.Agents[j].Host
		}
		return out.Agents[i].Slug < out.Agents[j].Slug
	})
	return out
}

// SubscribeAll opens an SSE stream against every peer in the
// federation and returns a single multiplexed channel of
// FederatedEvent. Per-peer connection failures are auto-retried
// every reconnectDelay — a peer rebooting briefly stays in the
// federation, the consumer just pauses on its events.
//
// The returned channel closes when ctx is cancelled. Call sites
// should treat the channel as the lifetime of one TUI session.
func (f *Federation) SubscribeAll(ctx context.Context) <-chan FederatedEvent {
	out := make(chan FederatedEvent, 64)
	f.mu.RLock()
	names := append([]string(nil), f.order...)
	f.mu.RUnlock()

	var wg sync.WaitGroup
	for _, name := range names {
		c := f.For(name)
		if c == nil {
			continue
		}
		wg.Add(1)
		go func(name string, c *Client) {
			defer wg.Done()
			subscribePeerLoop(ctx, name, c, out)
		}(name, c)
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}

// subscribePeerLoop keeps trying to subscribe to one peer, forwarding
// every event it receives. On failure or stream end it sleeps
// reconnectDelay and tries again — the goal is "self-healing as long
// as ctx is alive."
func subscribePeerLoop(ctx context.Context, name string, c *Client, out chan<- FederatedEvent) {
	for {
		if ctx.Err() != nil {
			return
		}
		ch, err := c.SubscribeEvents(ctx)
		if err != nil {
			if !sleepCtx(ctx, reconnectDelay) {
				return
			}
			continue
		}
		for ev := range ch {
			select {
			case out <- FederatedEvent{Host: name, BusEvent: ev}:
			case <-ctx.Done():
				return
			}
		}
		// Stream closed — reconnect after a short pause unless ctx
		// is done.
		if !sleepCtx(ctx, reconnectDelay) {
			return
		}
	}
}

// sleepCtx waits for d unless ctx is cancelled first. Returns true
// when the wait completed naturally, false when ctx fired (caller
// should bail).
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
