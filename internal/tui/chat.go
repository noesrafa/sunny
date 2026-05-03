package tui

import (
	"image"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"

	"github.com/noesrafa/sunny/internal/list"
)

// Multi-click thresholds — match Crush's defaults.
const (
	chatDoubleClickThreshold = 400 * time.Millisecond
	chatClickTolerance       = 2
)

// DelayedClickMsg fires after the double-click window so a single click can
// run its action only if no second click superseded it. ClickID lets us tell
// stale ticks apart from the current pending click.
type DelayedClickMsg struct {
	ClickID int
	ItemIdx int
	X, Y    int
}

// chatModel is the chat viewport. Wraps a list.List, owns the mouse state
// machine for drag-to-select, and exposes a thin API the parent Model talks
// to. Lifted from Crush's internal/ui/model/chat.go and slimmed for sunnytui's
// needs (no per-item key dispatch, no animation pause book-keeping).
type chatModel struct {
	list *list.List

	// Mouse state. mouseDownItem == -1 means there is no active drag.
	mouseDown     bool
	mouseDownItem int
	mouseDownX    int
	mouseDownY    int
	mouseDragItem int
	mouseDragX    int
	mouseDragY    int

	// Multi-click tracking — each consecutive click within the threshold +
	// tolerance bumps clickCount. Pending single-click actions are gated by
	// pendingClickID so a fast double-click invalidates the deferred action.
	lastClickTime  time.Time
	lastClickX     int
	lastClickY     int
	clickCount     int
	pendingClickID int

	// follow auto-anchors the viewport to the bottom when new items arrive.
	// Toggled off when the user scrolls up by hand.
	follow bool
}

func newChatModel() *chatModel {
	c := &chatModel{
		mouseDownItem: -1,
		mouseDragItem: -1,
		follow:        true,
	}
	l := list.NewList()
	l.SetGap(1)
	l.RegisterRenderCallback(c.applyHighlightRange)
	c.list = l
	return c
}

// SetSize forwards to the underlying list and re-anchors to the bottom if we
// were already there (typing-time resize shouldn't drop the user mid-history).
func (c *chatModel) SetSize(width, height int) {
	wasBottom := c.list.AtBottom()
	c.list.SetSize(width, height)
	if wasBottom {
		c.list.ScrollToBottom()
	}
}

func (c *chatModel) Width() int  { return c.list.Width() }
func (c *chatModel) Height() int { return c.list.Height() }

// SetItems replaces the list contents without touching the scroll offset.
// list.setItems preserves offsetIdx + offsetLine when they're still valid,
// so a streaming delta won't yank the user mid-read. Callers that want to
// re-anchor to the bottom (turn complete, user just sent, end key) call
// ScrollToBottom explicitly afterwards.
func (c *chatModel) SetItems(items []list.Item) {
	c.list.SetItems(items...)
}

// Render returns the visible slice of the chat list.
func (c *chatModel) Render() string {
	return c.list.Render()
}

// AtBottom proxies to list.AtBottom.
func (c *chatModel) AtBottom() bool { return c.list.AtBottom() }

// ScrollToBottom anchors the list to the bottom and re-enables follow.
func (c *chatModel) ScrollToBottom() {
	c.list.ScrollToBottom()
	c.follow = true
}

// ScrollToTop jumps to the start of the transcript and disables follow so
// the user can read history without streaming yanking them back.
func (c *chatModel) ScrollToTop() {
	c.list.ScrollToTop()
	c.follow = false
}

// ScrollBy scrolls by a number of lines (positive = down). Disables follow
// when the user scrolls up so streaming doesn't yank them back to the bottom.
func (c *chatModel) ScrollBy(lines int) {
	c.list.ScrollBy(lines)
	if lines < 0 {
		c.follow = false
	} else if c.list.AtBottom() {
		c.follow = true
	}
}

// PageUp / PageDown scroll by the visible viewport height.
func (c *chatModel) PageUp()   { c.ScrollBy(-c.list.Height()) }
func (c *chatModel) PageDown() { c.ScrollBy(c.list.Height()) }

