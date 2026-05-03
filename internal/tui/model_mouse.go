package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// inChatRegion reports whether (x, y) in screen coords lies inside the chat
// list. Used to gate mouse events at the parent before forwarding into the
// chatModel — keeps drag-to-select from firing on the sidebar / textarea.
// The chat content sits at x=mainPadLeft (the left gutter) and runs for
// the inner main width; sidebar + gap occupy the rest.
func (m Model) inChatRegion(x, y int) bool {
	mainW := m.width - sidebarWidth - sidebarGap - mainPadLeft
	if x < mainPadLeft || x >= mainPadLeft+mainW {
		return false
	}
	if y < headerHeight {
		return false
	}
	if y >= headerHeight+m.chat.Height() {
		return false
	}
	return true
}

// screenToChat maps screen coords into the chat list's local coords (origin
// at the chat's top-left). Returns negative values when outside the chat,
// callers may clamp as needed for drag-past-edges behavior. The chat starts
// at x=mainPadLeft (the left gutter), so we subtract the gutter to get the
// chat-local x.
func (m Model) screenToChat(x, y int) (int, int) {
	return x - mainPadLeft, y - headerHeight
}

// updateMouse routes mouse events into the chatModel's drag-to-select state
// machine. Wheel scrolls; click/motion/release drive selection. Returns
// handled=true only for genuine MouseMsg values — other messages pass through.
func (m Model) updateMouse(msg tea.Msg) (Model, tea.Cmd, bool) {
	if mm, isWheel := msg.(tea.MouseWheelMsg); isWheel {
		// Horizontal wheel: drop. We don't support horizontal scroll in
		// the chat — the list does its own width-aware wrapping.
		if mm.Button == tea.MouseWheelLeft || mm.Button == tea.MouseWheelRight {
			return m, nil, true
		}
		if mm.Mod.Contains(tea.ModShift) {
			return m, nil, true
		}
		// Dialog open? Forward so its scrollable content can move.
		if m.overlay.HasOpen() {
			return m, m.overlay.UpdateTop(mm), true
		}
		// Vertical wheel on the chat: scroll a few lines per tick.
		step := 3
		if mm.Button == tea.MouseWheelUp {
			m.chat.ScrollBy(-step)
		} else if mm.Button == tea.MouseWheelDown {
			m.chat.ScrollBy(step)
		}
		return m, nil, true
	}
	mm, ok := msg.(tea.MouseMsg)
	if !ok {
		return m, nil, false
	}
	// Overlays never get app-level drag-to-select.
	if m.overlay.HasOpen() {
		return m, nil, true
	}
	e := mm.Mouse()
	cx, cy := m.screenToChat(e.X, e.Y)
	switch ev := mm.(type) {
	case tea.MouseClickMsg:
		if ev.Button != tea.MouseLeft {
			return m, nil, true
		}
		if !m.inChatRegion(e.X, e.Y) {
			m.chat.ClearMouse()
			return m, nil, true
		}
		_, cmd := m.chat.HandleMouseDown(cx, cy)
		return m, cmd, true
	case tea.MouseMotionMsg:
		// Drag past the visible area: chatModel clamps to first/last.
		m.chat.HandleMouseDrag(cx, cy)
		return m, nil, true
	case tea.MouseReleaseMsg:
		if m.chat.HandleMouseUp(cx, cy) {
			if m.chat.HasHighlight() {
				if text := m.chat.CopySelection(); text != "" {
					m.logger.Info("clipboard write", "len", len(text))
				}
			}
		}
		return m, nil, true
	}
	return m, nil, true
}

// mouseEventFilter throttles high-frequency mouse motion / wheel events to at
// most one per 15ms. Crush uses the same trick to keep heavy scroll-wheel
// activity from saturating the program loop.
var lastMouseEvent time.Time

func mouseEventFilter(_ tea.Model, msg tea.Msg) tea.Msg {
	switch msg.(type) {
	case tea.MouseWheelMsg, tea.MouseMotionMsg:
		now := time.Now()
		if now.Sub(lastMouseEvent) < 15*time.Millisecond {
			return nil
		}
		lastMouseEvent = now
	}
	return msg
}
