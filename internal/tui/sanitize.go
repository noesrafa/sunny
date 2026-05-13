package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// sanitizeRender scrubs untrusted strings (tool inputs, tool result
// bodies, anything that comes from a child process or that the model
// can shape freely) before they reach the chat layout. Without this,
// the UI can be hijacked by:
//
//   - Bare CR ("\r"). Curl with -i prints "HTTP/1.1 ...\r\n" headers;
//     splitting on \n keeps the trailing \r, and the terminal moves
//     the cursor to col 0 mid-line, painting the next glyphs on top
//     of what's already there. That's the "scattered columns" symptom.
//   - ANSI/CSI escape sequences. A single \x1b[2J would clear the
//     screen; \x1b[31m would smear red into the rest of the row.
//   - Other C0 controls (NUL, BEL, BS, VT, FF, DEL). Renders as
//     zero-width oddities or real cursor motions depending on term.
//   - Bare TAB. Width math sees one cell, but the terminal expands
//     to a tab stop, shifting later glyphs out of where lipgloss
//     decided to wrap.
//
// Newlines are preserved — callers split on them upstream and we
// want each line measured independently.
func sanitizeRender(s string) string {
	if s == "" {
		return s
	}
	s = ansi.Strip(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteString("    ")
		case r < 0x20, r == 0x7F:
			// drop other C0 controls + DEL
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// truncateVisual cuts s to at most width visible cells, appending
// tail when truncation happens. Rune-safe (never slices mid-codepoint)
// and ANSI-aware (preserves escape sequences spanning the cut).
func truncateVisual(s string, width int, tail string) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, tail)
}
