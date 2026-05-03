package tui

import (
	"github.com/noesrafa/sunny/internal/client"
	"github.com/noesrafa/sunny/internal/sysstats"
)

// chatEventMsg carries one event from the daemon's SSE stream. Pumped
// by waitForChatEvent and consumed by Update, which applies it to the
// matching session and re-arms the next read.
type chatEventMsg struct {
	SessionID string
	Stream    *client.Stream
	Event     client.Event
}

// chatStreamDoneMsg fires when the SSE stream ends — either cleanly
// (Err == nil) or with an error (transport / cancel / decode). If the
// session's activeStream still matches Stream we clear it; otherwise
// the stream was already replaced (e.g. user fired a second turn) and
// we ignore.
type chatStreamDoneMsg struct {
	SessionID string
	Stream    *client.Stream
	Err       error
}

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

// busEventMsg surfaces one federated event (from any peer's GET
// /events stream) into the bubbletea loop. The model reacts based
// on Event.Type — typically by emitting AgentChangedMsg so an open
// AgentPickerDialog refreshes itself.
type busEventMsg struct {
	Event client.FederatedEvent
}

// busEventClosedMsg fires when the federation event multiplexer
// closes (ctx cancellation, all peers gone). Today the model just
// stops re-arming the wait; future versions can show a "real-time
// sync paused" hint.
type busEventClosedMsg struct{}
