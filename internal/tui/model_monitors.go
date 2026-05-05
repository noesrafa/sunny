package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/noesrafa/sunny/internal/client"
)

// applyMonitorsLoaded stores host's fresh GET /monitors response
// into the per-peer state. Errors leave the previous list in
// place but record the message for display.
func (m *Model) applyMonitorsLoaded(msg monitorsLoadedMsg) {
	cur, ok := m.peerMonitors[msg.Host]
	if !ok {
		cur = &peerMonitorsState{}
		m.peerMonitors[msg.Host] = cur
	}
	cur.loading = false
	if msg.Err != nil {
		cur.err = msg.Err.Error()
		return
	}
	cur.err = ""
	cur.list = msg.Monitors
}

// toggleMonitorCmd flips a monitor's enabled flag on the active
// peer. Errors land as monitorActionFailedMsg; success returns
// monitorsLoadedMsg with no Runs/Err so the appmsg handler refetches.
func (m Model) toggleMonitorCmd(host, name string, enabled bool) tea.Cmd {
	c := m.clientFor(host)
	if c == nil {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_, err := c.ToggleMonitor(cctx, name, enabled)
		if err != nil {
			return monitorActionFailedMsg{Host: host, Name: name, Action: "toggle", Err: err}
		}
		return monitorsLoadedMsg{Host: host}
	}
}

// fetchMonitorHistoryCmd kicks off the async history tail for the
// log viewer dialog. Result lands as monitorHistoryLoadedMsg.
func (m Model) fetchMonitorHistoryCmd(host, name string, tail int) tea.Cmd {
	c := m.clientFor(host)
	if c == nil {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		entries, err := c.MonitorHistory(cctx, name, tail)
		return monitorHistoryLoadedMsg{Host: host, Name: name, Entries: entries, Err: err}
	}
}

// (silence unused import if Model is in flux)
var _ = client.Monitor{}
