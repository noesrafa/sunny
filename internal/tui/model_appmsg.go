package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noesrafa/sunny/internal/client"
	"github.com/noesrafa/sunny/internal/session"
)

// updateAppMsg handles in-app messages emitted by dialogs and other
// sub-models. It owns its messages — when handled is true the caller MUST
// return immediately.
func (m Model) updateAppMsg(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch v := msg.(type) {
	case CloseDialogMsg:
		m.overlay.CloseTop()
		return m, nil, true
	case ConfirmQuitMsg:
		// Force a synchronous flush so the in-flight draft + transcript
		// don't get lost to the debounce window.
		if m.saveDirty {
			m.flushState()
		}
		return m, tea.Quit, true
	case ConfirmCloseSessionMsg:
		m.overlay.CloseTop()
		m.handleCloseTab()
		return m, nil, true
	case ConfirmNewConvMsg:
		// "Reset chat" used to spawn a fresh claude subprocess. Now the
		// engine owns the conversation lifecycle, so locally we just clear
		// items + state and let the next /chat call start a new turn.
		m.overlay.CloseTop()
		cur := m.manager.Current()
		if cur == nil {
			return m, nil, true
		}
		cur.Items = nil
		cur.State = session.StateIdle
		cur.LastErr = nil
		cur.ConvID = "" // detach from prior conversation; next send creates a new one
		cur.Turns = 0
		cur.TotalCost = 0
		m.textarea.Reset()
		m.layout()
		m.refreshViewport()
		m.chat.ScrollToBottom()
		m.saveState()
		return m, nil, true
	case CreateSessionMsg:
		return m.createSession(v)
	case RenameSessionMsg:
		m.overlay.CloseTop()
		if cur := m.manager.Current(); cur != nil {
			cur.Title = v.NewTitle
			m.logger.Info("session renamed", "session", cur.ID, "title", v.NewTitle)
		}
		m.saveState()
		return m, nil, true
	case SwitchTabMsg:
		m.overlay.CloseTop()
		m.switchToTab(v.Kind, v.Index)
		return m, nil, true
	case PreviewThemeMsg:
		// Live preview while user navigates the picker. Repaint only —
		// don't close or persist; the dialog still owns the decision.
		m.repaint(v.ID)
		return m, nil, true
	case ApplyThemeMsg:
		// User pressed enter — commit the choice.
		m.overlay.CloseTop()
		m.repaint(v.ID)
		m.saveState()
		m.logger.Info("theme applied", "id", v.ID)
		return m, nil, true
	case CancelSettingsMsg:
		// User pressed esc — roll back to whatever was active before
		// they opened the dialog.
		m.overlay.CloseTop()
		m.repaint(v.OriginalID)
		return m, nil, true

	// --- agent CRUD flow ---

	case OpenSubDialogMsg:
		// A dialog is launching another dialog (e.g. picker → confirm).
		return m, m.overlay.Open(v.Dialog), true

	case SwitchAgentMsg:
		m.overlay.CloseTop()
		return m.switchAgent(v.Slug)

	case OpenAgentFormMsg:
		// Replace the picker with the form (no nested stack — the picker
		// will be re-opened by AgentSavedMsg if needed).
		m.overlay.CloseTop()
		return m, m.overlay.Open(NewAgentFormDialog(m.client, v, m.styles)), true

	case SubmitAgentFormMsg:
		return m, m.submitAgentForm(v), true

	case AgentSavedMsg:
		if v.Err != nil {
			// Form already shows the error; do nothing else.
			return m, nil, true
		}
		// Close the form, reopen the picker so the user sees the change.
		m.overlay.CloseTop()
		curSlug := ""
		if cur := m.manager.Current(); cur != nil {
			curSlug = cur.AgentSlug()
		}
		return m, m.overlay.Open(NewAgentPickerDialog(m.client, curSlug, m.styles)), true

	case DeleteAgentMsg:
		// Confirm dialog approved; close confirm, run delete async.
		m.overlay.CloseTop()
		return m, m.deleteAgentCmd(v.Slug), true

	case AgentChangedMsg:
		// Bubbles down to the open picker (if any) so it refreshes.
		// CloseTop already ran in the upstream handler; nothing else.
		return m, m.overlay.UpdateTop(v), true
	}
	return m, nil, false
}

// switchAgent spawns a new session bound to slug. Existing sessions are
// not modified — agent binding is per-session and immutable for that
// session's lifetime.
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
// emits AgentSavedMsg with the result.
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
			Prompt:      &req.Prompt,
		})
		if err != nil {
			return AgentSavedMsg{EditSlug: req.EditSlug, Err: err}
		}
		return AgentSavedMsg{EditSlug: req.EditSlug, Slug: req.EditSlug}
	}
}

