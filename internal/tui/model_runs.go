package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/noesrafa/sunny/internal/client"
)

// applyRunsLoaded stores host's fresh GET /runs response into the
// per-peer state. Empty Runs after a successful load means "no runs
// configured" — distinct from "load errored", which leaves the
// previous list in place but records the error.
func (m *Model) applyRunsLoaded(msg runsLoadedMsg) {
	cur, ok := m.peerRuns[msg.Host]
	if !ok {
		cur = &peerRunsState{}
		m.peerRuns[msg.Host] = cur
	}
	cur.loading = false
	if msg.Err != nil {
		cur.err = msg.Err.Error()
		return
	}
	cur.err = ""
	cur.list = msg.Runs
}

// runActionCmd wraps the four POST lifecycle calls (start/stop/restart)
// + delete with a uniform error→runActionFailedMsg surface. The
// follow-up bus event (run.started/exited/etc.) drives the UI
// refresh, so we don't return the post-action Run on success — it
// would get overwritten by the bus-driven fetch anyway.
func (m Model) runActionCmd(host, id, action string) tea.Cmd {
	c := m.clientFor(host)
	if c == nil {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var err error
		switch action {
		case "start":
			_, err = c.StartRun(cctx, id)
		case "stop":
			_, err = c.StopRun(cctx, id)
		case "restart":
			_, err = c.RestartRun(cctx, id)
		case "delete":
			err = c.DeleteRun(cctx, id)
		}
		if err != nil {
			return runActionFailedMsg{Host: host, RunID: id, Action: action, Err: err}
		}
		// Force a refetch so the sidebar updates immediately without
		// waiting for the bus event round-trip. Bus events are still
		// the primary refresh path; this is just a latency hedge.
		return runsLoadedMsg{Host: host}
	}
}

// createRunCmd posts a new run definition and surfaces a refresh
// when it lands. Errors are returned via runActionFailedMsg.
func (m Model) createRunCmd(host string, body CreateRunMsg) tea.Cmd {
	c := m.clientFor(host)
	if c == nil {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_, err := c.CreateRun(cctx, client.CreateRunRequest{
			Name:    body.Name,
			Cwd:     body.Cwd,
			Command: body.Command,
		})
		if err != nil {
			return runActionFailedMsg{Host: host, Action: "create", Err: err}
		}
		return runsLoadedMsg{Host: host}
	}
}

// updateRunCmd patches an existing run. We send all three fields
// (name, cwd, command) — the form always carries the full set, so
// PATCH semantics are equivalent to PUT here.
func (m Model) updateRunCmd(host string, body UpdateRunMsg) tea.Cmd {
	c := m.clientFor(host)
	if c == nil {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		name, cwd, cmd := body.Name, body.Cwd, body.Command
		_, err := c.PatchRun(cctx, body.ID, client.PatchRunRequest{
			Name:    &name,
			Cwd:     &cwd,
			Command: &cmd,
		})
		if err != nil {
			return runActionFailedMsg{Host: host, RunID: body.ID, Action: "update", Err: err}
		}
		return runsLoadedMsg{Host: host}
	}
}

// chainFetchAfter returns a cmd that runs `inner` and, after it
// completes, fires another runsLoadedMsg-producing fetch. Used to
// re-fetch from disk when the inner command's response (often a
// stub runsLoadedMsg with no Runs payload) needs to be replaced
// with the real list.
func (m Model) chainFetchAfter(inner tea.Cmd, host string) tea.Cmd {
	if inner == nil {
		return m.fetchRunsCmd(host)
	}
	fetch := m.fetchRunsCmd(host)
	return tea.Sequence(inner, fetch)
}
