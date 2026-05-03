// Package client is the TUI's HTTP client to the sunny daemon.
//
// The daemon's surface is split across four files in this package,
// matching the server's groupings:
//
//   - agents.go         — list, create, edit, archive (delete)
//   - conversations.go  — list, create, get journal, archive
//   - secrets.go        — list, put, delete (no values ever returned)
//   - stream.go         — Turn (SSE chat), Stream pump
//
// The Stream is cancelable via its context — closing it interrupts
// the in-flight request all the way down to the provider.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
)

// Client is the per-process daemon client. Cheap to construct (no
// connection pool tuning); concurrent use is fine — net/http handles
// the connection management.
type Client struct {
	base    string
	token   string
	meshKey string // when set, sent as X-Sunny-Mesh in lieu of bearer
	hc      *http.Client
}

// New constructs a daemon HTTP client from an "host:port" address
// (the local daemon shape). token is sent in
// `Authorization: Bearer <token>` on every request — empty token
// skips the header (only useful when talking to an unauth'd dev
// daemon).
func New(addr, token string) *Client {
	return NewFromBase("http://"+addr, token)
}

// NewFromBase constructs a client from a fully-qualified base URL
// (scheme://host:port). Used for federated peers whose URL lives in
// peers.yaml and may use a tailnet IP, hostname, etc.
func NewFromBase(base, token string) *Client {
	return &Client{
		base:  base,
		token: token,
		// No global timeout — turns can be long. The caller's context
		// owns lifetime.
		hc: &http.Client{},
	}
}

// NewFromBaseMesh is for peers reachable via the tailnet mesh: the
// client carries the shared mesh key in X-Sunny-Mesh on every
// request (instead of a bearer token). Bearer header is omitted —
// the daemon's mesh middleware accepts the request based on
// (tailnet IP + matching key).
func NewFromBaseMesh(base, meshKey string) *Client {
	return &Client{
		base:    base,
		meshKey: meshKey,
		hc:      &http.Client{},
	}
}

// NewFromBaseTailnet is the zero-config path: no bearer, no mesh
// key. The daemon trusts the request because the source IP is on
// the same tailnet AND belongs to the same tailscale account
// (TailnetIdentityAuth middleware). Used by the TUI for peers
// auto-discovered through tailscale identity.
func NewFromBaseTailnet(base string) *Client {
	return &Client{
		base: base,
		hc:   &http.Client{},
	}
}

// Base returns the daemon URL this client targets. Useful for
// rendering "you're talking to X" hints in the UI.
func (c *Client) Base() string { return c.base }

// auth attaches the bearer header when one is configured. When
// the client was constructed via NewFromBaseMesh, sends the shared
// mesh key in X-Sunny-Mesh instead — the daemon's middleware
// accepts that route when the source IP is on the tailnet.
func (c *Client) auth(req *http.Request) {
	if c.meshKey != "" {
		req.Header.Set("X-Sunny-Mesh", c.meshKey)
		return
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// doJSON marshals body, builds the request with the right headers and
// auth, and dispatches. The caller owns the response — defer Close
// and inspect StatusCode. Used by every JSON-bodied write request
// (POST, PUT, PATCH); GET / DELETE without a body call http directly
// since they're a single line each.
func (c *Client) doJSON(ctx context.Context, method, path string, body any) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.auth(req)
	return c.hc.Do(req)
}
