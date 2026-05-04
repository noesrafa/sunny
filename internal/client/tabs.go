package client

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Tab is the wire shape of one open tab on a daemon. Mirrors
// internal/tabs.Tab; lives here so the TUI doesn't have to import
// the daemon package.
type Tab struct {
	ID        string    `json:"id"`
	AgentSlug string    `json:"agent_slug"`
	ConvID    string    `json:"conv_id"`
	Title     string    `json:"title,omitempty"`
	Cwd       string    `json:"cwd,omitempty"`
	OpenedAt  time.Time `json:"opened_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// OpenTabRequest is the body of POST /tabs. ConvID is optional —
// when empty the daemon spawns a fresh conversation under the
// agent and returns the tab pointing at it.
type OpenTabRequest struct {
	AgentSlug string `json:"agent_slug"`
	ConvID    string `json:"conv_id,omitempty"`
	Title     string `json:"title,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
}

// PatchTabRequest is the body of PATCH /tabs/{id}. nil pointers
// leave the field untouched (vs clearing it).
type PatchTabRequest struct {
	Title *string `json:"title,omitempty"`
	Cwd   *string `json:"cwd,omitempty"`
}

// ListTabs returns every tab open on this daemon.
func (c *Client) ListTabs(ctx context.Context) ([]Tab, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/tabs", nil)
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
		return nil, errorFromBody("GET /tabs", resp)
	}
	var out []Tab
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// OpenTab creates a new tab and returns it (with id + conv_id +
// timestamps populated). When ConvID is omitted in the request, a
// fresh conversation is spawned under the agent — convenient for
// the TUI's "new chat" flow.
func (c *Client) OpenTab(ctx context.Context, body OpenTabRequest) (*Tab, error) {
	resp, err := c.doJSON(ctx, http.MethodPost, "/tabs", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, errorFromBody("POST /tabs", resp)
	}
	var out Tab
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CloseTab removes a tab from the daemon. Idempotent — closing a
// missing tab returns nil. Does NOT delete the underlying
// conversation; the journal stays under
// ~/.sunny/agents/<slug>/conversations/<id>/.
func (c *Client) CloseTab(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+"/tabs/"+id, nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return errorFromBody("DELETE /tabs/"+id, resp)
	}
	return nil
}

// PatchTab updates a tab's mutable fields (title, cwd). Returns
// the freshly-stored tab.
func (c *Client) PatchTab(ctx context.Context, id string, body PatchTabRequest) (*Tab, error) {
	resp, err := c.doJSON(ctx, http.MethodPatch, "/tabs/"+id, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errorFromBody("PATCH /tabs/"+id, resp)
	}
	var out Tab
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}
