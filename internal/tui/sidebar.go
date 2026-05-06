package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/noesrafa/sunny/internal/client"
	"github.com/noesrafa/sunny/internal/session"
	"github.com/noesrafa/sunny/internal/sysstats"
)

const (
	// 33 cols leaves innerW=29 (after the padding-and-safety margin),
	// which is exactly the width of the new dot-matrix SUNNY block
	// (5 cols per letter × 5 letters + 4 single-col gaps = 29).
	sidebarWidth = 33
	sidebarGap   = 3 // empty cols between main column and sidebar
)

// PeerStatus is the per-peer summary the sidebar renders. Built by
// the model from peerOrder + activePeer + peerActivity.
type PeerStatus struct {
	Name       string
	Active     bool
	HasActivity bool // dot ON when a non-active peer pinged us recently
}

func renderSidebar(mgr *session.Manager, peers []PeerStatus, runs []client.Run, monitors []client.Monitor, height int, s Styles, logoFrame int, sys sysstats.Stats) string {
	innerW := sidebarWidth - 4 // padding(0,1) + 1 col safety on each side

	rows := []string{renderLogo(innerW, s, logoFrame), ""}
	if len(peers) > 1 {
		rows = append(rows, renderPeersSection(peers, innerW, s)...)
		rows = append(rows, "")
	}
	rows = append(rows, renderSessionsSection(mgr, innerW, s)...)
	rows = append(rows, "")
	rows = append(rows, renderRunsSection(runs, innerW, s)...)
	rows = append(rows, "")
	rows = append(rows, renderMonitorsSection(monitors, innerW, s)...)
	if section := renderUsageSection(mgr, sys, innerW, s); len(section) > 0 {
		rows = append(rows, "")
		rows = append(rows, section...)
	}
	rows = append(rows, "")
	rows = append(rows, renderShortcutsSection(innerW, s)...)

	body := strings.Join(rows, "\n")
	return lipgloss.NewStyle().
		Width(sidebarWidth).
		Height(height).
		Padding(0, 1).
		Render(body)
}

// renderPeersSection draws the federation roster as numbered rows.
// Active peer marked with the same ▎ indicator the active session
// uses, plus a Ctrl+N hint label so the binding is discoverable
// without the help modal. Activity dot ON when the peer fired a
// recent bus event.
func renderPeersSection(peers []PeerStatus, innerW int, s Styles) []string {
	rows := sectionHeader("peers", innerW, s)
	for i, p := range peers {
		if i >= 9 {
			rows = append(rows, s.Hint.Render(fmt.Sprintf("  +%d more", len(peers)-9)))
			break
		}
		num := i + 1
		dot := " "
		if p.HasActivity && !p.Active {
			dot = lipgloss.NewStyle().Foreground(colWarning).Bold(true).Render("•")
		}
		var line string
		if p.Active {
			indicator := s.UserPrompt.Render("▎")
			numStr := lipgloss.NewStyle().Foreground(colPrimary).Bold(true).Render(fmt.Sprintf("%d", num))
			nameStr := s.AssistantText.Bold(true).Render(p.Name)
			line = indicator + numStr + " " + nameStr
		} else {
			numStr := lipgloss.NewStyle().Foreground(colSecondary).Render(fmt.Sprintf("%d", num))
			line = " " + numStr + " " + s.HeaderDim.Render(p.Name) + " " + dot
		}
		rows = append(rows, line)
	}
	return rows
}

// sectionHeader returns the bold title + the rule below it, the canonical
// way every sidebar block starts.
func sectionHeader(title string, innerW int, s Styles) []string {
	return []string{
		s.HeaderTitle.Render(title),
		s.HeaderSep.Render(strings.Repeat("─", innerW)),
	}
}

func renderSessionsSection(mgr *session.Manager, innerW int, s Styles) []string {
	rows := sectionHeader("sessions", innerW, s)
	if len(mgr.Sessions) == 0 {
		return append(rows, s.Hint.Render("(none)"))
	}
	for i, sess := range mgr.Sessions {
		if i > 0 {
			rows = append(rows, "")
		}
		rows = append(rows, renderSidebarRow(sess, i == mgr.Active, s)...)
	}
	return rows
}

// renderRunsSection draws the per-peer background-services strip
// below sessions. Each row shows a colored status pill (green
// running / dim stopped / red failed / blue exited) plus the run
// name. Empty list shows the Ctrl+U hint so the user knows where
// to manage them.
func renderRunsSection(runs []client.Run, innerW int, s Styles) []string {
	rows := sectionHeader("runs", innerW, s)
	if len(runs) == 0 {
		return append(rows, s.Hint.Render("(ninguno — ctrl+u)"))
	}
	for _, r := range runs {
		rows = append(rows, renderRunsRow(r, innerW, s))
	}
	return rows
}

