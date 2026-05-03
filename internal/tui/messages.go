package tui

import (
	"github.com/noesrafa/sunny/internal/sysstats"
)

// branchTickMsg is fired every few seconds so the input-hint row can pick up
// branch changes (e.g. user ran `git checkout` in another terminal).
type branchTickMsg struct{}

// logoTickMsg drives the SUNNY-letters gradient sweep. One per ~120ms is
// fast enough to read as motion and slow enough to be unobtrusive on
// long-running sessions.
type logoTickMsg struct{}

// sysStatsTickMsg requests a fresh CPU/RAM sample.
type sysStatsTickMsg struct{}

// sysStatsResultMsg carries one snapshot from sysstats.Sample.
type sysStatsResultMsg struct {
	Stats sysstats.Stats
}

// saveTickMsg fires every saveFlushInterval and flushes the state.json
// debounce buffer to disk if anything changed since the last write.
type saveTickMsg struct{}

// bgPollMsg fires periodically to re-ask the terminal for its current
// background color, so Auto themes follow OS appearance changes.
type bgPollMsg struct{}
