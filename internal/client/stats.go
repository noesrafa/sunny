package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Stats mirrors the daemon's GET /stats response. Field names follow
// the wire JSON; consumers can render dashboards directly off this
// shape.
type Stats struct {
	Daemon  StatsDaemon  `json:"daemon"`
	Counts  StatsCounts  `json:"counts"`
	Live    StatsLive    `json:"live"`
	System  StatsSystem  `json:"system"`
	Process StatsProcess `json:"process"`
}

type StatsDaemon struct {
	Version             string   `json:"version"`
	InstanceID          string   `json:"instance_id"`
	UptimeSeconds       int64    `json:"uptime_s"`
	StartedAt           string   `json:"started_at"`
	ProvidersConfigured []string `json:"providers_configured"`
	DefaultProvider     string   `json:"default_provider,omitempty"`
}

type StatsCounts struct {
	Agents        int            `json:"agents"`
	Conversations int            `json:"conversations"`
	Tabs          int            `json:"tabs"`
	ConvsPerAgent map[string]int `json:"conversations_per_agent,omitempty"`
}

type StatsLive struct {
	TurnsInFlight  []StatsTurn    `json:"turns_in_flight"`
	BusSubscribers int            `json:"bus_subscribers"`
	Watchers       map[string]int `json:"watchers,omitempty"`
}

type StatsTurn struct {
	AgentID string `json:"agent_id"`
	ConvID  string `json:"conv_id"`
}

type StatsSystem struct {
	CPUPercent    float64 `json:"cpu_percent"`
	NumCPU        int     `json:"num_cpu"`
	MemoryPercent float64 `json:"memory_percent"`
	MemoryTotalB  uint64  `json:"memory_total_bytes"`
	MemoryUsedB   uint64  `json:"memory_used_bytes"`
	Platform      string  `json:"platform"`
}

type StatsProcess struct {
	Goroutines int    `json:"goroutines"`
	HeapAllocB uint64 `json:"heap_alloc_bytes"`
	HeapSysB   uint64 `json:"heap_sys_bytes"`
}

// FetchStats hits GET /stats. The CPU sample inside the daemon takes
// ~1s wall (it spans two `top` snapshots), so callers should run
// this off the UI loop.
func (c *Client) FetchStats(ctx context.Context) (*Stats, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/stats", nil)
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
		return nil, fmt.Errorf("stats: HTTP %d", resp.StatusCode)
	}
	var out Stats
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("stats: decode: %w", err)
	}
	return &out, nil
}
