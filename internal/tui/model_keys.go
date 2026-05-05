package tui

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"

	imgclip "github.com/noesrafa/sunny/internal/clipboard"
	"github.com/noesrafa/sunny/internal/session"
)

// updateKey is the tea.KeyMsg dispatcher: master shortcuts → session keys.
// Returns handled=true when the key was consumed; when false, the
// dispatcher falls through to textarea/viewport so character input
// reaches the editor.
func (m Model) updateKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	switch {
	case key.Matches(msg, m.keymap.Quit):
		return m, m.openQuitDialog(), true
	case key.Matches(msg, m.keymap.TilePicker):
		return m, m.overlay.Open(NewTilePickerDialog(m.collectTiles(), m.styles)), true
	case key.Matches(msg, m.keymap.Agents):
		curSlug, curHost := "", ""
		if cur := m.manager.Current(); cur != nil {
			curSlug = cur.AgentSlug()
			curHost = cur.Host()
		}
		return m, m.overlay.Open(NewAgentPickerDialog(m.fed, curSlug, curHost, m.styles)), true
	case key.Matches(msg, m.keymap.Secrets):
		return m, m.overlay.Open(NewSecretsDialog(m.client, m.styles)), true
	case key.Matches(msg, m.keymap.Settings):
		return m, m.overlay.Open(NewSettingsDialog(m.themeID, m.bgIsLight, m.styles)), true
	case key.Matches(msg, m.keymap.Runs):
		return m, m.overlay.Open(NewRunManagerDialog(m.activePeer, m.runsForActivePeer(), m.styles)), true
	case key.Matches(msg, m.keymap.Monitors):
		return m, m.overlay.Open(NewMonitorManagerDialog(m.activePeer, m.allMonitorsForActivePeer(), m.styles)), true
	case key.Matches(msg, m.keymap.NewSession):
		curAgent := m.defaultAgent
		if cur := m.manager.Current(); cur != nil && cur.AgentSlug() != "" {
			curAgent = cur.AgentSlug()
		}
		// New session targets the active peer. For local we keep
		// initialCwd as a sensible starting point in the dir picker;
		// for remote we leave it empty so the daemon returns its own
		// home dir (the TUI's local cwd is meaningless there).
		peerClient := m.clientFor(m.activePeer)
		defaultCwd := ""
		if m.activePeer == "local" {
			defaultCwd = m.initialCwd
		}
		return m, m.overlay.Open(NewNewSessionDialog(peerClient, m.activePeer, defaultCwd, curAgent, m.styles)), true
	case key.Matches(msg, m.keymap.NextSession):
		m.cycleTab(1)
		return m, nil, true
	case key.Matches(msg, m.keymap.PrevSession):
		m.cycleTab(-1)
		return m, nil, true
	case key.Matches(msg, m.keymap.CloseSession):
		if cur := m.manager.Current(); cur != nil {
			body := []string{"¿Cerrar la sesión \"" + cur.Title + "\"?"}
			if cur.State == session.StateThinking {
				body = append(body, "")
				body = append(body, "⚠ la sesión está pensando — el turno se cancelará")
			}
			d := NewConfirmDialog(m.styles, "Cerrar sesión", body, ConfirmCloseSessionMsg{})
			return m, m.overlay.Open(d), true
		}
		return m, nil, true
	case key.Matches(msg, m.keymap.ClearOrCancel):
		m.handleClearOrCancel()
		return m, nil, true
	case key.Matches(msg, m.keymap.Diff):
		cwd, branch := "", ""
		var changes session.ChangeStats
		if cur := m.manager.Current(); cur != nil {
			cwd, branch, changes = cur.Cwd, cur.Branch, cur.Changes
		} else {
			cwd = m.initialCwd
		}
		return m, m.overlay.Open(NewDiffDialog(cwd, branch, changes, m.styles)), true
	case key.Matches(msg, m.keymap.Rename):
		if cur := m.manager.Current(); cur != nil {
			d := NewRenameDialog(cur.Title, m.styles)
			return m, m.overlay.Open(d), true
		}
		return m, nil, true
	case key.Matches(msg, m.keymap.NewConv):
		if cur := m.manager.Current(); cur != nil {
			body := []string{
				"¿Empezar una nueva conversación?",
				"",
				"Se mantendrá la pestaña (" + cur.Title + ") en " + prettyPath(cur.Cwd) + ",",
				"pero el transcript actual se va a descartar.",
			}
			d := NewConfirmDialog(m.styles, "Nueva conversación", body, ConfirmNewConvMsg{})
			return m, m.overlay.Open(d), true
		}
		return m, nil, true
	case key.Matches(msg, m.keymap.Send):
		next, cmd := m.handleSend()
		return next, cmd, true
	case key.Matches(msg, m.keymap.Paste):
		m.handlePaste()
		return m, nil, true
	case key.Matches(msg, m.keymap.Help):
		return m, m.overlay.Open(NewHelpDialog(m.styles)), true
	case key.Matches(msg, m.keymap.Peer1):
		m.switchToPeerByIdx(0)
		return m, nil, true
	case key.Matches(msg, m.keymap.Peer2):
		m.switchToPeerByIdx(1)
		return m, nil, true
	case key.Matches(msg, m.keymap.Peer3):
		m.switchToPeerByIdx(2)
		return m, nil, true
	case key.Matches(msg, m.keymap.Peer4):
		m.switchToPeerByIdx(3)
		return m, nil, true
	case key.Matches(msg, m.keymap.Peer5):
		m.switchToPeerByIdx(4)
		return m, nil, true
	case key.Matches(msg, m.keymap.Peer6):
		m.switchToPeerByIdx(5)
		return m, nil, true
	case key.Matches(msg, m.keymap.Peer7):
		m.switchToPeerByIdx(6)
		return m, nil, true
	case key.Matches(msg, m.keymap.Peer8):
		m.switchToPeerByIdx(7)
		return m, nil, true
	case key.Matches(msg, m.keymap.Peer9):
		m.switchToPeerByIdx(8)
		return m, nil, true
	}
	return m, nil, false
}

