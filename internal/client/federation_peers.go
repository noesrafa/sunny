package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// FederationPeer is one entry in GET /federation/peers — a sunny
// daemon the local daemon's tailnet sweep classified as trustable.
type FederationPeer struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Trust string `json:"trust"` // "tailnet" | "mesh"
}

// FetchFederationPeers hits GET /federation/peers on this client's
// daemon. The TUI calls this on a tick (typically against the local
// daemon) so peers that come online after boot surface in the
// header switcher without restarting.
//
// An empty list is the natural answer when tailscale isn't running
// — callers should treat the empty slice as "no auto-discovery
// available right now", not an error.
func (c *Client) FetchFederationPeers(ctx context.Context) ([]FederationPeer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/federation/peers", nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("federation peers: HTTP %d", resp.StatusCode)
	}
	var out []FederationPeer
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("federation peers decode: %w", err)
	}
	return out, nil
}
