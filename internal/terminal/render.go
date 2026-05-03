package terminal

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/hinshun/vt10x"
)

// Bit flags inside vt10x.Glyph.Mode (the package keeps these unexported, so
// we mirror them here. Values come from /tmp/charm-x... actually from
// /tmp/.../vt10x/state.go:13).
const (
	attrReverse = 1 << iota
	attrUnderline
	attrBold
	_ // gap
	attrItalic
	attrBlink
)

// Render walks the pane's cell grid and returns a styled string suitable for
// a Bubble Tea View(). Cells are coalesced into runs of the same style to
// keep ANSI noise down.
func Render(p *Pane) string {
	term := p.LockTerm()
	defer p.UnlockTerm()
	cols, rows := term.Size()
	if cols == 0 || rows == 0 {
		return ""
	}

	var out strings.Builder
	// Pre-grow: ~5 bytes per cell is reasonable for styled text.
	out.Grow(cols * rows * 6)

	for y := 0; y < rows; y++ {
		out.WriteString(renderRow(term, y, cols))
		if y < rows-1 {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

// renderRow batches cells of the same style into a single Render() call.
func renderRow(term vt10x.Terminal, y, cols int) string {
	var out strings.Builder
	var runText strings.Builder
	var runStyle lipgloss.Style
	var runActive bool

	flush := func() {
		if runText.Len() > 0 {
			out.WriteString(runStyle.Render(runText.String()))
			runText.Reset()
		}
	}

	for x := 0; x < cols; x++ {
		cell := term.Cell(x, y)
		st := glyphToStyle(cell)
		if !runActive || !sameStyle(runStyle, st) {
			flush()
			runStyle = st
			runActive = true
		}
		ch := cell.Char
		if ch == 0 || ch < 0x20 && ch != '\t' {
			ch = ' '
		}
		runText.WriteRune(ch)
	}
	flush()
	return out.String()
}

// sameStyle compares the bits we actually set in glyphToStyle (foreground,
// background, bold/italic/underline/reverse). lipgloss has no Equal so we
// compare by Render of a probe — cheap enough.
func sameStyle(a, b lipgloss.Style) bool {
	return a.Render("·") == b.Render("·")
}

// glyphToStyle maps a vt10x.Glyph (color + attribute bits) to a lipgloss style.
func glyphToStyle(g vt10x.Glyph) lipgloss.Style {
	s := lipgloss.NewStyle()

	fg, ok := vtColor(g.FG, true)
	if ok {
		s = s.Foreground(fg)
	}
	bg, ok := vtColor(g.BG, false)
	if ok {
		s = s.Background(bg)
	}

	if g.Mode&attrBold != 0 {
		s = s.Bold(true)
	}
	if g.Mode&attrItalic != 0 {
		s = s.Italic(true)
	}
	if g.Mode&attrUnderline != 0 {
		s = s.Underline(true)
	}
	if g.Mode&attrReverse != 0 {
		s = s.Reverse(true)
	}
	return s
}

// vtColor maps a vt10x.Color to lipgloss-friendly color.Color. Returns
// ok=false for "default", which lipgloss handles by simply not setting
// fg/bg.
func vtColor(c vt10x.Color, isFG bool) (color.Color, bool) {
	switch c {
	case vt10x.DefaultFG, vt10x.DefaultBG, vt10x.DefaultCursor:
		return nil, false
	}
	if c < 256 {
		// ANSI 16 + xterm 256: lipgloss.Color accepts the index as a string.
		return lipgloss.Color(strconv.Itoa(int(c))), true
	}
	// True color packed as r<<16 | g<<8 | b (vt10x convention).
	r := (c >> 16) & 0xFF
	g := (c >> 8) & 0xFF
	b := c & 0xFF
	return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X", r, g, b)), true
}
