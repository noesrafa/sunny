package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/noesrafa/sunny/internal/client"
	"github.com/noesrafa/sunny/internal/session"
)

// This file owns the model's reactions to the agent-CRUD message
// flow (picker → form → save → refresh, archive flow). The dispatch
// itself lives in updateAppMsg (model_appmsg.go); these are the
// helpers it routes to.

// switchAgent reacts to "enter on agent" in the picker. Existing
// sessions are not modified — agent binding is per-session and
// immutable for that session's lifetime, so this always opens a new
// tab.
//
// Local agents auto-create at m.initialCwd (the TUI's launch dir),
// matching the long-standing UX: enter feels instant, no extra
// dialog. Remote agents open the new-session dialog scoped to that
// peer's filesystem so the user can pick a cwd that actually exists
// on the remote daemon — using m.initialCwd there would leak the
// local path into a remote that doesn't have it.
func (m Model) switchAgent(req SwitchAgentMsg) (Model, tea.Cmd, bool) {
	agentID := req.ID
	host := req.Host
	if host == "" {
		host = "local"
	}
	if cur := m.manager.Current(); cur != nil && cur.AgentID() == agentID && cur.Host() == host {
		return m, nil, true // already on this agent on this peer — picker just closes
	}
	if host != "local" {
		peerClient := m.clientFor(host)
		if peerClient == nil {
			m.lastErr = errNoClient
			m.logger.Error("switch agent: no client for peer", "peer", host)
			return m, nil, true
		}
		dialog := NewNewSessionDialog(peerClient, host, "", agentID, m.styles)
		return m, m.overlay.Open(dialog), true
	}
	if cur := m.manager.Current(); cur != nil {
		cur.Draft = m.textarea.Value()
	}
	peerClient := m.clientFor(host)
	if peerClient == nil {
		m.lastErr = errNoClient
		m.logger.Error("switch agent: no client for peer", "peer", host)
		return m, nil, true
	}
	tab, err := peerClient.OpenTab(m.ctx, client.OpenTabRequest{
		AgentID: agentID,
		Cwd:     m.initialCwd,
	})
	if err != nil {
		m.lastErr = err
		m.logger.Error("switch agent: open tab failed", "err", err, "peer", host, "agent", agentID)
		return m, nil, true
	}
	s, err := session.New(m.ctx, m.initialCwd, session.Options{
		Logger:                   m.logger,
		Model:                    m.defaultModel,
		Effort:                   m.defaultEffort,
		DangerousSkipPermissions: m.skipPerms,
		AgentID:                  agentID,
		Host:                     host,
		TabID:                    tab.ID,
		ConvID:                   tab.ConvID,
		Title:                    tab.Title,
	})
	if err != nil {
		m.lastErr = err
		m.logger.Error("switch agent failed", "err", err, "agent", agentID, "host", host)
		return m, nil, true
	}
	s.Bind(m.ctx, peerClient, agentID, host)
	m.manager.Add(s)
	m.textarea.Reset()
	m.layout()
	m.refreshViewport()
	m.saveState()
	return m, waitForJournalEvent(s.ID, s.WatchEvents()), true
}

// submitAgentForm runs the create/update API call asynchronously and
// emits AgentSavedMsg with the result. Both the form and the picker
// listen for AgentSavedMsg — the form to dismiss/show error, the
// picker to refresh.
func (m Model) submitAgentForm(req SubmitAgentFormMsg) tea.Cmd {
	c := m.client
	if c == nil {
		return func() tea.Msg {
			return AgentSavedMsg{EditID: req.EditID, Err: errNoClient}
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if req.EditID == "" {
			a, err := c.CreateAgent(ctx, client.AgentCreate{
				Name:        req.Name,
				Description: req.Description,
				Model:       req.Model,
				Effort:      req.Effort,
				Provider:    req.Provider,
				Prompt:      req.Prompt,
			})
			if err != nil {
				return AgentSavedMsg{Err: err}
			}
			return AgentSavedMsg{ID: a.ID}
		}
		_, err := c.UpdateAgent(ctx, req.EditID, client.AgentPatch{
			Name:        &req.Name,
			Description: &req.Description,
			Model:       &req.Model,
			Effort:      &req.Effort,
			Provider:    &req.Provider,
			Prompt:      &req.Prompt,
		})
		if err != nil {
			return AgentSavedMsg{EditID: req.EditID, Err: err}
		}
		return AgentSavedMsg{EditID: req.EditID, ID: req.EditID}
	}
}

// renameAgentCmd patches just the agent's name and emits
// AgentChangedMsg so the picker (and any other open viewers) refresh.
func (m Model) renameAgentCmd(req SubmitAgentRenameMsg) tea.Cmd {
	c := m.client
	if c == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if _, err := c.UpdateAgent(ctx, req.ID, client.AgentPatch{Name: &req.Name}); err != nil {
			return AgentChangedMsg{Status: "rename failed: " + err.Error()}
		}
		return AgentChangedMsg{Status: "renamed to " + req.Name}
	}
}

// deleteAgentCmd archives an agent (the daemon moves it to .archive/).
// Emits an AgentChangedMsg the picker uses to refresh its list.
func (m Model) deleteAgentCmd(id string) tea.Cmd {
	c := m.client
	if c == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := c.DeleteAgent(ctx, id); err != nil {
			return AgentChangedMsg{Status: "archive failed: " + err.Error()}
		}
		return AgentChangedMsg{Status: "archived"}
	}
}

// errNoClient is returned by agent + secrets CRUD commands when the
// TUI was launched without a daemon connection. Should be unreachable
// in practice (auto-start always makes one).
var errNoClient = clientErr("no daemon connection")

type clientErr string

func (e clientErr) Error() string { return string(e) }