// handlePaste runs the image-aware paste flow: image clipboard first, then
// falls back to plain text via atotto when there's no image.
func (m *Model) handlePaste() {
	if m.tryImagePaste("") {
		return
	}
	text, err := clipboard.ReadAll()
	if err != nil {
		m.logger.Debug("clipboard text read", "err", err)
		return
	}
	if text == "" {
		return
	}
	m.textarea.InsertString(text)
	if cur := m.manager.Current(); cur != nil {
		cur.Draft = m.textarea.Value()
	}
	m.layout()
}

// handleClearOrCancel: ctrl+c cancels the in-flight turn without touching
// the textarea draft. When the session is idle, ctrl+c is a no-op.
func (m *Model) handleClearOrCancel() {
	cur := m.manager.Current()
	if cur == nil || cur.State != session.StateThinking {
		return
	}
	if err := cur.Cancel(m.ctx); err != nil {
		cur.LastErr = err
	}
}

func (m *Model) handleCloseTab() {
	cur := m.manager.Current()
	if cur == nil {
		return
	}
	// Tell the daemon to drop the tab. This publishes a tab.closed
	// event so other viewers also remove it. Idempotent — we don't
	// surface errors because the user just expects the tab to go.
	if cur.TabID != "" {
		if c := m.clientFor(cur.Host()); c != nil {
			if err := c.CloseTab(m.ctx, cur.TabID); err != nil {
				m.logger.Warn("close tab", "tab", cur.TabID, "err", err)
			}
		}
	}
	m.manager.Close(cur.ID)
	if next := m.manager.Current(); next != nil {
		m.textarea.SetValue(next.Draft)
		m.textarea.CursorEnd()
	} else {
		m.textarea.Reset()
	}
	m.layout()
	m.refreshViewport()
	m.saveState()
}

// syncAttachmentMarkers reconciles the textarea against pending attachments.
// If the user damaged a marker, we drop that attachment + clean the text.
func (m *Model) syncAttachmentMarkers() {
	cur := m.manager.Current()
	if cur == nil || len(cur.Attachments) == 0 {
		return
	}
	text := m.textarea.Value()
	cleaned := text
	kept := cur.Attachments[:0]
	for _, a := range cur.Attachments {
		marker := fmt.Sprintf("[Image #%d]", a.Index)
		if strings.Contains(cleaned, marker) {
			kept = append(kept, a)
			continue
		}
		fragment := regexp.MustCompile(fmt.Sprintf(`\[?Image\s*#?\s*%d\b\]?`, a.Index))
		cleaned = fragment.ReplaceAllString(cleaned, "")
		if err := os.Remove(a.Path); err != nil && !os.IsNotExist(err) {
			m.logger.Debug("remove orphan image", "path", a.Path, "err", err)
		}
		m.logger.Info("attachment dropped", "session", cur.ID, "idx", a.Index)
	}
	cur.Attachments = kept
	if cleaned != text {
		m.textarea.SetValue(cleaned)
		m.textarea.CursorEnd()
		cur.Draft = cleaned
	}
}

// tryImagePaste peeks at the system clipboard for image data. If found, it
// saves the bytes, registers an attachment, and inserts a marker.
func (m *Model) tryImagePaste(text string) bool {
	cur := m.manager.Current()
	if cur == nil {
		return false
	}
	data, mediaType, ok, err := imgclip.ReadImage()
	if err != nil {
		m.logger.Debug("clipboard image read", "err", err)
	}
	if !ok {
		return false
	}
	path, err := imgclip.SaveImage(data, mediaType)
	if err != nil {
		m.logger.Warn("save attachment", "err", err)
		return false
	}
	idx := cur.AddAttachment(path, mediaType)
	marker := fmt.Sprintf("[Image #%d]", idx)
	insert := marker
	if text != "" {
		insert = marker + " " + text
	}
	m.textarea.InsertString(insert)
	cur.Draft = m.textarea.Value()
	m.layout()
	m.logger.Info("image pasted", "session", cur.ID, "idx", idx, "path", path, "bytes", len(data))
	return true
}

func (m Model) handleSend() (Model, tea.Cmd) {
	cur := m.manager.Current()
	if cur == nil || cur.State != session.StateIdle {
		return m, nil
	}
	value := m.textarea.Value()
	// Trailing backslash escapes Enter to a newline.
	if before, ok := strings.CutSuffix(value, "\\"); ok {
		m.textarea.SetValue(before + "\n")
		m.textarea.CursorEnd()
		m.layout()
		return m, nil
	}
	text := strings.TrimSpace(value)
	if text == "" {
		return m, nil
	}

	m.textarea.Reset()
	cur.Draft = ""

	if err := cur.Send(m.ctx, text); err != nil {
		cur.LastErr = err
		cur.State = session.StateError
		m.logger.Error("send failed", "err", err, "session", cur.ID)
		m.layout()
		m.refreshViewport()
		m.chat.ScrollToBottom()
		return m, nil
	}

	m.layout()
	m.refreshViewport()
	m.chat.ScrollToBottom()
	// Spinner + thinking animation kick on while the engine works;
	// the watch goroutine is already pumping events into the session,
	// so no additional wait command is needed here.
	return m, tea.Batch(
		m.spinner.Tick,
		m.thinkingAnim.Step(),
	)
}
