// Package client is a tiny HTTP client for talking to the sunny daemon.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	base string
	hc   *http.Client
}

func New(addr string) *Client {
	return &Client{
		base: "http://" + addr,
		hc:   &http.Client{Timeout: 5 * time.Second},
	}
}

type AgentSummary struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Model       string `json:"model"`
	Skills      int    `json:"skills"`
	Knowledge   int    `json:"knowledge"`
}

func (c *Client) ListAgents(ctx context.Context) ([]AgentSummary, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/agents", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET /agents: %d: %s", resp.StatusCode, string(body))
	}
	var out []AgentSummary
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
