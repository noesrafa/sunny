package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// DaemonVersion is what GET /sunny/version returns. StartedAt
// changes only on a fresh boot, so callers can detect a successful
// restart by polling for a different value.
type DaemonVersion struct {
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
	OS        string    `json:"os"`
	Arch      string    `json:"arch"`
}

// LifecycleAck is the 202 body returned by Restart/Update/Stop. The
// caller stores StartedAt and waits for it to change (via WaitRestarted)
// to confirm the daemon actually came back fresh.
type LifecycleAck struct {
	StartedAt time.Time `json:"started_at"`
}

// VersionCheck mirrors GET /sunny/version/check. UpdateAvailable is
// what UIs gate on; Error carries a non-fatal explanation when the
// GitHub fetch failed (offline, rate-limited, etc.) so the caller can
// stay quiet rather than show a misleading "you're up to date".
type VersionCheck struct {
	Current         string    `json:"current"`
	Latest          string    `json:"latest,omitempty"`
	UpdateAvailable bool      `json:"update_available"`
	ReleaseURL      string    `json:"release_url,omitempty"`
	PublishedAt     string    `json:"published_at,omitempty"`
	CheckedAt       time.Time `json:"checked_at"`
	Error           string    `json:"error,omitempty"`
}

// CheckUpdate fetches GET /sunny/version/check. The daemon caches the
// GitHub response for ~5 min so polling this is cheap.
func (c *Client) CheckUpdate(ctx context.Context) (*VersionCheck, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/sunny/version/check", nil)
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
		return nil, errorFromBody("GET /sunny/version/check", resp)
	}
	var out VersionCheck
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DaemonVersion fetches GET /sunny/version.
func (c *Client) DaemonVersion(ctx context.Context) (*DaemonVersion, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/sunny/version", nil)
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
		return nil, errorFromBody("GET /sunny/version", resp)
	}
	var out DaemonVersion
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RestartDaemon asks the daemon to restart itself. Returns the ack
// (with the OLD started_at) so the caller can wait for the new
// daemon by calling WaitRestarted.
func (c *Client) RestartDaemon(ctx context.Context) (*LifecycleAck, error) {
	return c.postLifecycle(ctx, "/sunny/restart", nil)
}

// UpdateDaemon asks the daemon to upgrade and restart. Same response
// shape as RestartDaemon — wait for started_at to change.
func (c *Client) UpdateDaemon(ctx context.Context) (*LifecycleAck, error) {
	return c.postLifecycle(ctx, "/sunny/update", nil)
}

// StopDaemon asks the daemon to stop. Requires confirm=true on the
// server side, so we always send it; callers are expected to have
// confirmed with the user before calling. After this returns the
// daemon will go down — clients should NOT poll for a new started_at
// (no recovery). The peer is offline until something starts it again.
func (c *Client) StopDaemon(ctx context.Context) (*LifecycleAck, error) {
	return c.postLifecycle(ctx, "/sunny/stop", map[string]bool{"confirm": true})
}

func (c *Client) postLifecycle(ctx context.Context, path string, body any) (*LifecycleAck, error) {
	var resp *http.Response
	var err error
	if body != nil {
		resp, err = c.doJSON(ctx, http.MethodPost, path, body)
	} else {
		var req *http.Request
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, nil)
		if err != nil {
			return nil, err
		}
		c.auth(req)
		resp, err = c.hc.Do(req)
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return nil, errorFromBody("POST "+path, resp)
	}
	var ack LifecycleAck
	if err := json.NewDecoder(resp.Body).Decode(&ack); err != nil {
		return nil, err
	}
	return &ack, nil
}

// WaitRestarted polls /sunny/version until the daemon's started_at
// is strictly after `before` (or timeout). Used by callers to know
// when a restart/update has actually completed and the new daemon
// is serving requests.
func (c *Client) WaitRestarted(ctx context.Context, before time.Time, timeout time.Duration) (*DaemonVersion, error) {
	deadline := time.Now().Add(timeout)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		probeCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		v, err := c.DaemonVersion(probeCtx)
		cancel()
		if err == nil && v.StartedAt.After(before) {
			return v, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("daemon did not restart within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
