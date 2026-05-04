package tui

import (
	"fmt"
	"image/color"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noesrafa/sunny/internal/session"
)

func (m Model) View() tea.View {
	v := tea.NewView("starting…")
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	if !m.ready {
		return v
	}
	base := m.renderBase()
	if m.overlay.HasOpen() {
		v.SetContent(m.composeWithModal(base))
	} else {
		v.SetContent(base)
	}
	return v
}


// renderBase produces the full-screen UI without any modal on top.
func (m Model) renderBase() string {
	header := m.renderHeader()
	body := m.renderBody()
	status := m.renderStatus()
	out := lipgloss.JoinVertical(lipgloss.Left, header, body, status)
	return clampHeight(out, m.height)
}

// composeWithModal places the active dialog on top of the base UI using
// lipgloss v2's Canvas + Compositor. Cells the modal doesn't cover keep
// showing the chat underneath — this is the Crush "transparent overlay" feel.
//
// Important: we MUST go through a Compositor (not Canvas.Compose(layer)
// directly), because Compose called on a bare Layer ignores X/Y and draws
// the layer's content over the full canvas bounds.
func (m Model) composeWithModal(base string) string {
	maxW := m.width - 4
	maxH := m.height - 4
	if maxW < 20 {
		maxW = 20
	}
	if maxH < 6 {
		maxH = 6
	}
	modal := m.overlay.ViewTop(maxW, maxH)
	modalW, modalH := lipgloss.Size(modal)

	x := (m.width - modalW) / 2
	if x < 0 {
		x = 0
	}
	y := (m.height - modalH) / 2
	if y < 0 {
		y = 0
	}

	baseLayer := lipgloss.NewLayer(base)
	modalLayer := lipgloss.NewLayer(modal).X(x).Y(y).Z(10)

	canvas := lipgloss.NewCanvas(m.width, m.height)
	canvas.Compose(lipgloss.NewCompositor(baseLayer, modalLayer))
	return canvas.Render()
}