// HandleMouseDown starts a drag-to-select on the chat. Returns whether the
// click was consumed and an optional Cmd for the delayed single-click action
// (kept for future use; sunnytui doesn't expand items today).
func (c *chatModel) HandleMouseDown(x, y int) (bool, tea.Cmd) {
	if c.list.Len() == 0 {
		return false, nil
	}

	itemIdx, itemY := c.list.ItemIndexAtPosition(x, y)
	if itemIdx < 0 {
		return false, nil
	}

	c.pendingClickID++
	clickID := c.pendingClickID

	now := time.Now()
	if now.Sub(c.lastClickTime) <= chatDoubleClickThreshold &&
		absInt(x-c.lastClickX) <= chatClickTolerance &&
		absInt(y-c.lastClickY) <= chatClickTolerance {
		c.clickCount++
	} else {
		c.clickCount = 1
	}
	c.lastClickTime = now
	c.lastClickX = x
	c.lastClickY = y

	c.list.SetSelected(itemIdx)

	var cmd tea.Cmd
	switch c.clickCount {
	case 1:
		c.mouseDown = true
		c.mouseDownItem = itemIdx
		c.mouseDownX = x
		c.mouseDownY = itemY
		c.mouseDragItem = itemIdx
		c.mouseDragX = x
		c.mouseDragY = itemY
		cmd = tea.Tick(chatDoubleClickThreshold, func(time.Time) tea.Msg {
			return DelayedClickMsg{ClickID: clickID, ItemIdx: itemIdx, X: x, Y: itemY}
		})
	case 2:
		c.selectWord(itemIdx, x, itemY)
	case 3:
		c.selectLine(itemIdx, itemY)
		c.clickCount = 0
	}
	return true, cmd
}

// HandleMouseDrag extends the active selection. No-op if the mouse isn't down.
func (c *chatModel) HandleMouseDrag(x, y int) bool {
	if !c.mouseDown || c.list.Len() == 0 {
		return false
	}
	itemIdx, itemY := c.list.ItemIndexAtPosition(x, y)
	if itemIdx < 0 {
		// Allow drag past the visible area — clamp to first/last visible.
		startIdx, endIdx := c.list.VisibleItemIndices()
		if y < 0 {
			itemIdx = startIdx
			itemY = 0
		} else {
			itemIdx = endIdx
			if endItem := c.list.ItemAt(endIdx); endItem != nil {
				if rr, ok := endItem.(list.RawRenderable); ok {
					itemY = lipgloss.Height(rr.RawRender(c.list.Width())) - 1
				} else {
					itemY = lipgloss.Height(endItem.Render(c.list.Width())) - 1
				}
			}
		}
	}
	c.mouseDragItem = itemIdx
	c.mouseDragX = x
	c.mouseDragY = itemY
	return true
}

// HandleMouseUp finalizes the drag — the parent Model checks HasHighlight()
// afterwards to copy the selection to the clipboard.
func (c *chatModel) HandleMouseUp(x, y int) bool {
	if !c.mouseDown {
		return false
	}
	c.mouseDown = false
	return true
}

// HasHighlight reports whether the current drag state defines a non-empty
// selection range.
func (c *chatModel) HasHighlight() bool {
	si, sl, sc, ei, el, ec := c.getHighlightRange()
	if si < 0 || ei < 0 {
		return false
	}
	return si != ei || sl != el || sc != ec
}

// HighlightContent walks every item in the selection range and concatenates
// the raw text under the highlight overlay. Returns plain text suitable for
// the clipboard.
func (c *chatModel) HighlightContent() string {
	si, sl, sc, ei, el, ec := c.getHighlightRange()
	if si < 0 || ei < 0 || (si == ei && sl == el && sc == ec) {
		return ""
	}
	var sb strings.Builder
	for i := si; i <= ei; i++ {
		item := c.list.ItemAt(i)
		if item == nil {
			continue
		}
		hl, ok := item.(list.Highlightable)
		if !ok {
			continue
		}
		startLine, startCol, endLine, endCol := hl.Highlight()
		if startLine < 0 && endLine < 0 {
			continue
		}
		w := c.list.Width()
		var rendered string
		if rr, ok := item.(list.RawRenderable); ok {
			rendered = rr.RawRender(w)
		} else {
			rendered = item.Render(w)
		}
		// HighlightContent walks rendered cells; the overlay style is just
		// inverse-video, the cell content is unchanged so we get plain text.
		innerW := w - MessageLeftPadding
		if innerW <= 0 {
			innerW = 1
		}
		text := list.HighlightContent(
			rendered,
			image.Rect(0, 0, innerW, lipgloss.Height(rendered)),
			startLine, startCol, endLine, endCol,
		)
		if text != "" {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(text)
		}
	}
	return strings.TrimSpace(sb.String())
}

// CopySelection writes HighlightContent to the system clipboard, returning
// the text so callers can log how much they shipped.
func (c *chatModel) CopySelection() string {
	text := c.HighlightContent()
	if text == "" {
		return ""
	}
	_ = clipboard.WriteAll(text)
	return text
}

// ClearMouse drops drag state without otherwise affecting the list — used
// when the user clicks outside the chat or hits a key that should cancel.
func (c *chatModel) ClearMouse() {
	c.mouseDown = false
	c.mouseDownItem = -1
	c.mouseDragItem = -1
	c.lastClickTime = time.Time{}
	c.lastClickX = 0
	c.lastClickY = 0
	c.clickCount = 0
	c.pendingClickID++
}

