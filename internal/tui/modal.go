package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/rivo/uniseg"
)

// HatchedTitle renders "title ╱╱╱╱…" filling `width` with a diagonal hatch in
// a horizontal gradient between fromCol and toCol. This mirrors Crush's modal
// title bar (see /tmp/charm-crush/internal/ui/common/elements.go).
func HatchedTitle(title string, width int, fromCol, toCol color.Color, titleStyle lipgloss.Style) string {
	titleRendered := titleStyle.Render(title)
	titleW := lipgloss.Width(titleRendered)
	remaining := width - titleW - 1
	if remaining <= 0 {
		return titleRendered
	}
	hatch := strings.Repeat("╱", remaining)
	hatch = applyForegroundGradient(hatch, fromCol, toCol)
	return titleRendered + " " + hatch
}

// applyForegroundGradient colors each grapheme cluster of s with a position
// in the gradient between from and to. Adapted from Crush's grad.go using
// lipgloss.Blend1D, which exists in lipgloss v2.
func applyForegroundGradient(s string, from, to color.Color) string {
	if s == "" {
		return ""
	}
	var clusters []string
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		clusters = append(clusters, string(g.Runes()))
	}
	n := len(clusters)
	if n == 1 {
		return lipgloss.NewStyle().Foreground(from).Render(clusters[0])
	}
	ramp := lipgloss.Blend1D(n, from, to)
	var b strings.Builder
	for i, c := range clusters {
		b.WriteString(lipgloss.NewStyle().Foreground(ramp[i]).Render(c))
	}
	return b.String()
}

// applyAnimatedForegroundGradient is applyForegroundGradient with a phase
// offset — the colors slide left → right and bounce back, same trick the
// brand logo uses. `frame` is the monotonically-increasing logo tick
// counter; passing the model's logoFrame makes the gradient animate in
// lockstep with the brand mark.
func applyAnimatedForegroundGradient(s string, from, to color.Color, frame int) string {
	if s == "" {
		return ""
	}
	var clusters []string
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		clusters = append(clusters, string(g.Runes()))
	}
	n := len(clusters)
	if n == 0 {
		return ""
	}
	if n == 1 {
		return lipgloss.NewStyle().Foreground(from).Render(clusters[0])
	}
	ramp := lipgloss.Blend1D(n, from, to)
	span := 2 * n
	var b strings.Builder
	for i, c := range clusters {
		// Palindromic wrap so the gradient flows out and back without a
		// jarring jump when the phase wraps around.
		pos := (i + frame) % span
		if pos < 0 {
			pos += span
		}
		if pos >= n {
			pos = span - 1 - pos
		}
		b.WriteString(lipgloss.NewStyle().Foreground(ramp[pos]).Render(c))
	}
	return b.String()
}
