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
	"github.com/noesrafa/sunny/internal/terminal"
)

// updateKey is the tea.KeyMsg dispatcher: master shortcuts → pane-forwarding
// → claude-session keys. Returns handled=true when the key was consumed; when
// false, the dispatcher falls through to textarea/viewport so character input
// reaches the editor.
func (m Model) updateKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	// MASTER shortcuts always take precedence — even when a pane is
	// active they're how the user navigates back out / opens dialogs.
	switch {
	case key.Matches(msg, m.keymap.Quit):
		return m, m.openQuitDialog(), true
	case key.Matches(msg, m.keymap.TilePicker):
		return m, m.overlay.Open(NewTilePickerDialog(m.collectTiles(), m.styles)), true
	case key.Matches(msg, m.keymap.Settings):
		return m, m.overlay.Open(NewSettingsDialog(m.themeID, m.bgIsLight, m.styles)), true
	case key.Matches(msg, m.keymap.Game):
		return m, m.overlay.Open(NewMinigameDialog(m.styles)), true
	case key.Matches(msg, m.keymap.NewSession):
		return m, m.overlay.Open(NewNewSessionDialog(m.initialCwd, m.defaultModel, m.defaultEffort, m.styles)), true
	case key.Matches(msg, m.keymap.Runs):
		if m.runs == nil {
			return m, nil, true
		}
		return m, tea.Batch(m.overlay.Open(NewRunsDialog(m.runs, m.styles)), m.spinner.Tick), true
	case key.Matches(msg, m.keymap.NextSession):
		m.cycleTab(1)
		return m, nil, true
	case key.Matches(msg, m.keymap.PrevSession):
		m.cycleTab(-1)
		return m, nil, true
	case key.Matches(msg, m.keymap.CloseSession):
		// Handled here (above the pane-forward block) so ctrl+w closes
		// the active terminal pane instead of getting eaten by the shell
		// inside it as a "delete word" keystroke. Panes close immediately;
		// claude sessions go through a confirmation dialog so the user
		// doesn't lose the transcript on a typo.
		if m.activeKind == activePane {
			m.handleCloseTab()
			return m, nil, true
		}
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
	}

	// If a terminal pane has focus, all other keys are forwarded to its
	// PTY (translated to xterm bytes). The pane-only Esc/Ctrl+W still
	// fall through master switch above.
	if p := m.activePane(); p != nil {
		if pk, ok := msg.(tea.KeyPressMsg); ok {
			if b := terminal.KeyToBytes(pk); b != nil {
				if err := p.Send(b); err != nil {
					m.logger.Debug("pane send failed", "err", err, "pane", p.ID)
				}
			}
		}
		return m, nil, true
	}

	// Below: claude-session-only keys.
	switch {
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
				"¿Empezar una nueva conversación de claude?",
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
	}
	return m, nil, false
}

// handlePaste runs the image-aware paste flow: image clipboard first
// (saves + drops "[Image #N]" marker), then falls back to plain text via
// atotto when there's no image. Triggered by Ctrl+V — terminals don't
// forward Cmd+V as bracketed paste when the clipboard is image-only.
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

// handleClearOrCancel: ctrl+c cancels the in-flight claude turn (SIGINT)
// without touching the textarea draft or attachments — the user pressed
// it because they changed their mind about the prompt and wants to type
// a new one. When the session is idle, ctrl+c is a no-op (the user has
// to clear the textarea explicitly to avoid losing typed text by mistake).
func (m *Model) handleClearOrCancel() {
	cur := m.manager.Current()
	if cur == nil || cur.State != session.StateThinking {
		return
	}
	if err := cur.Cancel(); err != nil {
		cur.LastErr = err
	}
}