// renderMonitorsSection draws the active-peer's enabled monitors.
// Disabled ones don't appear here — the manager dialog (Ctrl+B)
// shows the full set.
func renderMonitorsSection(mons []client.Monitor, innerW int, s Styles) []string {
	rows := sectionHeader("monitors", innerW, s)
	if len(mons) == 0 {
		return append(rows, s.Hint.Render("(ninguno — ctrl+b)"))
	}
	for _, mon := range mons {
		rows = append(rows, renderMonitorsRow(mon, innerW, s))
	}
	return rows
}

func renderMonitorsRow(mon client.Monitor, innerW int, s Styles) string {
	var pill string
	switch {
	case mon.LastErr != "":
		pill = lipgloss.NewStyle().Foreground(colDanger).Bold(true).Render("✗")
	case mon.Running:
		pill = lipgloss.NewStyle().Foreground(colSuccess).Bold(true).Render("●")
	default:
		pill = s.Hint.Render("○")
	}
	maxName := innerW - 4
	name := mon.Name
	if maxName > 0 && len(name) > maxName {
		name = name[:maxName-1] + "…"
	}
	return pill + " " + s.AssistantText.Render(name)
}

func renderRunsRow(r client.Run, innerW int, s Styles) string {
	var pill string
	switch r.State.Status {
	case client.RunRunning:
		pill = lipgloss.NewStyle().Foreground(colSuccess).Bold(true).Render("●")
	case client.RunFailed:
		pill = lipgloss.NewStyle().Foreground(colDanger).Bold(true).Render("✗")
	case client.RunExited:
		pill = lipgloss.NewStyle().Foreground(colSecondary).Render("○")
	default:
		pill = s.Hint.Render("○")
	}
	maxName := innerW - 4
	name := r.Name
	if maxName > 0 && len(name) > maxName {
		name = name[:maxName-1] + "…"
	}
	if r.State.Status == client.RunRunning {
		return pill + " " + s.AssistantText.Render(name)
	}
	return pill + " " + s.HeaderDim.Render(name)
}

func renderUsageSection(mgr *session.Manager, sys sysstats.Stats, innerW int, s Styles) []string {
	_ = mgr
	sysRows := buildSysStatsRows(sys, innerW, s)
	if len(sysRows) == 0 {
		return nil
	}
	header := sectionHeader("usage", innerW, s)
	return append(header, sysRows...)
}

// buildSysStatsRows renders the whole-machine cpu/ram bars under the
// usage section. Sample==zero (e.g. sysstats.Sample failed or returned
// before the first tick landed) means the section is rendered without
// these rows — easier than threading "is initialized" through everything.
func buildSysStatsRows(st sysstats.Stats, innerW int, s Styles) []string {
	if st.CPUPct == 0 && st.MemPct == 0 {
		return nil
	}
	return []string{
		renderProgressBar("cpu", st.CPUPct, "", innerW, s),
		renderProgressBar("ram", st.MemPct, "", innerW, s),
	}
}

// renderShortcutsSection shows just the three most-used keys.
// Everything else lives in the help modal (ctrl+/) so the sidebar
// doesn't crowd out the sessions/peers view.
func renderShortcutsSection(innerW int, s Styles) []string {
	g := s.HeaderDim
	return []string{
		s.HeaderSep.Render(strings.Repeat("─", innerW)),
		g.Render("ctrl+a  agents"),
		g.Render("ctrl+n  new chat"),
		g.Render("esc     quit"),
		g.Render("ctrl+/  more"),
	}
}

func renderSidebarRow(sess *session.Session, active bool, s Styles) []string {
	badge := stateBadge(sess.State, s)
	title := sess.Title
	if title == "" {
		title = "session"
	}
	maxTitleLen := sidebarWidth - 8
	if maxTitleLen > 0 && len(title) > maxTitleLen {
		title = "…" + title[len(title)-(maxTitleLen-1):]
	}
	indicator := " "
	titleStyle := s.AssistantText
	if active {
		indicator = s.UserPrompt.Render("▎")
		titleStyle = s.AssistantText.Bold(true)
	}
	line1 := indicator + badge + " " + titleStyle.Render(title)

	var line2 string
	switch sess.State {
	case session.StateThinking:
		live := sess.LiveStatus()
		// Use the journal-derived turn-start time so this counter
		// ticks correctly on EVERY viewer, not just the sender.
		// Pre-v0.19 we used a local-only StartedAt that left the
		// viewer's "thinking · Xs" reading 63 billion seconds.
		secs := 0.0
		if start := sess.TurnStart(); !start.IsZero() {
			secs = time.Since(start).Seconds()
		}
		txt := fmt.Sprintf("%s · %.1fs", live, secs)
		line2 = "    " + s.StatusBusy.Render(txt)
	case session.StateError:
		msg := "error"
		if sess.LastErr != nil {
			msg = sess.LastErr.Error()
		}
		if len(msg) > sidebarWidth-6 && sidebarWidth > 6 {
			msg = msg[:sidebarWidth-7] + "…"
		}
		line2 = "    " + s.ResultError.Render(msg)
	default:
		base := "ready"
		if sess.Turns > 0 {
			base = fmt.Sprintf("%d turns", sess.Turns)
		}
		line2 = "    " + s.Hint.Render(base)
	}
	return []string{line1, line2}
}