func clampHeight(s string, maxLines int) string {
	if maxLines <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderHeader() string {
	// Single-peer (zero-config local-only) → header stays blank to
	// avoid clutter. Layout reserves headerHeight=1 either way so
	// the math doesn't shift.
	if len(m.peerOrder) <= 1 {
		return ""
	}
	return " " + m.renderPeerSwitcher()
}

// renderPeerSwitcher renders peer pills like
//
//	[1 local]  2 vps  ·  3 pi
//
// where the active peer is highlighted in brackets and inactive
// peers show their Ctrl+N number prefix so the binding is
// discoverable without help. A recent activity dot appears next to
// the number when a non-active peer received an event in the last
// few seconds.
func (m Model) renderPeerSwitcher() string {
	const (
		// activityWindow is how long after an event the dot stays
		// visible. Short enough that it actually marks "this is
		// happening NOW" rather than "this happened sometime today".
		activityWindow = 8 * time.Second
		maxVisible     = 5
	)
	pills := make([]string, 0, len(m.peerOrder))
	now := time.Now()
	visible := m.peerOrder
	if len(visible) > maxVisible {
		visible = visible[:maxVisible]
	}
	for i, name := range visible {
		num := i + 1
		isActive := name == m.activePeer
		dot := ""
		if !isActive {
			if t, ok := m.peerActivity[name]; ok && now.Sub(t) < activityWindow {
				dot = lipgloss.NewStyle().Foreground(colWarning).Bold(true).Render("•") + " "
			}
		}
		if isActive {
			numStr := lipgloss.NewStyle().Foreground(colPrimary).Bold(true).Render(fmt.Sprintf("%d", num))
			nameStr := lipgloss.NewStyle().Foreground(colText).Bold(true).Render(name)
			pills = append(pills, "["+numStr+" "+nameStr+"]")
		} else {
			numStr := lipgloss.NewStyle().Foreground(colSecondary).Bold(true).Render(fmt.Sprintf("%d", num))
			label := m.styles.HeaderDim.Render(name)
			pills = append(pills, dot+numStr+" "+label)
		}
	}
	more := ""
	if len(m.peerOrder) > maxVisible {
		more = m.styles.HeaderDim.Render(fmt.Sprintf(" +%d", len(m.peerOrder)-maxVisible))
	}
	sep := m.styles.HeaderSep.Render(" · ")
	return strings.Join(pills, sep) + more
}

func (m Model) renderBody() string {
	bodyH := m.height - headerHeight - statusHeight
	if bodyH < 6 {
		bodyH = 6
	}
	main := m.renderMain(bodyH)
	sidebar := renderSidebar(m.manager, bodyH, m.styles, m.logoFrame, m.sysStats)
	// 3-col gap between main and sidebar — Crush-style breathing room, no
	// vertical divider line. Sidebar sits on the RIGHT (Rafael's preference,
	// keeps the chat anchored to the left edge where the eye lands first).
	gap := lipgloss.NewStyle().Width(sidebarGap).Height(bodyH).Render("")
	return lipgloss.JoinHorizontal(lipgloss.Top, main, gap, sidebar)
}

func (m Model) renderMain(height int) string {
	mainW := m.width - sidebarWidth - sidebarGap - mainPadLeft
	if mainW < 20 {
		mainW = 20
	}
	// outerW is the full slot the main column occupies in the body row;
	// inner content uses mainW and the PaddingLeft eats the gutter.
	outerW := mainW + mainPadLeft

	// Claude session mode: transcript + (gap) + input + hint. The blank
	// rows between transcript and input give the assistant attribution
	// breathing room. Layout reserves `inputTopGap` rows for it (see
	// layout()), so we render the same number here via a string of N-1
	// newlines (JoinVertical itself adds one between elements).
	cur := m.manager.Current()
	transcript := m.chat.Render()
	input := m.renderInput(cur)
	hint := m.renderInputHint()
	gap := strings.Repeat("\n", inputTopGap-1)
	body := lipgloss.JoinVertical(lipgloss.Left, transcript, gap, input, hint)
	return lipgloss.NewStyle().Width(outerW).Height(height).PaddingLeft(mainPadLeft).Render(body)
}

func (m Model) renderInput(cur *session.Session) string {
	style := m.styles.Input
	if cur != nil && cur.State == session.StateIdle {
		style = m.styles.InputFocused
	}
	mainW := m.width - sidebarWidth - sidebarGap - mainPadLeft
	return style.Width(mainW).Render(m.textarea.View())
}

// renderInputHint is the row under the input. Crush-style: shows the active
// session's cwd · model · branch (shortcuts already live in the sidebar).
//
// The model + effort segment is painted with a pink → purple gradient (same
// vibe as the morphing "thinking" spinner) so the user can spot the active
// effort level at a glance. Branch gets a "●" indicator when the working
// tree has uncommitted/untracked changes pending.
func (m Model) renderInputHint() string {
	s := m.styles
	cur := m.manager.Current()
	if cur == nil {
		return ""
	}
	var parts []string
	parts = append(parts, s.HeaderDim.Render(prettyPath(cur.Cwd)))
	if cur.Model != "" {
		text := cur.Model
		if cur.Effort != "" {
			text = cur.Model + " " + cur.Effort
		}
		parts = append(parts, applyAnimatedForegroundGradient(text, colSecondary, colPrimary, m.logoFrame))
	}
	if cur.Branch != "" {
		branch := s.HeaderDim.Render("⌥ " + cur.Branch)
		if badge := renderChangesBadge(cur.Changes); badge != "" {
			branch += " " + badge
		}
		parts = append(parts, branch)
	}
	sep := s.HeaderSep.Render(" · ")
	return " " + strings.Join(parts, sep)
}

// renderStatus is the bottom row. We used to show "N sessions · M turns"
// on the right but that info is already implicit (sidebar lists sessions,
// each row carries its turn count) — kept the row only so a session's
// `error: …` message has somewhere to surface without stealing input space.
func (m Model) renderStatus() string {
	var left string
	if cur := m.manager.Current(); cur != nil && cur.State == session.StateError && cur.LastErr != nil {
		left = m.styles.ResultError.Render("error: " + cur.LastErr.Error())
	}
	pad := m.width - lipgloss.Width(left)
	if pad < 0 {
		pad = 0
	}
	return left + strings.Repeat(" ", pad)
}

// renderChangesBadge paints the per-bucket file counts of pending git changes
// as a compact, colored pill: green +N (added), pink ~N (modified), red −N
// (deleted), accent ?N (untracked). Empty buckets are omitted; an empty
// badge means a clean tree.
//
// Visual goal: at a glance the user can tell whether they have unstaged
// edits, brand-new files, or deletions still pending — without having to
// drop into the diff dialog.
func renderChangesBadge(c session.ChangeStats) string {
	if !c.Dirty() {
		return ""
	}
	type seg struct {
		count int
		sym   string
		col   color.Color
	}
	segs := []seg{
		{c.Added, "+", colSuccess},
		{c.Modified, "~", colSecondary},
		{c.Deleted, "−", colDanger},
		{c.Untracked, "?", colAccent},
	}
	var parts []string
	for _, s := range segs {
		if s.count == 0 {
			continue
		}
		parts = append(parts,
			lipgloss.NewStyle().Foreground(s.col).Bold(true).Render(
				fmt.Sprintf("%s%d", s.sym, s.count),
			),
		)
	}
	return strings.Join(parts, " ")
}

func prettyPath(p string) string {
	home := homedir()
	if home != "" && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return filepath.Clean(p)
}
