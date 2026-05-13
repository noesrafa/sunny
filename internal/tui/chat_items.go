package tui

import (
	"fmt"
	"image"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/noesrafa/sunny/internal/list"
	"github.com/noesrafa/sunny/internal/session"
)

// MessageLeftPadding is how many columns the outer style consumes (border +
// padding). Mirrors Crush's chat.MessageLeftPaddingTotal — matches both
// UserMsgBlurred (border 1 + padding 1) and AssistantMsgBlurred (padding 2).
const MessageLeftPadding = 2

// cachedItem caches a render by width. RawRender consults it first; we drop
// the cache when SetItems rebuilds the chat (so updated ToolUseItem.Result
// etc. show through immediately).
type cachedItem struct {
	rendered string
	width    int
	height   int
}

func (c *cachedItem) get(width int) (string, int, bool) {
	if c.width == width && c.rendered != "" {
		return c.rendered, c.height, true
	}
	return "", 0, false
}

func (c *cachedItem) set(rendered string, width, height int) {
	c.rendered = rendered
	c.width = width
	c.height = height
}

// highlightableItem implements list.Highlightable. SetHighlight subtracts
// MessageLeftPadding from the columns so callers can pass viewport-space
// coords and the overlay still lands on the content.
type highlightableItem struct {
	startLine, startCol int
	endLine, endCol     int
	highlighter         list.Highlighter
}

func newHighlightable() *highlightableItem {
	return &highlightableItem{
		startLine: -1,
		startCol:  -1,
		endLine:   -1,
		endCol:    -1,
	}
}

func (h *highlightableItem) isHighlighted() bool {
	return h.startLine != -1 || h.endLine != -1
}

func (h *highlightableItem) renderHighlighted(content string, width, height int) string {
	if !h.isHighlighted() || width <= 0 || height <= 0 {
		return content
	}
	area := image.Rect(0, 0, width, height)
	hl := h.highlighter
	if hl == nil {
		hl = list.DefaultHighlighter
	}
	return list.Highlight(content, area, h.startLine, h.startCol, h.endLine, h.endCol, hl)
}

// SetHighlight implements list.Highlightable. Columns are adjusted for the
// outer-style left inset so the highlight aligns with the visible glyphs.
func (h *highlightableItem) SetHighlight(startLine, startCol, endLine, endCol int) {
	offset := MessageLeftPadding
	h.startLine = startLine
	if startCol >= 0 {
		h.startCol = max(0, startCol-offset)
	} else {
		h.startCol = startCol
	}
	h.endLine = endLine
	if endCol >= 0 {
		h.endCol = max(0, endCol-offset)
	} else {
		h.endCol = endCol
	}
}

// Highlight implements list.Highlightable.
func (h *highlightableItem) Highlight() (startLine, startCol, endLine, endCol int) {
	return h.startLine, h.startCol, h.endLine, h.endCol
}

// chatItem wraps a session.Item so it can live inside a list.List. Each
// wrapper owns its render cache + highlight state; rebuilding the chat (e.g.
// after a new event) creates fresh wrappers, dropping stale caches.
type chatItem struct {
	*cachedItem
	*highlightableItem

	id   string
	item session.Item
	ctx  RenderContext
}

func newChatItem(id string, item session.Item, ctx RenderContext) *chatItem {
	return &chatItem{
		cachedItem:        &cachedItem{},
		highlightableItem: newHighlightable(),
		id:                id,
		item:              item,
		ctx:               ctx,
	}
}

// ID returns a stable identifier so the chat can locate items by id.
func (c *chatItem) ID() string { return c.id }

// RawRender returns the item's content without the outer style (border /
// padding). This is what gets the highlight overlay applied.
func (c *chatItem) RawRender(width int) string {
	inner := width - MessageLeftPadding
	if inner < 1 {
		inner = 1
	}
	content, height, ok := c.get(inner)
	if !ok {
		ctx := c.ctx
		ctx.Width = inner
		content = RenderItemRaw(c.item, ctx)
		height = lipgloss.Height(content)
		c.set(content, inner, height)
	}
	return c.renderHighlighted(content, inner, height)
}

// Render returns the full styled item: outer prefix on every line + content.
// We avoid lipgloss's container Render here to skip the wrapping pass — the
// raw content is already wrapped to inner width by glamour / wordwrap.
func (c *chatItem) Render(width int) string {
	raw := c.RawRender(width)
	prefix := outerPrefixFor(c.item, c.ctx.Styles)
	if prefix == "" {
		return raw
	}
	lines := strings.Split(raw, "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n")
}

// outerPrefixFor returns the inline prefix string that the outer style would
// emit on its first column. We pre-render it once per Render call so each
// line of the body just gets a string concat.
func outerPrefixFor(it session.Item, s Styles) string {
	switch it.(type) {
	case session.UserItem:
		// Border-left ▌ + 1 col padding. NormalBorder is "│" — Crush uses ▌
		// for focused, NormalBorder for blurred. Match the Render() output
		// of UserMsgBlurred on a single line.
		return s.UserMsgBlurred.Render("")[:0] + renderBorderPrefix(s.UserMsgBlurred)
	case session.ResultItem:
		// Result attribution row has no padding — it's the bare row.
		return ""
	default:
		// AssistantMsgBlurred: padding-left 2 → 2 spaces.
		return "  "
	}
}

