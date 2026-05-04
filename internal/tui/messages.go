package tui

import (
	"github.com/noesrafa/sunny/internal/client"
	"github.com/noesrafa/sunny/internal/sysstats"
)

// journalEventMsg carries one event from a session's watch stream
// (GET /watch). Pumped by waitForJournalEvent and consumed by
// Update, which dispatches into the session's ApplyJournalEvent and
// re-arms the next read.
//
// Ch holds the watch channel the event was read from; the handler
// compares it against the session's current WatchEvents() to drop
// stragglers from a previous watch (e.g. after RebindConv). Two
// channels created with separate make() calls compare unequal even
// when they carry the same element type.
type journalEventMsg struct {
	SessionID string
	Event     client.JournalEvent
	Ch        <-chan client.JournalEvent
}

// journalWatchClosedMsg fires when a session's watch channel closes
// — typically because the session was Closed (TUI tearing down) or
// the bubbletea ctx was cancelled. The model stops re-arming for
// that session; live sessions stay subscribed.
//
// Ch is the channel that closed. The handler ignores closes from
// channels that are no longer the session's current watch (e.g. an
// old watch that was rotated out by RebindConv).
type journalWatchClosedMsg struct {
	SessionID string
	Ch        <-chan client.JournalEvent
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

// peerSyncTickMsg fires every couple of seconds so the model can
// reconcile its peerManagers map against Federation.Names() —
// tailnet discovery runs async after boot, so peers join the
// federation late and need to surface in the header switcher
// without forcing a TUI restart.
type peerSyncTickMsg struct{}

// tabsRefreshedMsg carries a fresh GET /tabs response for one
// peer. Triggered by tab.* bus events and by peerSyncTick when a
// new peer joins the federation. The handler reconciles the
// peer's session.Manager: adds sessions for tabs we don't have,
// closes sessions whose tab disappeared.
type tabsRefreshedMsg struct {
	Host string
	Tabs []client.Tab
	Err  error
}

// tabPatchFailedMsg surfaces a failed PATCH /tabs/{id} so we can log
// it. The user's local mutation already happened (e.g. rename
// applied to cur.Title); we just log the sync failure.
type tabPatchFailedMsg struct {
	Host  string
	TabID string
	Err   error
}
