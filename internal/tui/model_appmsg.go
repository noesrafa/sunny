package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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
		cur.RemoteID = ""
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
	}
	return m, nil, false
}

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
	// Save current draft before switching to the new session.
	if cur := m.manager.Current(); cur != nil {
		cur.Draft = m.textarea.Value()
	}
	s, err := session.New(m.ctx, v.Cwd, session.Options{
		Logger:                   m.logger,
		Model:                    v.Model,
		Effort:                   v.Effort,
		DangerousSkipPermissions: m.skipPerms,
	})
	if err != nil {
		m.lastErr = err
		m.logger.Error("create session failed", "err", err, "cwd", v.Cwd)
		return m, nil, true
	}
	m.manager.Add(s)
	m.textarea.Reset() // new session starts with empty draft
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
