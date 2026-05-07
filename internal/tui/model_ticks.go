package tui

import (
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/noesrafa/sunny/internal/anim"
	"github.com/noesrafa/sunny/internal/session"
	"github.com/noesrafa/sunny/internal/sysstats"
)

// handleSpinnerTick keeps ticking while any session is thinking OR a modal
// that wants live data is open.
func (m Model) handleSpinnerTick(msg spinner.TickMsg) (Model, tea.Cmd) {
	if !m.anyThinking() && !m.overlay.HasOpen() {
		return m, nil
	}
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	if cur := m.manager.Current(); cur != nil && cur.State == session.StateThinking {
		m.refreshViewport()
	}
	return m, cmd
}

// branchTickCmd schedules the next branch poll. Each tick fires TWO git
// subprocesses per session (`branch --show-current` + `status --porcelain`),
// so on a 4-session setup that's 8 invocations per tick. 15s is the sweet
// spot: the input-hint row still feels live to the user (checkouts done
// outside the TUI surface within 15s), and we drop CPU spent in git +
// fork/exec by ~5×.
func branchTickCmd() tea.Cmd {
	return tea.Tick(15*time.Second, func(time.Time) tea.Msg { return branchTickMsg{} })
}

// logoTickCmd drives the brand-mark gradient sweep. 120ms cadence keeps
// the animation visible without saturating the program loop on idle
// terminals. Each tick increments Model.logoFrame and re-arms itself.
func logoTickCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return logoTickMsg{} })
}

// bgPollCmd schedules the next terminal-background re-query. 3s is the
// shortest delay that still feels lazy on the wire (~20 OSC 11 round
// trips per minute, each is a few bytes) but short enough that flipping
// macOS appearance feels effectively instant — without it the user
// stares at the wrong-polarity TUI for up to half a minute. Resize and
// the explicit `tea.RequestBackgroundColor` on startup also drive
// re-queries so this cadence is just the safety net.
func bgPollCmd() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return bgPollMsg{} })
}

// sysStatsTickCmd is the metronome for CPU/RAM sampling. Each tick spawns
// `top -l 1` (~50ms wall, but a non-trivial fork/exec). 10s cadence keeps
// the bars looking alive while reducing the per-day invocation count by
// ~2.5× from the old 4s setting. Tick → sample → tick is intentionally
// split across two messages so the actual `top` invocation runs off the
// main loop.
func sysStatsTickCmd() tea.Cmd {
	return tea.Tick(10*time.Second, func(time.Time) tea.Msg { return sysStatsTickMsg{} })
}

// saveFlushInterval bounds how often the dirty state.json gets rewritten.
// Five seconds is the sweet spot: short enough that a crash loses very
// little, long enough that an active transcript collapses dozens of
// per-event saveState() calls into a single MarshalIndent + atomic rename.
const saveFlushInterval = 5 * time.Second

// saveTickCmd schedules the next state-flush check. The actual write only
// happens if Model.saveDirty is true at tick time.
func saveTickCmd() tea.Cmd {
	return tea.Tick(saveFlushInterval, func(time.Time) tea.Msg { return saveTickMsg{} })
}

// fetchSysStatsCmd hits GET /stats on the active peer and returns
// the system block as a sysStatsResultMsg. Replaces the old
// sysstats.Sample()-on-the-TUI-host path so the sidebar bars reflect
// whichever daemon the user is viewing — local or remote.
//
// Always going through HTTP (even for local) keeps the path uniform
// and lets the daemon own the ~1s `top -l 2` cost. A failed fetch
// (peer offline, transient network) returns an empty Stats which
// renders as no bars; the next tick recovers naturally.
func (m *Model) fetchSysStatsCmd() tea.Cmd {
	c := m.clientFor(m.activePeer)
	if c == nil {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		s, err := c.FetchStats(ctx)
		if err != nil {
			return sysStatsResultMsg{}
		}
		return sysStatsResultMsg{Stats: sysstats.Stats{
			CPUPct:        s.System.CPUPercent,
			MemPct:        s.System.MemoryPercent,
			MemTotalBytes: s.System.MemoryTotalB,
			MemUsedBytes:  s.System.MemoryUsedB,
			NumCPU:        s.System.NumCPU,
		}}
	}
}