// renderBorderPrefix builds the leading "│ " (border + padding) for a styled
// row. We render an empty styled string to capture the border ANSI then trim
// the trailing reset so the prefix concatenates cleanly with body text.
func renderBorderPrefix(style lipgloss.Style) string {
	// Render a single space so the style emits border + padding around it,
	// then take everything up to (but not including) the body cell.
	rendered := style.Render(" ")
	// The styled output is: <ANSI border>│<ANSI reset> <ANSI body>...<reset>
	// We want everything up to the space that represents body padding.
	// Simpler: just emit border glyph + padding manually using the style's
	// border-left rune and padding count.
	_ = rendered
	border := style.GetBorderStyle()
	leftRune := border.Left
	if leftRune == "" {
		leftRune = " "
	}
	padLeft := style.GetPaddingLeft()
	fg := style.GetBorderLeftForeground()
	borderStyled := lipgloss.NewStyle().Foreground(fg).Render(leftRune)
	return borderStyled + strings.Repeat(" ", padLeft)
}

// RenderItemRaw produces the content of a session.Item without applying the
// outer container style (border + padding). Mirrors RenderItem (transcript.go)
// but stops one layer earlier so the chat list can wrap the body itself,
// keeping highlight + cache aware of pure content coordinates.
func RenderItemRaw(it session.Item, ctx RenderContext) string {
	s := ctx.Styles
	switch v := it.(type) {
	case session.UserItem:
		// Apply UserText (colText) so the body text picks up the active
		// palette's text color — Fallout's phosphor green, Whisper's near
		// white, etc. Without this the user text renders in the terminal
		// default foreground regardless of theme.
		// linkify before wrap so the URL regex sees an unbroken string.
		// wordwrap treats the OSC 8 sequence as a single token (lipgloss
		// width-counts only the visible glyphs), so the link stays intact.
		body := s.UserText.Render(wrap(linkify(v.Text), ctx.Width-1))
		if len(v.Attachments) > 0 || len(v.PastedTexts) > 0 {
			lines := make([]string, 0, len(v.Attachments)+len(v.PastedTexts)+1)
			lines = append(lines, body)
			for _, a := range v.Attachments {
				label := fmt.Sprintf("↳ [Image #%d] %s", a.Index, shortenPath(a.Path, ctx.Width-4))
				lines = append(lines, s.Hint.Render(label))
			}
			for _, p := range v.PastedTexts {
				label := fmt.Sprintf("↳ [Pasted text #%d] %d lines, %d chars", p.Index, p.Lines, len(p.Text))
				lines = append(lines, s.Hint.Render(label))
			}
			body = strings.Join(lines, "\n")
		}
		return body

	case session.AssistantTextItem:
		if ctx.Markdown != nil {
			return strings.TrimSuffix(ctx.Markdown(v.Text), "\n")
		}
		return s.AssistantText.Render(wrap(v.Text, ctx.Width-1))

	case session.ThinkingItem:
		return s.AssistantThink.Render(wrap(linkify(v.Text), ctx.Width-1))

	case session.ToolUseItem:
		return renderToolUse(v, withWidth(ctx, ctx.Width))

	case session.ToolResultItem:
		// linkify AFTER truncate: truncateLines slices by bytes, which
		// would shred an OSC 8 escape mid-sequence. URLs that survive
		// the truncation untouched still get clickable; ones cut by the
		// width clamp render as plain text (acceptable tradeoff vs.
		// rewriting truncateLines to be ANSI-aware).
		preview := linkify(truncateLines(v.Content, 6, ctx.Width-2))
		return s.ToolResult.Render("↳ " + preview)

	case session.EmptyResponseItem:
		return s.Hint.Render("(sin respuesta)")

	case session.ErrorItem:
		return s.ResultError.Render("✗ " + linkify(v.Message))

	case session.ResultItem:
		modelName := simplifyModelName(ctx.ModelName)
		duration := fmt.Sprintf("%.1fs", float64(v.DurationMs)/1000.0)
		icon := s.AttribIcon.Render("◇")
		mdl := s.AttribModel.Render(modelName)
		dur := s.AttribDuration.Render(duration)
		errIcon := ""
		if v.IsError {
			errIcon = s.ResultError.Render("✗ ") + " "
		}
		return errIcon + icon + " " + mdl + " · " + dur
	}
	return ""
}

// withWidth returns ctx with a different Width, used so the inner renderers
// see the inner-content width without touching the original ctx.
func withWidth(ctx RenderContext, w int) RenderContext {
	ctx.Width = w
	return ctx
}

// chatItemID composes a stable id for a session item at index idx in a
// session — uses session.ID + idx so wrapper reuse works across rebuilds.
func chatItemID(sessionID string, idx int) string {
	return fmt.Sprintf("%s:%d", sessionID, idx)
}

// buildChatItems takes a session's items and produces list.Item wrappers ready
// for the chat list. The optional spinner is the live "thinking" indicator
// rendered as a trailing pseudo-item.
func buildChatItems(s *session.Session, ctx RenderContext) []list.Item {
	out := make([]list.Item, 0, len(s.Items)+1)
	for i, it := range s.Items {
		out = append(out, newChatItem(chatItemID(s.ID, i), it, ctx))
	}
	return out
}

// stringItem is a no-cache list.Item that renders a static string. Used for
// the welcome screen and the trailing thinking spinner — content that doesn't
// belong to a session.Item but still has to live inside the chat list.
type stringItem string

func (s stringItem) Render(width int) string { return string(s) }

