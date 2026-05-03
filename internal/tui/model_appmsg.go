package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noesrafa/sunny/internal/session"
	"github.com/noesrafa/sunny/internal/terminal"
)

// updateAppMsg handles in-app messages emitted by dialogs and other
// sub-models (close/quit/run/pane/session creation, etc.). It owns its
// messages — when handled is true the caller MUST return immediately.
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
		m.overlay.CloseTop()
		cur := m.manager.Current()
		if cur == nil {
			return m, nil, true
		}
		if err := cur.Reset(m.ctx, m.skipPerms); err != nil {
			cur.LastErr = err
			m.logger.Error("session reset failed", "session", cur.ID, "err", err)
			return m, nil, true
		}
		m.textarea.Reset()
		m.layout()
		m.refreshViewport()
		m.chat.ScrollToBottom()
		m.saveState()
		return m, waitForSession(cur), true
	case OpenRunEditMsg:
		cwd := m.initialCwd
		if cur := m.manager.Current(); cur != nil {
			cwd = cur.Cwd
		}
		return m, m.overlay.Open(NewRunEditDialog(cwd, m.styles)), true
	case OpenRunLogsMsg:
		if m.runs != nil {
			if r := m.runs.Get(v.ID); r != nil {
				return m, m.overlay.Open(NewRunLogsDialog(r, m.styles)), true
			}
		}
		return m, nil, true
	case CreateRunMsg:
		m.overlay.CloseTop()
		if m.runs == nil {
			return m, nil, true
		}
		r := m.runs.Add(v.Name, v.Command, v.Cwd)
		if err := m.runs.Save(); err != nil {
			m.logger.Warn("save runs", "err", err)
		}
		m.logger.Info("run created", "id", r.ID, "name", r.Name)
		// Reopen the runs list so the user can act on the new entry.
		return m, m.overlay.Open(NewRunsDialog(m.runs, m.styles)), true
	case DeleteRunMsg:
		if m.runs != nil {
			m.runs.Remove(v.ID)
			if err := m.runs.Save(); err != nil {
				m.logger.Warn("save runs", "err", err)
			}
		}
		return m, nil, true
	case CreatePaneMsg:
		return m.createPane(v)
	case ClosePaneMsg:
		if m.panes != nil {
			m.panes.Close(v.ID)
			if m.panes.Len() == 0 {
				m.activeKind = activeClaude
			}
			m.saveState()
		}
		return m, nil, true
	case SwitchTabMsg:
		m.overlay.CloseTop()
		m.switchToTab(v.Kind, v.Index)
		return m, nil, true
	case paneOutputMsg:
		// vt10x already received the bytes inside ReadOnce; just re-arm the
		// reader so the next chunk gets pumped.
		if m.panes != nil {
			if p := m.panes.ByID(v.PaneID); p != nil {
				return m, readPaneCmd(p), true
			}
		}
		return m, nil, true
	case paneClosedMsg:
		// Process exited (clean exit, crash, or user pressed ctrl+c). Keep
		// the pane in the slice so the user can still read whatever the
		// process printed before dying — only ctrl+w removes it. The
		// sidebar already shows the dead state via "□".
		if v.Err != nil {
			m.logger.Debug("pane exited", "id", v.PaneID, "err", v.Err)
		}
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
//
// The id passed in is the *user-facing* selection (may be AutoThemeID).
// We resolve it through ResolveTheme so Auto picks the right concrete
// palette based on the most recent terminal-bg reading, but persist the
// original id so the user's "follow terminal" choice survives restarts.
//
//   - m.styles is fully rebuilt from the new palette globals.
//   - The textarea owns its own Styles struct; without re-applying it,
//     the cursor color / placeholder color / prompt color stick to the
//     previous theme until a restart.
//   - The spinner stores a Style on its struct, so it's re-set explicitly.
//   - The morphing-thinking anim stores its gradient colors as fields;
//     SetColors swaps them without resetting the morph state.
//   - Glamour bakes ANSI codes into its output; m.md + m.mdCache are
//     thrown away so transcripts re-render against the new palette.
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

	// Open dialogs (notably SettingsDialog mid-preview) cache a Styles
	// copy at construction. Without propagating the rebuilt palette they
	// keep painting in the previous theme until the user closes & reopens
	// them — that's the "I had to cycle several themes" symptom.
	m.overlay.RefreshStyles(m.styles)
	m.overlay.RefreshBgIsLight(m.bgIsLight)

	m.refreshViewport()
}

func (m Model) createPane(v CreatePaneMsg) (Model, tea.Cmd, bool) {
	m.overlay.CloseTop()
	if m.panes == nil {
		m.panes = terminal.NewManager()
	}
	mainW, mainH := m.paneSize()
	p, err := terminal.Spawn(v.Name, v.Command, v.Cwd, mainW, mainH)
	if err != nil {
		m.lastErr = err
		m.logger.Error("spawn pane failed", "err", err, "cmd", v.Command)
		return m, nil, true
	}
	m.panes.Add(p)
	m.activeKind = activePane
	m.saveState()
	m.logger.Info("pane spawned", "id", p.ID, "name", p.Title, "cmd", p.Command)
	return m, readPaneCmd(p), true
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
	return m, waitForSession(s), true
}

func (m *Model) switchToTab(kind string, index int) {
	switch kind {
	case "claude":
		m.activeKind = activeClaude
		m.manager.Active = index
		if cur := m.manager.Current(); cur != nil {
			m.textarea.SetValue(cur.Draft)
			m.textarea.CursorEnd()
			m.layout()
			m.refreshViewport()
			m.chat.ScrollToBottom()
		}
	case "pane":
		if m.panes != nil {
			m.activeKind = activePane
			m.panes.SetActive(index)
			if p := m.activePane(); p != nil {
				w, h := m.paneSize()
				p.Resize(w, h)
			}
		}
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
