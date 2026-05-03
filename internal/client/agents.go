package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// AgentSummary is one row of GET /agents.
type AgentSummary struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Model       string `json:"model"`
	Skills      int    `json:"skills"`
	Knowledge   int    `json:"knowledge"`
}

// AgentDetail is GET /agents/{slug}: full config + skill + knowledge metadata.
type AgentDetail struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Model       string `json:"model"`
	Prompt      string `json:"prompt,omitempty"`
	Skills      []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"skills"`
	Knowledge []struct {
		Name string `json:"name"`
	} `json:"knowledge"`
}

// AgentCreate is the body of POST /agents.
type AgentCreate struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Model       string `json:"model"`
	Prompt      string `json:"prompt,omitempty"`
}

// AgentPatch is the body of PATCH /agents/{slug}. nil pointers leave
// the corresponding field untouched.
type AgentPatch struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Model       *string `json:"model,omitempty"`
	Prompt      *string `json:"prompt,omitempty"`
}

// ListAgents returns summaries for every agent on the daemon.
func (c *Client) ListAgents(ctx context.Context) ([]AgentSummary, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/agents", nil)
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
		return nil, errorFromBody("GET /agents", resp)
	}
	var out []AgentSummary
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetAgent fetches the full detail for one agent.
func (c *Client) GetAgent(ctx context.Context, slug string) (*AgentDetail, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/agents/"+slug, nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("agent %q not found", slug)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errorFromBody("GET /agents/"+slug, resp)
	}
	var out AgentDetail
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateAgent scaffolds a new agent on the daemon.
func (c *Client) CreateAgent(ctx context.Context, body AgentCreate) (*AgentSummary, error) {
	resp, err := c.doJSON(ctx, http.MethodPost, "/agents", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, errorFromBody("POST /agents", resp)
	}
	var out AgentSummary
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateAgent patches an existing agent. nil fields are left untouched.
func (c *Client) UpdateAgent(ctx context.Context, slug string, patch AgentPatch) (*AgentSummary, error) {
	resp, err := c.doJSON(ctx, http.MethodPatch, "/agents/"+slug, patch)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errorFromBody("PATCH /agents/"+slug, resp)
	}
	var out AgentSummary
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteAgent moves the agent's directory to ~/.sunny/.archive/. Idempotent.
func (c *Client) DeleteAgent(ctx context.Context, slug string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+"/agents/"+slug, nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return errorFromBody("DELETE /agents/"+slug, resp)
	}
	return nil
}

// errorFromBody is a small helper used by every request method to turn
// a non-success HTTP response into a single-line error preserving the
// daemon's body content. Avoids re-implementing this 12 times.
func errorFromBody(label string, resp *http.Response) error {
	raw, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("%s: %d: %s", label, resp.StatusCode, strings.TrimSpace(string(raw)))
}