// handleBranchTick fans out one async GET /git/status per session
// (across every peer, not just the active one — the sidebar shows
// dirty pills for every visible session). Each fetch lands as a
// gitStatusLoadedMsg that the main update loop applies.
//
// The 15s cadence × N sessions × N peers can balloon, so each
// request rides the per-peer client (cheap localhost RT for local,
// tailnet RT otherwise). The daemon's git package is fork+exec on
// every call — fine at this rate; if it ever isn't, cache there.
func (m *Model) handleBranchTick() tea.Cmd {
	cmds := []tea.Cmd{branchTickCmd()}
	for _, s := range m.allSessions() {
		if cmd := m.fetchGitStatusCmd(s); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

// fetchGitStatusCmd returns a tea.Cmd that calls GET /git/status on
// the session's host peer and posts a gitStatusLoadedMsg. nil when
// the session has no host client (peer offline / not configured) or
// no cwd to ask about.
func (m *Model) fetchGitStatusCmd(s *session.Session) tea.Cmd {
	if s == nil || s.Cwd == "" {
		return nil
	}
	c := m.clientFor(s.Host())
	if c == nil {
		return nil
	}
	ctx := m.ctx
	cwd := s.Cwd
	id := s.ID
	return func() tea.Msg {
		branch, changes, err := c.GitStatus(ctx, cwd)
		return gitStatusLoadedMsg{SessionID: id, Branch: branch, Changes: changes, Err: err}
	}
}

// peerSyncTickCmd reconciles the federation roster every 2s.
// Tailnet auto-discovery runs in a goroutine after boot so peers
// land in fed.Names() asynchronously; without this tick they
// wouldn't show up in the header switcher until the user restarted
// the TUI.
func peerSyncTickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return peerSyncTickMsg{} })
}

// federationDiscoverTickCmd schedules the next /federation/peers
// fetch. 30s matches the daemon's federationCacheTTL, so each tick
// almost always hits the cache for free; only when the cache window
// rolls over does the daemon pay the tailnet sweep cost.
func federationDiscoverTickCmd() tea.Cmd {
	return tea.Tick(30*time.Second, func(time.Time) tea.Msg { return federationDiscoverTickMsg{} })
}

// fetchFederationPeersCmd asks the local daemon for its current
// federation list. nil when there's no federation client to call.
// Errors land as federationDiscoveredMsg.Err and are logged by the
// handler — the next tick retries.
func (m *Model) fetchFederationPeersCmd() tea.Cmd {
	if m.fed == nil {
		return nil
	}
	c := m.fed.Local()
	if c == nil {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		peers, err := c.FetchFederationPeers(ctx)
		return federationDiscoveredMsg{Peers: peers, Err: err}
	}
}

// handlePeerSync compares m.peerOrder against fed.Names() and adds
// any missing peers (with an empty session.Manager + a tabs
// refetch so the new peer's open tabs show up immediately).
// Removal is intentional NOT supported here — a peer that went
// offline is likely to come back; dropping it would close every
// session the user had open against it.
func (m *Model) handlePeerSync() tea.Cmd {
	cmds := []tea.Cmd{peerSyncTickCmd()}
	if m.fed != nil {
		known := map[string]bool{}
		for _, n := range m.peerOrder {
			known[n] = true
		}
		for _, name := range m.fed.Names() {
			if known[name] {
				continue
			}
			m.peerManagers[name] = session.NewManager()
			m.peerOrder = append(m.peerOrder, name)
			m.logger.Info("peer joined", "name", name)
			cmds = append(cmds, m.refetchTabsCmd(name))
		}
	}
	return tea.Batch(cmds...)
}

func (m *Model) handleAnimStep(msg anim.StepMsg) tea.Cmd {
	if msg.ID != m.thinkingAnim.ID() {
		return nil
	}
	m.thinkingAnim.Tick()
	if !m.anyThinking() {
		return nil
	}
	m.refreshViewport()
	return m.thinkingAnim.Step()
}
