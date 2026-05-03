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

func sysStatsSampleCmd() tea.Cmd {
	return func() tea.Msg {
		st, _ := sysstats.Sample()
		return sysStatsResultMsg{Stats: st}
	}
}

func (m *Model) handleBranchTick() tea.Cmd {
	changed := false
	for _, s := range m.manager.Sessions {
		if s.RefreshBranch() {
			changed = true
		}
	}
	if changed {
		m.refreshViewport()
	}
	return branchTickCmd()
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
