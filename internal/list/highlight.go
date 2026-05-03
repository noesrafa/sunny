package list

import (
	"image"
	"strings"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// DefaultHighlighter is the default highlighter function that applies inverse style.
var DefaultHighlighter Highlighter = func(x, y int, c *uv.Cell) *uv.Cell {
	if c == nil {
		return c
	}
	c.Style.Attrs |= uv.AttrReverse
	return c
}

// Highlighter represents a function that defines how to highlight text.
type Highlighter func(x, y int, c *uv.Cell) *uv.Cell

// HighlightContent walks the highlight region and returns the plain text
// inside it (no ANSI), suitable for clipboard copy. Inserts newlines between
// rows and trims trailing whitespace.
func HighlightContent(content string, area image.Rectangle, startLine, startCol, endLine, endCol int) string {
	var sb strings.Builder
	pos := image.Pt(-1, -1)
	HighlightBuffer(content, area, startLine, startCol, endLine, endCol, func(x, y int, c *uv.Cell) *uv.Cell {
		if c == nil {
			return c
		}
		pos.X = x
		if pos.Y == -1 {
			pos.Y = y
		} else if y > pos.Y {
			sb.WriteString(strings.Repeat("\n", y-pos.Y))
			pos.Y = y
		}
		sb.WriteString(c.Content)
		return c
	})
	return strings.TrimRight(sb.String(), " \n")
}

// Highlight highlights a region of text within the given content and region.
// Returns the styled string ready to drop back into a viewport / list.
func Highlight(content string, area image.Rectangle, startLine, startCol, endLine, endCol int, highlighter Highlighter) string {
	buf := HighlightBuffer(content, area, startLine, startCol, endLine, endCol, highlighter)
	if buf == nil {
		return content
	}
	return buf.Render()
}

// HighlightBuffer highlights a region of text within the given content and
// region, returning a [uv.ScreenBuffer]. We intentionally skip Crush's
// stringext.NormalizeSpace pass so the buffer preserves leading whitespace
// inside the transcript (sunnytui's tool blocks rely on that for the indent
// columns to line up).
func HighlightBuffer(content string, area image.Rectangle, startLine, startCol, endLine, endCol int, highlighter Highlighter) *uv.ScreenBuffer {
	if startLine < 0 || startCol < 0 {
		return nil
	}
	if highlighter == nil {
		highlighter = DefaultHighlighter
	}

	width, height := area.Dx(), area.Dy()
	if width <= 0 || height <= 0 {
		return nil
	}
	buf := uv.NewScreenBuffer(width, height)
	styled := uv.NewStyledString(content)
	styled.Draw(&buf, area)

	// Treat -1 as "end of content"
	if endLine < 0 {
		endLine = height - 1
	}
	if endCol < 0 {
		endCol = width
	}

	for y := startLine; y <= endLine && y < height; y++ {
		if y >= buf.Height() {
			break
		}

		line := buf.Line(y)

		colStart := 0
		if y == startLine {
			colStart = min(startCol, len(line))
		}

		colEnd := len(line)
		if y == endLine {
			colEnd = min(endCol, len(line))
		}

		// Track last non-empty position so we don't paint trailing blank cells.
		lastContentX := -1
		for x := colStart; x < colEnd; x++ {
			cell := line.At(x)
			if cell == nil {
				continue
			}
			if cell.Content != "" && cell.Content != " " {
				lastContentX = x
			}
		}

		highlightEnd := colEnd
		if lastContentX >= 0 {
			highlightEnd = lastContentX + 1
		} else {
			highlightEnd = colStart
		}

		for x := colStart; x < highlightEnd; x++ {
			if !image.Pt(x, y).In(area) {
				continue
			}
			cell := line.At(x)
			if cell != nil {
				highlighter(x, y, cell)
			}
		}
	}

	return &buf
}

// ToHighlighter converts a [lipgloss.Style] to a [Highlighter] — the cell
// style overlays the underlying content. Convenient when you want a colored
// selection overlay instead of plain reverse-video.
func ToHighlighter(lgStyle lipgloss.Style) Highlighter {
	return func(_ int, _ int, c *uv.Cell) *uv.Cell {
		if c != nil {
			c.Style = ToStyle(lgStyle)
		}
		return c
	}
}

// ToStyle converts an inline [lipgloss.Style] to a [uv.Style].
func ToStyle(lgStyle lipgloss.Style) uv.Style {
	var uvStyle uv.Style

	uvStyle.Fg = lgStyle.GetForeground()
	uvStyle.Bg = lgStyle.GetBackground()

	var attrs uint8
	if lgStyle.GetBold() {
		attrs |= uv.AttrBold
	}
	if lgStyle.GetItalic() {
		attrs |= uv.AttrItalic
	}
	if lgStyle.GetUnderline() {
		uvStyle.Underline = uv.UnderlineSingle
	}
	if lgStyle.GetStrikethrough() {
		attrs |= uv.AttrStrikethrough
	}
	if lgStyle.GetFaint() {
		attrs |= uv.AttrFaint
	}
	if lgStyle.GetBlink() {
		attrs |= uv.AttrBlink
	}
	if lgStyle.GetReverse() {
		attrs |= uv.AttrReverse
	}
	uvStyle.Attrs = attrs

	return uvStyle
}

// Apply is the convenience entry point that sunnytui used before it had a
// list package — kept for back-compat with the clipboard path. Equivalent to
// Highlight with the default reverse-video highlighter.
func Apply(content string, width, height, startLine, startCol, endLine, endCol int) string {
	if width <= 0 || height <= 0 {
		return content
	}
	area := image.Rect(0, 0, width, height)
	return Highlight(content, area, startLine, startCol, endLine, endCol, DefaultHighlighter)
}

// Extract returns the plain (un-styled) text inside the selection range,
// suitable for clipboard. Mirrors HighlightContent — the older sunnytui name.
func Extract(content string, width, height, startLine, startCol, endLine, endCol int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	area := image.Rect(0, 0, width, height)
	return HighlightContent(content, area, startLine, startCol, endLine, endCol)
}
