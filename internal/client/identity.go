package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Identity mirrors server.IdentityResponse on the wire. Used by
// mesh discovery to ask "which mesh is this daemon in?" before
// trusting it.
type Identity struct {
	App        string `json:"app"`
	Version    string `json:"version"`
	Mesh       string `json:"mesh_fingerprint,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
}

// FetchIdentity hits GET <base>/sunny/identity (no auth needed).
// Returns the identity response or an error if the URL doesn't
// answer or doesn't look like a sunny daemon.
//
// We use a fresh http.Client so the call doesn't reuse this
// Client's transport — discovery probes shouldn't hijack
// connection pools earmarked for chat.
func FetchIdentity(ctx context.Context, base string) (*Identity, error) {
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
	var out Identity
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("identity: decode: %w", err)
	}
	if out.App != "sunny" {
		return nil, fmt.Errorf("identity: not a sunny daemon (app=%q)", out.App)
	}
	return &out, nil
}
