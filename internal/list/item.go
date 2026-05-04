// Package list is a lazy, item-based scrollable list. Ported from Crush
// (charmbracelet/crush, internal/ui/list) — the chat in Crush rides on
// top of this and gets its scroll smoothness, drag-to-select cleanliness,
// and lack of "phantom blank rows" for free. Sunnytui uses it instead of
// bubbles/viewport so we get the same behavior without the bugs.
package list

import (
	"github.com/charmbracelet/x/ansi"
)

// Item represents a single item in the lazy-loaded list.
type Item interface {
	// Render returns the string representation of the item for the given
	// width.
	Render(width int) string
}

// RawRenderable represents an item that can provide a raw rendering
// without additional styling.
type RawRenderable interface {
	// RawRender returns the raw rendered string without any additional
	// styling.
	RawRender(width int) string
}

// Focusable represents an item that can be aware of focus state changes.
type Focusable interface {
	// SetFocused sets the focus state of the item.
	SetFocused(focused bool)
}

// Highlightable represents an item that can highlight a portion of its content.
type Highlightable interface {
	// SetHighlight highlights the content from the given start to end
	// positions. Use -1 for no highlight.
	SetHighlight(startLine, startCol, endLine, endCol int)
	// Highlight returns the current highlight positions within the item.
	Highlight() (startLine, startCol, endLine, endCol int)
}

// MouseClickable represents an item that can handle mouse click events.
type MouseClickable interface {
	// HandleMouseClick processes a mouse click event at the given coordinates.
	// It returns true if the event was handled, false otherwise.
	HandleMouseClick(btn ansi.MouseButton, x, y int) bool
}