// applyHighlightRange is the list RenderCallback that translates the global
// highlight range (across items) into per-item SetHighlight calls. Items
// outside the range get cleared (-1) so a previous selection doesn't linger.
func (c *chatModel) applyHighlightRange(idx, _ int, item list.Item) list.Item {
	hi, ok := item.(list.Highlightable)
	if !ok {
		return item
	}
	startItemIdx, startLine, startCol, endItemIdx, endLine, endCol := c.getHighlightRange()
	sLine, sCol, eLine, eCol := -1, -1, -1, -1
	if startItemIdx >= 0 && idx >= startItemIdx && idx <= endItemIdx {
		switch {
		case idx == startItemIdx && idx == endItemIdx:
			sLine, sCol, eLine, eCol = startLine, startCol, endLine, endCol
		case idx == startItemIdx:
			sLine, sCol = startLine, startCol
			eLine, eCol = -1, -1
		case idx == endItemIdx:
			sLine, sCol = 0, 0
			eLine, eCol = endLine, endCol
		default:
			sLine, sCol = 0, 0
			eLine, eCol = -1, -1
		}
	}
	hi.SetHighlight(sLine, sCol, eLine, eCol)
	return hi.(list.Item)
}

// getHighlightRange normalizes the down/drag pair into reading-order coords.
func (c *chatModel) getHighlightRange() (startItemIdx, startLine, startCol, endItemIdx, endLine, endCol int) {
	if c.mouseDownItem < 0 {
		return -1, -1, -1, -1, -1, -1
	}
	di, dj := c.mouseDownItem, c.mouseDragItem
	draggingDown := dj > di ||
		(dj == di && c.mouseDragY > c.mouseDownY) ||
		(dj == di && c.mouseDragY == c.mouseDownY && c.mouseDragX >= c.mouseDownX)
	if draggingDown {
		return di, c.mouseDownY, c.mouseDownX, dj, c.mouseDragY, c.mouseDragX
	}
	return dj, c.mouseDragY, c.mouseDragX, di, c.mouseDownY, c.mouseDownX
}

// selectWord double-click selection. Naive: looks for whitespace boundaries
// on the clicked line. Enough for chat copy; we can swap in UAX#29 later.
func (c *chatModel) selectWord(itemIdx, x, itemY int) {
	item := c.list.ItemAt(itemIdx)
	if item == nil {
		return
	}
	var rendered string
	if rr, ok := item.(list.RawRenderable); ok {
		rendered = rr.RawRender(c.list.Width())
	} else {
		rendered = item.Render(c.list.Width())
	}
	lines := strings.Split(rendered, "\n")
	if itemY < 0 || itemY >= len(lines) {
		return
	}
	contentX := max(x-MessageLeftPadding, 0)
	startCol, endCol := wordBoundaries(stripANSI(lines[itemY]), contentX)
	if startCol == endCol {
		// fall back to single-click selection
		c.mouseDown = true
		c.mouseDownItem = itemIdx
		c.mouseDownX = x
		c.mouseDownY = itemY
		c.mouseDragItem = itemIdx
		c.mouseDragX = x
		c.mouseDragY = itemY
		return
	}
	c.mouseDown = true
	c.mouseDownItem = itemIdx
	c.mouseDownX = startCol + MessageLeftPadding
	c.mouseDownY = itemY
	c.mouseDragItem = itemIdx
	c.mouseDragX = endCol + MessageLeftPadding
	c.mouseDragY = itemY
}

// selectLine triple-click selects the whole line of the clicked item.
func (c *chatModel) selectLine(itemIdx, itemY int) {
	item := c.list.ItemAt(itemIdx)
	if item == nil {
		return
	}
	var rendered string
	if rr, ok := item.(list.RawRenderable); ok {
		rendered = rr.RawRender(c.list.Width())
	} else {
		rendered = item.Render(c.list.Width())
	}
	lines := strings.Split(rendered, "\n")
	if itemY < 0 || itemY >= len(lines) {
		return
	}
	lineLen := lipgloss.Width(lines[itemY])
	c.mouseDown = true
	c.mouseDownItem = itemIdx
	c.mouseDownX = MessageLeftPadding
	c.mouseDownY = itemY
	c.mouseDragItem = itemIdx
	c.mouseDragX = lineLen + MessageLeftPadding
	c.mouseDragY = itemY
}

// wordBoundaries finds the start and exclusive end column of the word at col.
// Whitespace clicks return (col, col) → no selection.
func wordBoundaries(line string, col int) (int, int) {
	if col < 0 || col >= len(line) {
		return col, col
	}
	if isSpace(line[col]) {
		return col, col
	}
	start := col
	for start > 0 && !isSpace(line[start-1]) {
		start--
	}
	end := col
	for end < len(line) && !isSpace(line[end]) {
		end++
	}
	return start, end
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n'
}

// stripANSI is a minimal ANSI escape stripper — enough for word-boundary
// detection on our own rendered content (no DCS, no OSC).
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			// CSI — skip until final byte (0x40..0x7e)
			i += 2
			for i < len(s) {
				ch := s[i]
				if ch >= 0x40 && ch <= 0x7e {
					break
				}
				i++
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
