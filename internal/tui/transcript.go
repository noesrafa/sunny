package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/noesrafa/sunny/internal/session"
)

// RenderContext carries cross-cutting concerns into per-item rendering.
type RenderContext struct {
	Width     int
	Styles    Styles
	LiveFrame string              // current spinner frame for live tools
	Markdown  func(string) string // optional markdown renderer; nil → plain wrap
	ModelName string              // active session's claude model id (for attribution row)
}

// simplifyModelName strips Crush-style noise from claude model ids:
// "claude-opus-4-7[1m]" → "Opus 4.7", "claude-sonnet-4-6" → "Sonnet 4.6".
func simplifyModelName(s string) string {
	if s == "" {
		return "Claude"
	}
	if i := strings.Index(s, "["); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "claude-")
	parts := strings.Split(s, "-")
	if len(parts) == 0 {
		return s
	}
	family := parts[0]
	if len(family) > 0 {
		family = strings.ToUpper(family[:1]) + family[1:]
	}
	if len(parts) == 1 {
		return family
	}
	version := strings.Join(parts[1:], ".")
	return family + " " + version
}

func renderToolUse(v session.ToolUseItem, ctx RenderContext) string {
	s := ctx.Styles
	var icon string
	switch {
	case !v.Done:
		frame := ctx.LiveFrame
		if frame == "" {
			frame = "◐"
		}
		icon = s.StatusBusy.Render(frame)
	case v.IsError:
		icon = s.ResultError.Render("✗")
	default:
		icon = s.ResultOK.Render("✓")
	}
	gear := s.ToolPrompt.Render("⚙")
	name := s.ToolName.Render(v.Name)
	header := fmt.Sprintf("%s %s %s", icon, gear, name)
	if len(v.Input) > 0 {
		inputBudget := ctx.Width - lipgloss.Width(header) - 2
		if inputBudget > 8 {
			// linkify after compactJSON: compactJSON byte-slices when over
			// budget, which would shred an OSC 8 escape if linkify ran
			// first. URLs that survive untouched still click; truncated
			// ones render as plain text.
			header += " " + s.ToolInput.Render(linkify(compactJSON(v.Input, inputBudget)))
		}
	}
	if !v.Done {
		return header
	}
	if v.Result == "" {
		return header
	}
	body := truncateLines(v.Result, 8, ctx.Width-4)
	bodyStyle := s.ToolResult
	if v.IsError {
		bodyStyle = s.ResultError
	}
	// Manually prefix each body line with the indent — first line gets the
	// "↳ " glyph, subsequent lines get just the matching width of spaces.
	// Avoids lipgloss.JoinHorizontal, which (in lipgloss v2 + the bubbles
	// viewport with SoftWrap=true) was inserting visible blank rows
	// between the body lines on render. The selection overlay flattens
	// those out via highlight.Apply's ScreenBuffer, which is why the
	// transcript looked "wider" while the user was dragging — same content,
	// just without the spurious blanks.
	//
	// linkify per-line AFTER truncation so the byte-slice in truncateLines
	// can't split an OSC 8 escape mid-sequence. Lines whose URL got cut
	// by the width clamp simply render as plain text — acceptable.
	bodyLines := strings.Split(body, "\n")
	indentLead := s.ToolPrompt.Render("  ↳ ")
	indentRest := "    " // 4 spaces, same width as "  ↳ "
	rendered := make([]string, 0, len(bodyLines)+1)
	rendered = append(rendered, header)
	for i, ln := range bodyLines {
		prefix := indentRest
		if i == 0 {
			prefix = indentLead
		}
		rendered = append(rendered, prefix+bodyStyle.Render(linkify(ln)))
	}
	return strings.Join(rendered, "\n")
}

// shortenPath collapses /Users/me/.sunnytui/images/foo.png down to fit
// `width`. Keeps the basename, replaces middle with "…" if too long.
func shortenPath(p string, width int) string {
	if width <= 0 || lipgloss.Width(p) <= width {
		return p
	}
	if width < 4 {
		return p
	}
	keep := width - 1
	return "…" + p[len(p)-keep:]
}

func wrap(text string, width int) string {
	if width <= 0 {
		return text
	}
	var out strings.Builder
	for _, line := range strings.Split(text, "\n") {
		out.WriteString(wordwrap(line, width))
		out.WriteString("\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

func wordwrap(line string, width int) string {
	if lipgloss.Width(line) <= width {
		return line
	}
	words := strings.Fields(line)
	var b strings.Builder
	col := 0
	for i, w := range words {
		wl := lipgloss.Width(w)
		if i == 0 {
			b.WriteString(w)
			col = wl
			continue
		}
		if col+1+wl > width {
			b.WriteByte('\n')
			b.WriteString(w)
			col = wl
		} else {
			b.WriteByte(' ')
			b.WriteString(w)
			col += 1 + wl
		}
	}
	return b.String()
}

func compactJSON(raw json.RawMessage, max int) string {
	s := strings.TrimSpace(string(raw))
	s = strings.ReplaceAll(s, "\n", " ")
	if max > 0 && lipgloss.Width(s) > max {
		s = s[:max] + "…"
	}
	return s
}

func truncateLines(text string, maxLines, width int) string {
	lines := strings.Split(text, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines = append(lines, "…")
	}
	for i, l := range lines {
		if width > 0 && lipgloss.Width(l) > width {
			lines[i] = l[:width-1] + "…"
		}
	}
	return strings.Join(lines, "\n")
}
