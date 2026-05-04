package server

import (
	"net/http"
	"runtime"
	"time"

	"github.com/noesrafa/sunny/internal/sysstats"
)

// statsResponse is the body returned by GET /stats. Shape is stable
// — clients (sunny TUI, third-party apps) consume this directly.
type statsResponse struct {
	Daemon  daemonStats  `json:"daemon"`
	Counts  countStats   `json:"counts"`
	Live    liveStats    `json:"live"`
	System  systemStats  `json:"system"`
	Process processStats `json:"process"`
}

type daemonStats struct {
	Version           string   `json:"version"`
	InstanceID        string   `json:"instance_id"`
	UptimeSeconds     int64    `json:"uptime_s"`
	StartedAt         string   `json:"started_at"`
	ProvidersConfigured []string `json:"providers_configured"`
	DefaultProvider   string   `json:"default_provider,omitempty"`
}

type countStats struct {
	Agents        int            `json:"agents"`
	Conversations int            `json:"conversations"`
	Tabs          int            `json:"tabs"`
	ConvsPerAgent map[string]int `json:"conversations_per_agent,omitempty"`
}

type liveStats struct {
	TurnsInFlight  []TurnRef      `json:"turns_in_flight"`
	BusSubscribers int            `json:"bus_subscribers"`
	Watchers       map[string]int `json:"watchers,omitempty"`
}

// systemStats is whole-machine CPU + memory. Zero on platforms that
// sysstats doesn't support (today: anything other than darwin).
type systemStats struct {
	CPUPercent     float64 `json:"cpu_percent"`
	NumCPU         int     `json:"num_cpu"`
	MemoryPercent  float64 `json:"memory_percent"`
	MemoryTotalB   uint64  `json:"memory_total_bytes"`
	MemoryUsedB    uint64  `json:"memory_used_bytes"`
	Platform       string  `json:"platform"`
}

// processStats is the daemon's own resource use (Go runtime).
type processStats struct {
	Goroutines  int    `json:"goroutines"`
	HeapAllocB  uint64 `json:"heap_alloc_bytes"`
	HeapSysB    uint64 `json:"heap_sys_bytes"`
}

// stats answers GET /stats with a one-shot snapshot of the daemon's
// runtime state, the on-disk index counts, and the host's CPU/RAM.
//
// This is the endpoint other peers and external apps hit to render a
// "what is this daemon doing right now" dashboard without owning any
// of the calculation. CPU sampling is the slowest part (~1s under
// `top -l 2`); the rest is all in-memory.
func (s *server) stats(w http.ResponseWriter, _ *http.Request) {
	out := statsResponse{
		Daemon: daemonStats{
			Version:    s.version,
			InstanceID: s.instanceID,
			UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
			StartedAt:  s.startedAt.Format(time.RFC3339),
		},
	}

	if eng := s.engine.Load(); eng != nil {
		out.Daemon.ProvidersConfigured = eng.ProviderNames()
		out.Daemon.DefaultProvider = eng.DefaultProvider()
	}

	agents := s.store.Agents()
	out.Counts.Agents = len(agents)
	out.Counts.ConvsPerAgent = make(map[string]int, len(agents))
	totalConvs := 0
	for _, a := range agents {
		n, err := s.conv.Count(a.Slug)
		if err != nil {
			continue
		}
		out.Counts.ConvsPerAgent[a.Slug] = n
		totalConvs += n
	}
	out.Counts.Conversations = totalConvs
	if s.tabs != nil {
		out.Counts.Tabs = len(s.tabs.List())
	}

	out.Live.TurnsInFlight = s.activeTurns.snapshot()
	if s.hub != nil {
		out.Live.BusSubscribers = s.hub.SubCount()
	}
	if s.sink != nil {
		out.Live.Watchers = s.sink.LiveStats()
	}

	if sample, err := sysstats.Sample(); err == nil {
		out.System = systemStats{
			CPUPercent:    sample.CPUPct,
			NumCPU:        sample.NumCPU,
			MemoryPercent: sample.MemPct,
			MemoryTotalB:  sample.MemTotalBytes,
			MemoryUsedB:   sample.MemUsedBytes,
			Platform:      runtime.GOOS,
		}
	} else {
		out.System.Platform = runtime.GOOS
		out.System.NumCPU = runtime.NumCPU()
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	out.Process = processStats{
		Goroutines: runtime.NumGoroutine(),
		HeapAllocB: ms.HeapAlloc,
		HeapSysB:   ms.HeapSys,
	}

	writeJSON(w, http.StatusOK, out)
}
