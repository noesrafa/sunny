package client

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// Monitor mirrors monitors.MonitorView. Lives here so the TUI
// doesn't have to import the daemon package.
type Monitor struct {
	Name     string    `json:"name"`
	Enabled  bool      `json:"enabled"`
	Interval string    `json:"interval"`
	Source   string    `json:"source"`
	Running  bool      `json:"running"`
	LastFire time.Time `json:"last_fire,omitempty"`
	LastErr  string    `json:"last_err,omitempty"`
}

// MonitorHistoryEntry mirrors monitors.HistoryEntry.
type MonitorHistoryEntry struct {
	Ts      time.Time              `json:"ts"`
	Rule    string                 `json:"rule"`
	Item    map[string]any         `json:"item"`
	Actions []MonitorHistoryAction `json:"actions"`
}

type MonitorHistoryAction struct {
	Type   string `json:"type"`
	Result any    `json:"result,omitempty"`
	Err    string `json:"err,omitempty"`
}

// PatchMonitorRequest is the body of PATCH /monitors/{name}. The
// only supported field is `enabled`; everything else lives in the
// YAML and is owned by the agent.
type PatchMonitorRequest struct {
	Enabled *bool `json:"enabled,omitempty"`
}

func (c *Client) ListMonitors(ctx context.Context) ([]Monitor, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/monitors", nil)
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
		return nil, errorFromBody("GET /monitors", resp)
	}
	var out []Monitor
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// ToggleMonitor flips a monitor's enabled flag and returns the
// post-toggle state.
func (c *Client) ToggleMonitor(ctx context.Context, name string, enabled bool) (*Monitor, error) {
	body := PatchMonitorRequest{Enabled: &enabled}
	resp, err := c.doJSON(ctx, http.MethodPatch, "/monitors/"+name, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errorFromBody("PATCH /monitors/"+name, resp)
	}
	var out Monitor
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MonitorHistory returns the last `tail` entries for a monitor.
// tail<=0 returns all (heavy for long-running monitors).
func (c *Client) MonitorHistory(ctx context.Context, name string, tail int) ([]MonitorHistoryEntry, error) {
	url := c.base + "/monitors/" + name + "/history"
	if tail > 0 {
		url += "?tail=" + strconv.Itoa(tail)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
		return nil, errorFromBody("GET /monitors/"+name+"/history", resp)
	}
	var out []MonitorHistoryEntry
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
