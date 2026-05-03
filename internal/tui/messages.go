package tui

import (
	"github.com/noesrafa/sunny/internal/claude"
	"github.com/noesrafa/sunny/internal/sysstats"
)

type sessionEventMsg struct {
	SessionID string
	Stream    *claude.Stream // identity of the stream that produced this event
	Event     claude.Event
}

type sessionClosedMsg struct {
	SessionID string
	Stream    *claude.Stream // identity of the stream that closed; ignored if it isn't the session's current stream
}

// paneOutputMsg is fired by the per-pane PTY-read tea.Cmd. The data has
// already been written into the vt10x emulator inside Pane.ReadOnce, so the
// handler only needs to trigger a re-render and re-arm the next read.
type paneOutputMsg struct {
	PaneID string
}

type paneClosedMsg struct {
	PaneID string
	Err    error
}

// branchTickMsg is fired every few seconds so the input-hint row can pick up
// branch changes (e.g. user ran `git checkout` in another terminal).
type branchTickMsg struct{}

// logoTickMsg drives the SUNNY-letters gradient sweep. One per ~120ms is
// fast enough to read as motion and slow enough to be unobtrusive on
// long-running sessions.
type logoTickMsg struct{}

// sysStatsTickMsg requests a fresh CPU/RAM sample. The handler returns a
// sysStatsResultMsg once `top` has come back with output.
type sysStatsTickMsg struct{}

// sysStatsResultMsg carries one snapshot from sysstats.Sample.
type sysStatsResultMsg struct {
	Stats sysstats.Stats
}

// saveTickMsg fires every saveFlushInterval and flushes the state.json
// debounce buffer to disk if anything changed since the last write. The
// granular saveState() calls scattered through Update only mark dirty;
// this is what actually pays the I/O cost.
type saveTickMsg struct{}

// bgPollMsg fires periodically to re-ask the terminal for its current
// background color. Together with the startup query and the resize-time
// query in the model, this catches macOS appearance changes (Ghostty
// updates its bg) within ~30s without paying for sub-second polling.
type bgPollMsg struct{}