func (m *Model) handleCloseTab() {
	if m.activeKind == activePane && m.panes != nil {
		if p := m.panes.Current(); p != nil {
			m.panes.Close(p.ID)
			if m.panes.Len() == 0 {
				m.activeKind = activeClaude
			}
		}
		m.saveState()
		return
	}
	cur := m.manager.Current()
	if cur != nil {
		m.manager.Close(cur.ID)
		if next := m.manager.Current(); next != nil {
			m.textarea.SetValue(next.Draft)
			m.textarea.CursorEnd()
		} else {
			m.textarea.Reset()
		}
		m.layout()
		m.refreshViewport()
	}
	m.saveState()
}

// syncAttachmentMarkers reconciles the textarea against pending
// attachments. If the user damaged a marker (e.g., backspaced over the
// closing bracket of "[Image #2]"), we drop that attachment from the
// pending list, scrub any orphan fragment from the textarea, and delete
// the file from disk — it has never been sent and will never be sent.
//
// Called after every textarea-mutating keystroke. Cheap: O(pending
// attachments × textarea length).
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
		// Marker broken. Strip whatever fragment of it is still hanging
		// around so the textarea doesn't show "[Image #2" forever, then
		// delete the unused file.
		fragment := regexp.MustCompile(fmt.Sprintf(`\[?Image\s*#?\s*%d\b\]?`, a.Index))
		cleaned = fragment.ReplaceAllString(cleaned, "")
		if err := os.Remove(a.Path); err != nil && !os.IsNotExist(err) {
			m.logger.Debug("remove orphan image", "path", a.Path, "err", err)
		}
		m.logger.Info("attachment dropped",
			"session", cur.ID,
			"idx", a.Index,
			"path", a.Path,
		)
	}
	cur.Attachments = kept
	if cleaned != text {
		m.textarea.SetValue(cleaned)
		m.textarea.CursorEnd()
		cur.Draft = cleaned
	}
}

// tryImagePaste peeks at the system clipboard for image data. If found, it
// saves the bytes under ~/.sunnytui/images/, registers an attachment on
// the active session, and inserts the matching "[Image #N]" marker at the
// textarea cursor. Any text that came along in the bracketed paste is
// inserted right after the marker so users don't lose accompanying text.
//
// Returns true when the message has been fully handled (the caller MUST
// skip forwarding the original PasteMsg to the textarea, or the binary
// junk will land as garbled glyphs).
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
	m.logger.Info("image pasted",
		"session", cur.ID,
		"idx", idx,
		"path", path,
		"bytes", len(data),
	)
	return true
}

func (m Model) handleSend() (Model, tea.Cmd) {
	cur := m.manager.Current()
	if cur == nil || cur.State != session.StateIdle {
		return m, nil
	}
	value := m.textarea.Value()
	// Crush pattern: trailing backslash escapes Enter to a newline (so users
	// can compose multiline messages without Shift+Enter).
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

	// If the previous stream died (typically because the user hit ctrl+c
	// during a turn and claude exited instead of cleanly aborting), the
	// session lives on in the manager with Stream == nil. Spawn a fresh
	// claude --resume <session_id> so the conversation continues without
	// the user having to recreate the tab.
	var resumed tea.Cmd
	if cur.Stream == nil {
		if err := cur.Resume(m.ctx, m.skipPerms); err != nil {
			cur.LastErr = err
			cur.State = session.StateError
			m.logger.Error("session resume failed", "err", err, "session", cur.ID)
			m.refreshViewport()
			return m, nil
		}
		// Re-arm the events reader against the new stream so its emitted
		// events (and eventual close) get routed back into Update.
		resumed = waitForSession(cur)
	}

	m.textarea.Reset()
	cur.Draft = ""
	if err := cur.Send(text); err != nil {
		cur.LastErr = err
	}
	m.layout()
	m.refreshViewport()
	m.chat.ScrollToBottom()
	// Kick off the spinner tick chain and the morphing-string anim — both
	// die when the session goes idle. The logo tick lives independently
	// (always running) so we don't need to resurrect it here.
	return m, tea.Batch(resumed, m.spinner.Tick, m.thinkingAnim.Step())
}
