package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noesrafa/sunny/internal/session"
)

// updateAppMsg is the central dispatch for in-app messages emitted
// by dialogs and other sub-models. The big switch routes to family-
// specific handlers that live next door:
//
//   - model_agents.go  — agent CRUD flow
//   - model_secrets.go — secrets paste flow
//
// When handled is true the caller MUST return immediately so other
// dispatchers don't see the same message twice.
func (m Model) updateAppMsg(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch v := msg.(type) {

	// --- core dialog plumbing ---
	case CloseDialogMsg:
		m.overlay.CloseTop()
		return m, nil, true
	case OpenSubDialogMsg:
		// A dialog is launching another dialog (e.g. picker → confirm).
		return m, m.overlay.Open(v.Dialog), true
	case ConfirmQuitMsg:
		// Force a synchronous flush so the in-flight draft + transcript
		// don't get lost to the debounce window.
		if m.saveDirty {
			m.flushState()
		}
		return m, tea.Quit, true

	// --- session lifecycle ---
	case ConfirmCloseSessionMsg:
		m.overlay.CloseTop()
		m.handleCloseTab()
		return m, nil, true
	case ConfirmNewConvMsg:
		// "Reset chat" used to spawn a fresh claude subprocess. Now the
		// engine owns the conversation lifecycle, so we just clear local
		// items + the conv binding and let the next send create a new one.
		m.overlay.CloseTop()
		cur := m.manager.Current()
		if cur == nil {
			return m, nil, true
		}
		cur.Items = nil
		cur.State = session.StateIdle
		cur.LastErr = nil
		cur.ConvID = ""
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

	// --- theme picker ---
	case PreviewThemeMsg:
		// Live preview while user navigates the picker. Repaint only —
		// don't close or persist; the dialog still owns the decision.
		m.repaint(v.ID)
		return m, nil, true
	case ApplyThemeMsg:
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

	// --- agent CRUD (handlers in model_agents.go) ---
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
			// Form already shows the error; nothing else to do.
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
		// Confirm dialog approved; close confirm, archive async.
		m.overlay.CloseTop()
		return m, m.deleteAgentCmd(v.Slug), true
	case AgentChangedMsg:
		// Bubbles down to the open picker so it refreshes.
		return m, m.overlay.UpdateTop(v), true

	// --- secrets (handlers in model_secrets.go) ---
	case SubmitSecretsMsg:
		// Form submitted — close it, run PUT async.
		m.overlay.CloseTop()
		return m, m.putSecretsCmd(v), true
	case SecretsSavedMsg:
		// Forward to the manager dialog so it reloads its list.
		return m, m.overlay.UpdateTop(v), true
	case DeleteSecretsMsg:
		return m, m.deleteSecretsCmd(v.Provider), true
	}
	return m, nil, false
}

// repaint swaps the active palette and re-applies it everywhere a
// Style got copied at construction time. Called by all three settings
// flows (preview, apply, cancel); only apply also closes the overlay
// and saves. Also called from the tea.BackgroundColorMsg handler when
// Auto mode needs to flip dark↔light.
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

// createSession spawns a new tab. If v.AgentSlug is empty it falls
// back to the model's default agent.
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
// returns the next read tea.Cmd. Drops events from streams the
// session has already replaced (e.g. a stale read landing after
// Cancel + a fresh turn started). Persists the transcript on the
// terminal event.
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
// closed cleanly without a Done event (rare — usually means context
// was cancelled mid-turn), fall the session back to Idle so the
// textarea unlocks.
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
