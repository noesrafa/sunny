package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// FsListItem mirrors the daemon's fsListItem shape.
type FsListItem struct {
	Name string `json:"name"`
}

// FsListResponse is the shape of GET /fs/list. Parent is empty when
// the listed path is a filesystem root.
type FsListResponse struct {
	Path    string       `json:"path"`
	Parent  string       `json:"parent,omitempty"`
	Entries []FsListItem `json:"entries"`
}

// ListDir returns the immediate subdirectories at path on the daemon's
// filesystem. Empty path → daemon's home dir. Used by the new-session
// dialog when the target peer isn't local: the dir picker has to walk
// the remote's tree, not ours.
func (c *Client) ListDir(ctx context.Context, path string) (*FsListResponse, error) {
	u := c.base + "/fs/list"
	if path != "" {
		u += "?" + url.Values{"path": []string{path}}.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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
		return nil, errorFromBody("GET /fs/list", resp)
	}
	var out FsListResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}
