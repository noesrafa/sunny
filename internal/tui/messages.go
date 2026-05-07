package tui

import (
	"github.com/noesrafa/sunny/internal/client"
	"github.com/noesrafa/sunny/internal/git"
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

// federationDiscoverTickMsg fires periodically to re-fetch the
// daemon's GET /federation/peers and add any newly-online peers to
// the federation. Without this, peers that come online after TUI
// boot would never surface — the previous client-side sweep ran
// once at startup.
type federationDiscoverTickMsg struct{}

// federationDiscoveredMsg carries the daemon's GET /federation/peers
// answer. The handler walks the list and applies AddTailnetPeer /
// AddMeshPeer so peerSyncTick reconciles the new entries into
// peerOrder + peerManagers.
type federationDiscoveredMsg struct {
	Peers []client.FederationPeer
	Err   error
}

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

// runsLoadedMsg carries one peer's GET /runs response. Triggered at
// startup, when a new peer joins the federation, and on run.* bus
// events. The handler replaces peerRuns[Host] with the result.
type runsLoadedMsg struct {
	Host string
	Runs []client.Run
	Err  error
}

// runActionFailedMsg surfaces a failed start/stop/restart/delete so
// the manager dialog can show it. The peer's run list is refreshed
// regardless via the bus event the daemon emits, so we don't carry
// the run id forward — the message is purely for error display.
type runActionFailedMsg struct {
	Host   string
	RunID  string
	Action string
	Err    error
}

// monitorsLoadedMsg carries one peer's GET /monitors response.
// Triggered at startup, on monitor.* bus events, and after a
// toggle action.
type monitorsLoadedMsg struct {
	Host     string
	Monitors []client.Monitor
	Err      error
}

// monitorActionFailedMsg surfaces a failed toggle/history-fetch.
type monitorActionFailedMsg struct {
	Host    string
	Name    string
	Action  string
	Err     error
}

// gitStatusLoadedMsg carries the daemon's GET /git/status answer for
// one session's cwd. Fired by the per-session fetch the branch tick
// kicks off; consumed by the model to update Session.Branch +
// Session.Changes via session.ApplyGitStatus.
type gitStatusLoadedMsg struct {
	SessionID string
	Branch    string
	Changes   git.ChangeStats
	Err       error
}

// gitFilesLoadedMsg carries the daemon's GET /git/files answer for
// the diff dialog's left pane. The dialog forwards this through the
// overlay so it can replace its file list and refresh the right
// pane.
type gitFilesLoadedMsg struct {
	Files []git.File
	Err   error
}

// gitDiffLoadedMsg carries the daemon's GET /git/diff answer for one
// file (Path is the same key the dialog used to request the diff;
// it's how we drop stale responses when the user has already moved
// on to a different file).
type gitDiffLoadedMsg struct {
	Path string
	Body string
	Err  error
}

// monitorHistoryLoadedMsg carries the result of GET /monitors/{name}/history.
type monitorHistoryLoadedMsg struct {
	Host    string
	Name    string
	Entries []client.MonitorHistoryEntry
	Err     error
}