func (m Model) deleteAgentCmd(slug string) tea.Cmd {
	c := m.client
	if c == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := c.DeleteAgent(ctx, slug); err != nil {
			return AgentChangedMsg{Status: "delete failed: " + err.Error()}
		}
		return AgentChangedMsg{Status: "deleted " + slug}
	}
}

// errNoClient is returned by agent CRUD commands when the TUI was
// launched without a daemon connection. Should be unreachable in
// practice (auto-start makes one).
var errNoClient = clientErr("no daemon connection")

type clientErr string

func (e clientErr) Error() string { return string(e) }

// repaint swaps the active palette and re-applies it everywhere a Style
// got copied at construction time. Called by all three settings flows
// (preview, apply, cancel); only apply also closes the overlay and saves.
// Also called from the tea.BackgroundColorMsg handler when Auto mode
// needs to flip dark↔light.
func (m *Model) repaint(id string) {
	t := ResolveTheme(id, m.bgIsLight)
	SetPalette(t.P)
	m.styles = DefaultStyles()
	m.themeID = id

	m.textarea.SetStyles(m.styles.EditorTextarea)
	m.spinner.Style = lipgloss.NewStyle().Foreground(colWarning)
	if m.thinkingAnim != nil {
		m.thinkingAnim.SetColors(colSecondary, colPrimary, colText)
	}

	m.md = nil
	m.mdCache = nil

	m.overlay.RefreshStyles(m.styles)
	m.overlay.RefreshBgIsLight(m.bgIsLight)

	m.refreshViewport()
}

func (m Model) createSession(v CreateSessionMsg) (Model, tea.Cmd, bool) {
	m.overlay.CloseTop()
	if v.Model != "" {
		m.defaultModel = v.Model
	}
	if v.Effort != "" {
		m.defaultEffort = v.Effort
	}
	if cur := m.manager.Current(); cur != nil {
		cur.Draft = m.textarea.Value()
	}
	slug := v.AgentSlug
	if slug == "" {
		slug = m.defaultAgent
	}
	s, err := session.New(m.ctx, v.Cwd, session.Options{
		Logger:                   m.logger,
		Model:                    v.Model,
		Effort:                   v.Effort,
		DangerousSkipPermissions: m.skipPerms,
		AgentSlug:                slug,
		Title:                    slug,
	})
	if err != nil {
		m.lastErr = err
		m.logger.Error("create session failed", "err", err, "cwd", v.Cwd)
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

func (m *Model) switchToTab(kind string, index int) {
	if kind != "claude" {
		return
	}
	m.manager.Active = index
	if cur := m.manager.Current(); cur != nil {
		m.textarea.SetValue(cur.Draft)
		m.textarea.CursorEnd()
		m.layout()
		m.refreshViewport()
		m.chat.ScrollToBottom()
	}
}

// handleChatEvent applies one SSE event to the matching session and
// returns the next read tea.Cmd. Drops events from streams the session
// has already replaced (e.g. a stale read landing after Cancel + a
// fresh turn started). Persists the transcript on the terminal event.
func (m *Model) handleChatEvent(msg chatEventMsg) tea.Cmd {
	sess := m.manager.ByID(msg.SessionID)
	if sess == nil {
		return nil
	}
	terminal := sess.ApplyEvent(msg.Event)
	if cur := m.manager.Current(); cur != nil && cur.ID == sess.ID {
		m.refreshViewport()
		if terminal {
			m.chat.ScrollToBottom()
		}
	}
	if terminal {
		m.saveState()
		return nil
	}
	return waitForChatEvent(sess.ID, msg.Stream)
}

// handleChatStreamDone fires on EOF or transport error. If the daemon
// closed cleanly without a Done event (rare — usually means context was
// cancelled mid-turn), fall the session back to Idle so the textarea
// unlocks.
func (m *Model) handleChatStreamDone(msg chatStreamDoneMsg) {
	sess := m.manager.ByID(msg.SessionID)
	if sess == nil {
		return
	}
	if sess.State == session.StateThinking {
		sess.State = session.StateIdle
	}
	if msg.Err != nil {
		sess.LastErr = msg.Err
		m.logger.Warn("chat stream ended", "session", sess.ID, "err", msg.Err)
	}
	m.refreshViewport()
	m.saveState()
}

func (m Model) openQuitDialog() tea.Cmd {
	anyThinking := false
	for _, s := range m.manager.Sessions {
		if s.State == session.StateThinking {
			anyThinking = true
			break
		}
	}
	d := NewQuitDialog(m.styles, len(m.manager.Sessions), anyThinking)
	return m.overlay.Open(d)
}
