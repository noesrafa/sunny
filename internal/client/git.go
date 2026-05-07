package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/noesrafa/sunny/internal/git"
)

// GitStatus hits GET /git/status?cwd=<cwd>. Returns the active branch
// and per-bucket change counts. A non-repo / git-missing combo
// surfaces as a 200 with empty branch and zero counts — the daemon
// keeps the "uninteresting" case out of the error path.
func (c *Client) GitStatus(ctx context.Context, cwd string) (string, git.ChangeStats, error) {
	q := url.Values{}
	q.Set("cwd", cwd)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/git/status?"+q.Encode(), nil)
	if err != nil {
		return "", git.ChangeStats{}, err
	}
	c.auth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", git.ChangeStats{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", git.ChangeStats{}, fmt.Errorf("git status: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Branch  string          `json:"branch"`
		Changes git.ChangeStats `json:"changes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", git.ChangeStats{}, fmt.Errorf("git status decode: %w", err)
	}
	return out.Branch, out.Changes, nil
}

// GitFiles hits GET /git/files?cwd=<cwd>. Returns the changed-file
// list ready for the diff dialog's left pane.
func (c *Client) GitFiles(ctx context.Context, cwd string) ([]git.File, error) {
	q := url.Values{}
	q.Set("cwd", cwd)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/git/files?"+q.Encode(), nil)
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
		return nil, fmt.Errorf("git files: HTTP %d", resp.StatusCode)
	}
	var out []git.File
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("git files decode: %w", err)
	}
	return out, nil
}

// GitDiff hits GET /git/diff?cwd=<cwd>&path=<path>. Returns the raw
// diff body (no ANSI styling — TUIs colorize after the round-trip).
func (c *Client) GitDiff(ctx context.Context, cwd, path string) (string, error) {
	q := url.Values{}
	q.Set("cwd", cwd)
	q.Set("path", path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/git/diff?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	c.auth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("git diff: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("git diff decode: %w", err)
	}
	return out.Body, nil
}
