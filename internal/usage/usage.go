// Package usage reads/writes the Claude Code statusline payload, which is the
// only documented place that exposes per-window rate-limit *percentages*
// (`five_hour.used_percentage`, `seven_day.used_percentage`).
//
// The flow:
//
//  1. The user installs `sunnytui statusline` as Claude Code's statusline
//     command (see `sunnytui statusline-install` for the snippet).
//  2. Each time Claude Code refreshes its statusline, it pipes a JSON
//     payload to stdin of our subcommand. We persist it to
//     ~/.sunny/usage-snapshot.json and print a one-line summary
//     (Claude Code displays whatever stdout we produce).
//  3. The TUI sidebar reads that snapshot to draw the usage bars.
//
// This mirrors claude-hud's "external usage snapshot" pattern.
package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Window is a single rate-limit bucket as Claude Code reports it.
//
// UsedPercentage is float64 even though it's usually an integer — Claude
// Code occasionally emits values like 7.000000000000001 (FP arithmetic
// noise from internal aggregation), and an int-typed field would refuse
// to decode them, knocking the whole snapshot offline and dropping the
// sidebar back to the "ok" rate_limit_event fallback.
type Window struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"` // unix seconds
}

// Payload is a subset of the Claude Code statusline stdin JSON we care about.
type Payload struct {
	Model *struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"model,omitempty"`

	ContextWindow *struct {
		ContextWindowSize int     `json:"context_window_size"`
		UsedPercentage    float64 `json:"used_percentage"`
	} `json:"context_window,omitempty"`

	RateLimits *struct {
		FiveHour *Window `json:"five_hour,omitempty"`
		SevenDay *Window `json:"seven_day,omitempty"`
	} `json:"rate_limits,omitempty"`
}

// Snapshot wraps the raw payload with our own freshness timestamp.
type Snapshot struct {
	UpdatedAt int64           `json:"updated_at"`
	Raw       json.RawMessage `json:"raw"`
}

func snapshotPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".sunny", "usage-snapshot.json"), nil
}

// Write persists the payload bytes to disk. Called from the statusline
// subcommand on every Claude Code refresh.
func Write(rawPayload []byte) error {
	p, err := snapshotPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	snap := Snapshot{
		UpdatedAt: time.Now().Unix(),
		Raw:       rawPayload,
	}
	out, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return os.WriteFile(p, out, 0o644)
}

// Read returns the most recent payload, or nil if no fresh snapshot exists.
// `maxAge` is the cutoff for considering a snapshot stale (e.g. 10 minutes).
// A nil result with no error means "no usable snapshot".
func Read(maxAge time.Duration) (*Payload, time.Time, error) {
	p, err := snapshotPath()
	if err != nil {
		return nil, time.Time{}, err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, time.Time{}, nil
		}
		return nil, time.Time{}, err
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, time.Time{}, fmt.Errorf("decode snapshot: %w", err)
	}
	updated := time.Unix(snap.UpdatedAt, 0)
	if maxAge > 0 && time.Since(updated) > maxAge {
		return nil, updated, nil
	}
	var payload Payload
	if err := json.Unmarshal(snap.Raw, &payload); err != nil {
		return nil, updated, fmt.Errorf("decode payload: %w", err)
	}
	return &payload, updated, nil
}

// SnapshotPath returns the on-disk location for the snapshot, useful for
// logging and `statusline-install` instructions.
func SnapshotPath() string {
	p, _ := snapshotPath()
	return p
}