// barRamp caches the per-cell Blend1D ramp used by renderProgressBar.
// Recomputing the ramp + allocating one lipgloss.Style per cell on every
// sidebar render (the logo tick fires the whole sidebar at ~120ms) was
// measurably wasteful — the ramp only changes on resize or theme swap.
var (
	cachedBarRamp     []lipgloss.Style // pre-styled "━" cells, indexed 0..barW-1
	cachedBarEmpty    lipgloss.Style   // pre-styled "─" cell
	cachedBarRampW    int
	cachedBarRampTop  any // colTertiary at build time — compared by interface identity
	cachedBarRampBot  any // colDanger at build time
	cachedBarBorderID any // colBorder at build time (drives the empty cell)
)

// barCells returns (filled-cell styles indexed 0..w-1, empty-cell style) for
// the requested bar width. Hits the package-level cache when neither width
// nor palette has changed.
func barCells(w int) ([]lipgloss.Style, lipgloss.Style) {
	if w < 1 {
		w = 1
	}
	if cachedBarRamp != nil &&
		cachedBarRampW == w &&
		cachedBarRampTop == colTertiary &&
		cachedBarRampBot == colDanger &&
		cachedBarBorderID == colBorder {
		return cachedBarRamp, cachedBarEmpty
	}
	ramp := lipgloss.Blend1D(w, colTertiary, colDanger)
	cells := make([]lipgloss.Style, w)
	for i := 0; i < w; i++ {
		cells[i] = lipgloss.NewStyle().Foreground(ramp[i])
	}
	cachedBarRamp = cells
	cachedBarEmpty = lipgloss.NewStyle().Foreground(colBorder)
	cachedBarRampW = w
	cachedBarRampTop = colTertiary
	cachedBarRampBot = colDanger
	cachedBarBorderID = colBorder
	return cachedBarRamp, cachedBarEmpty
}

// renderProgressBar is the canonical thin one-liner used by every usage
// metric. Layout:
//
//	ctx ━━━━━──────── 15%
//	5h  ━━━━────────  9% 3h54m
//	7d  ─────────────  1% 156h
//
// The filled portion uses a Blend1D ramp from `colTertiary` (mint, healthy)
// to `colDanger` (red, near-cap), so the warmer colors only appear as the
// bar fills towards the right — visually communicates risk without needing
// per-percentage thresholds.
func renderProgressBar(label string, pctF float64, reset string, innerW int, s Styles) string {
	if pctF < 0 {
		pctF = 0
	}
	if pctF > 100 {
		pctF = 100
	}
	pct := int(pctF + 0.5)
	pctStr := fmt.Sprintf("%3d%%", pct)
	paddedLabel := fmt.Sprintf("%-3s", label)

	barW := innerW - lipgloss.Width(paddedLabel) - 1 - 1 - lipgloss.Width(pctStr)
	if reset != "" {
		barW -= 1 + lipgloss.Width(reset)
	}
	if barW < 4 {
		barW = 4
	}

	filled := pct * barW / 100
	if filled < 0 {
		filled = 0
	}
	if filled > barW {
		filled = barW
	}

	var bar strings.Builder
	if barW > 0 {
		cells, empty := barCells(barW)
		for i := 0; i < barW; i++ {
			if i < filled {
				bar.WriteString(cells[i].Render("━"))
			} else {
				bar.WriteString(empty.Render("─"))
			}
		}
	}

	line := s.HeaderDim.Render(paddedLabel) + " " + bar.String() + " " + s.HeaderDim.Render(pctStr)
	if reset != "" {
		line += " " + s.Hint.Render(reset)
	}
	return line
}

func stateBadge(st session.State, s Styles) string {
	switch st {
	case session.StateThinking:
		return s.StatusBusy.Render("◐")
	case session.StateError:
		return s.ResultError.Render("✗")
	default:
		return s.StatusIdle.Render("●")
	}
}
