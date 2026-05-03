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
	base  string
	token string
	hc    *http.Client
}

// New constructs a daemon HTTP client. token is sent in
// `Authorization: Bearer <token>` on every request — empty token
// skips the header (only useful when talking to an unauth'd dev
// daemon).
func New(addr, token string) *Client {
	return &Client{
		base:  "http://" + addr,
		token: token,
		// No global timeout — turns can be long. The caller's context
		// owns lifetime.
		hc: &http.Client{},
	}
}

// auth attaches the bearer header when one is configured. Called by
// every request method before c.hc.Do.
func (c *Client) auth(req *http.Request) {
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
