package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/noesrafa/sunny/internal/mesh"
	"github.com/noesrafa/sunny/internal/tsnet"
)

// FederationPeer is one entry in GET /federation/peers. It tells the
// TUI which other sunny daemons live on this daemon's tailnet and
// how to trust them — mirrors the classification the TUI used to do
// in cmd/sunny/tui.go before discovery moved server-side.
//
// Trust values:
//
//	"tailnet" — same tailscale UserID as us; AddTailnetPeer (no creds)
//	"mesh"    — different account but identity.mesh matches our key;
//	            AddMeshPeer with our shared key
//
// Daemons that don't fit either bucket are simply omitted, not
// included with trust="none". The TUI never has to ignore rows.
type FederationPeer struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Trust string `json:"trust"`
}

// federationCache caches the tailnet sweep so /federation/peers
// answers in microseconds when called on a tick. The 30s TTL is the
// sweet spot: short enough that a peer coming online surfaces in the
// header switcher within half a minute, long enough that the per-
// peer GET /sunny/identity round-trips don't run on every poll.
type federationCache struct {
	mu      sync.Mutex
	result  []FederationPeer
	expires time.Time
}

func newFederationCache() *federationCache {
	return &federationCache{}
}

const federationCacheTTL = 30 * time.Second

// Sweep returns the cached or freshly-computed federation list.
// Concurrent callers serialize on the mutex; the first to find an
// expired cache pays the sweep cost and shares the result with the
// rest.
func (fc *federationCache) Sweep(ctx context.Context, meshKey mesh.Key) []FederationPeer {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if time.Now().Before(fc.expires) && fc.result != nil {
		return fc.result
	}
	fc.result = sweepTailnet(ctx, meshKey)
	if fc.result == nil {
		fc.result = []FederationPeer{}
	}
	fc.expires = time.Now().Add(federationCacheTTL)
	return fc.result
}

// sweepTailnet walks the local tailscale status, GETs /sunny/identity
// on every online peer, and classifies each by tailscale UserID
// (same account → tailnet) or mesh fingerprint match (→ mesh). 5s
// hard budget mirrors what the old TUI-side discovery used. Sunny
// daemons live on the conventional 7777 port; daemons running on a
// different port aren't auto-discovered (they need a manual
// peers.yaml entry), same as before.
func sweepTailnet(ctx context.Context, meshKey mesh.Key) []FederationPeer {
	const port = "7777"
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	st, err := tsnet.FetchStatus()
	if err != nil {
		return nil
	}
	wantFP := ""
	if meshKey != "" {
		wantFP = meshKey.Fingerprint()
	}

	type result struct {
		name  string
		url   string
		trust string
	}
	resCh := make(chan result, len(st.Peers))

	launched := 0
	for _, p := range st.Peers {
		if !p.Online || p.IP == "" {
			continue
		}
		launched++
		go func(p tsnet.Peer) {
			base := "http://" + p.IP + ":" + port
			id, err := fetchIdentity(ctx, base)
			if err != nil {
				resCh <- result{name: peerNameFromHost(p.HostName), url: base}
				return
			}
			r := result{name: peerNameFromHost(p.HostName), url: base}
			switch {
			case p.UserID != 0 && p.UserID == st.Self.UserID:
				r.trust = "tailnet"
			case wantFP != "" && id.Mesh == wantFP:
				r.trust = "mesh"
			}
			resCh <- r
		}(p)
	}

	out := []FederationPeer{}
	for i := 0; i < launched; i++ {
		select {
		case r := <-resCh:
			if r.trust == "" {
				continue
			}
			out = append(out, FederationPeer{Name: r.name, URL: r.url, Trust: r.trust})
		case <-ctx.Done():
			return out
		}
	}
	return out
}

// peerNameFromHost mirrors the TUI helper that used to live in
// cmd/sunny/tui.go: drop the magicDNS suffix, lowercase. Kept here
// instead of imported so server doesn't depend on the cmd package.
func peerNameFromHost(h string) string {
	h = strings.ToLower(h)
	if i := strings.Index(h, "."); i >= 0 {
		h = h[:i]
	}
	if h == "" {
		return "remote"
	}
	return h
}

// peerIdentity is the subset of /sunny/identity we read for trust
// classification. Inlined here rather than importing client to keep
// server's dep graph free of the HTTP client package.
type peerIdentity struct {
	App  string `json:"app"`
	Mesh string `json:"mesh_fingerprint"`
}

func fetchIdentity(ctx context.Context, base string) (*peerIdentity, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/sunny/identity", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("identity: HTTP %d", resp.StatusCode)
	}
	var out peerIdentity
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.App != "sunny" {
		return nil, fmt.Errorf("not sunny: app=%q", out.App)
	}
	return &out, nil
}

// federationPeers handles GET /federation/peers. Returns whatever the
// daemon sees on its tailnet right now (or the last 30s cache,
// whichever is fresher). Empty array on tailscale-not-running — the
// TUI just never auto-adds peers in that case.
func (s *server) federationPeers(w http.ResponseWriter, r *http.Request) {
	if s.federation == nil {
		writeJSON(w, http.StatusOK, []FederationPeer{})
		return
	}
	out := s.federation.Sweep(r.Context(), s.meshKey)
	writeJSON(w, http.StatusOK, out)
}
