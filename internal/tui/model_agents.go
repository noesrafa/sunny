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

// switchAgent spawns a new session bound to slug. Existing sessions
// are not modified — agent binding is per-session and immutable for
// that session's lifetime, so "switch agent" really means "open a
// new tab on the chosen agent."
func (m Model) switchAgent(slug string) (Model, tea.Cmd, bool) {
	if cur := m.manager.Current(); cur != nil && cur.AgentSlug() == slug {
		return m, nil, true // already on this agent — picker just closes
	}
	if cur := m.manager.Current(); cur != nil {
		cur.Draft = m.textarea.Value()
	}
	s, err := session.New(m.ctx, m.initialCwd, session.Options{
		Logger:                   m.logger,
		Model:                    m.defaultModel,
		Effort:                   m.defaultEffort,
		DangerousSkipPermissions: m.skipPerms,
		AgentSlug:                slug,
		Title:                    slug,
	})
	if err != nil {
		m.lastErr = err
		m.logger.Error("switch agent failed", "err", err, "slug", slug)
		return m, nil, true
	}
	if m.client != nil {
		s.AttachClient(m.client, slug)
	}
	m.manager.Add(s)
	m.textarea.Reset()
	m.layout()
	m.refreshViewport()
	m.saveState()
	return m, nil, true
}

// submitAgentForm runs the create/update API call asynchronously and
// emits AgentSavedMsg with the result. Both the form and the picker
// listen for AgentSavedMsg — the form to dismiss/show error, the
// picker to refresh.
func (m Model) submitAgentForm(req SubmitAgentFormMsg) tea.Cmd {
	c := m.client
	if c == nil {
		return func() tea.Msg {
			return AgentSavedMsg{EditSlug: req.EditSlug, Err: errNoClient}
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if req.EditSlug == "" {
			a, err := c.CreateAgent(ctx, client.AgentCreate{
				Slug:        req.Slug,
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
			return AgentSavedMsg{Slug: a.Slug}
		}
		_, err := c.UpdateAgent(ctx, req.EditSlug, client.AgentPatch{
			Name:        &req.Name,
			Description: &req.Description,
			Model:       &req.Model,
			Effort:      &req.Effort,
			Provider:    &req.Provider,
			Prompt:      &req.Prompt,
		})
		if err != nil {
			return AgentSavedMsg{EditSlug: req.EditSlug, Err: err}
		}
		return AgentSavedMsg{EditSlug: req.EditSlug, Slug: req.EditSlug}
	}
}

// deleteAgentCmd archives an agent (the daemon moves it to .archive/).
// Emits an AgentChangedMsg the picker uses to refresh its list.
func (m Model) deleteAgentCmd(slug string) tea.Cmd {
	c := m.client
	if c == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := c.DeleteAgent(ctx, slug); err != nil {
			return AgentChangedMsg{Status: "archive failed: " + err.Error()}
		}
		return AgentChangedMsg{Status: "archived " + slug}
	}
}

// errNoClient is returned by agent + secrets CRUD commands when the
// TUI was launched without a daemon connection. Should be unreachable
// in practice (auto-start always makes one).
var errNoClient = clientErr("no daemon connection")

type clientErr string

func (e clientErr) Error() string { return string(e) }
