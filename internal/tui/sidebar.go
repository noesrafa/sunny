package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/noesrafa/sunny/internal/claude"
	"github.com/noesrafa/sunny/internal/runs"
	"github.com/noesrafa/sunny/internal/session"
	"github.com/noesrafa/sunny/internal/sysstats"
	"github.com/noesrafa/sunny/internal/terminal"
	"github.com/noesrafa/sunny/internal/usage"
)

const (
	// 33 cols leaves innerW=29 (after the padding-and-safety margin),
	// which is exactly the width of the new dot-matrix SUNNY block
	// (5 cols per letter × 5 letters + 4 single-col gaps = 29).
	sidebarWidth = 33
	sidebarGap   = 3 // empty cols between main column and sidebar
)

func renderSidebar(mgr *session.Manager, runMgr *runs.Manager, paneMgr *terminal.Manager, activePaneActive bool, height int, s Styles, logoFrame int, sys sysstats.Stats) string {
	innerW := sidebarWidth - 4 // padding(0,1) + 1 col safety on each side

	rows := []string{renderLogo(innerW, s, logoFrame), ""}
	rows = append(rows, renderSessionsSection(mgr, activePaneActive, innerW, s)...)
	if section := renderTermsSection(paneMgr, activePaneActive, innerW, s); len(section) > 0 {
		rows = append(rows, "")
		rows = append(rows, section...)
	}
	if section := renderRunsSection(runMgr, innerW, s); len(section) > 0 {
		rows = append(rows, "")
		rows = append(rows, section...)
	}
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

// sectionHeader returns the bold title + the rule below it, the canonical
// way every sidebar block starts.
func sectionHeader(title string, innerW int, s Styles) []string {
	return []string{
		s.HeaderTitle.Render(title),
		s.HeaderSep.Render(strings.Repeat("─", innerW)),
	}
}

func renderSessionsSection(mgr *session.Manager, activePaneActive bool, innerW int, s Styles) []string {
	rows := sectionHeader("sessions", innerW, s)
	if len(mgr.Sessions) == 0 {
		return append(rows, s.Hint.Render("(none)"))
	}
	for i, sess := range mgr.Sessions {
		if i > 0 {
			rows = append(rows, "")
		}
		rows = append(rows, renderSidebarRow(sess, !activePaneActive && i == mgr.Active, s)...)
	}
	return rows
}

func renderTermsSection(paneMgr *terminal.Manager, activePaneActive bool, innerW int, s Styles) []string {
	if paneMgr == nil || paneMgr.Len() == 0 {
		return nil
	}
	rows := sectionHeader("terminals", innerW, s)
	for i, p := range paneMgr.Panes {
		rows = append(rows, renderPaneRow(p, activePaneActive && i == paneMgr.Active, s))
	}
	return rows
}

// renderUsageSection now only surfaces the host machine's cpu/ram. The
// Claude Code rate-limit bars (5h/7d/ctx) used to live here too but they
// turned out to be noisy and rarely actionable — Rafael wanted the panel
// quiet. The buildUsageWidget code path is kept around (dead) in case we
// want to reintroduce a richer view later.
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

// renderRunsSection always shows once a manager exists, so the "press ctrl+u"
// hint surface is discoverable even with zero registered runs.
func renderRunsSection(runMgr *runs.Manager, innerW int, s Styles) []string {
	if runMgr == nil {
		return nil
	}
	rows := sectionHeader("runs", innerW, s)
	all := runMgr.All()
	if len(all) == 0 {
		return append(rows, s.Hint.Render("(none)"))
	}
	for _, r := range all {
		rows = append(rows, renderRunSummary(r, innerW, s))
	}
	return rows
}

func renderShortcutsSection(innerW int, s Styles) []string {
	// Whole row in `colMuted` (no bold, no italic) so the shortcuts read
	// as a quiet reference panel instead of competing for attention with
	// the active session/run/usage rows above.
	g := s.HeaderDim
	return []string{
		s.HeaderSep.Render(strings.Repeat("─", innerW)),
		g.Render("ctrl+n  new chat"),
		g.Render("ctrl+r  rename"),
		g.Render("ctrl+l  reset chat"),
		g.Render("ctrl+d  diff"),
		g.Render("ctrl+u  runs"),
		g.Render("ctrl+k  switch"),
		g.Render("ctrl+s  settings"),
		g.Render("end     bottom"),
		g.Render("tab     next"),
		g.Render("ctrl+w  close"),
		g.Render("esc     quit"),
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
		secs := time.Since(sess.StartedAt).Seconds()
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
		if sess.Turns > 0 {
			line2 = "    " + s.Hint.Render(fmt.Sprintf("%d turns", sess.Turns))
		} else {
			line2 = "    " + s.Hint.Render("ready")
		}
	}
	return []string{line1, line2}
}

// buildUsageWidget tries the rich percentage view first (statusline snapshot)
// and falls back to a status-only line from the in-stream rate_limit_event.
// Mirrors claude-hud's display: context window % + 5h + 7d rate-limit windows.
//
// Freshness window: 24h. We deliberately keep stale snapshots visible
// instead of disappearing the bars whenever the user steps away from
// claude-hud — Claude Code only refreshes the statusline payload on
// activity, so a 10-minute cutoff hides the widget for the rest of the
// day after a single break.
func buildUsageWidget(mgr *session.Manager, innerW int, s Styles) []string {
	if payload, _, err := usage.Read(24 * time.Hour); err == nil && payload != nil {
		var rows []string
		if cw := payload.ContextWindow; cw != nil && cw.UsedPercentage > 0 {
			rows = append(rows, renderProgressBar("ctx", cw.UsedPercentage, "", innerW, s))
		}
		if rl := payload.RateLimits; rl != nil {
			if w := rl.FiveHour; w != nil {
				rows = append(rows, renderProgressBar("5h", w.UsedPercentage, resetHint(w.ResetsAt), innerW, s))
			}
			if w := rl.SevenDay; w != nil {
				rows = append(rows, renderProgressBar("7d", w.UsedPercentage, resetHint(w.ResetsAt), innerW, s))
			}
		}
		if len(rows) > 0 {
			return rows
		}
	}
	if cur := mgr.Current(); cur != nil && cur.RateLimit != nil {
		return renderRateLimitFallback(cur.RateLimit, innerW, s)
	}
	return nil
}

// renderRateLimitFallback paints the in-stream rate_limit_event in the
// same compact one-line style as the snapshot bars. We don't have a
// percentage in this path, so the bar is replaced by a status pill +
// reset hint:
//
//	5h ● ok · 55m
//	7d ● ok · 156h
func renderRateLimitFallback(rl *claude.RateLimitInfo, innerW int, s Styles) []string {
	label := "5h"
	switch rl.RateLimitType {
	case "weekly":
		label = "7d"
	case "five_hour", "":
		label = "5h"
	}
	dot := s.StatusIdle.Render("●")
	statusText := "ok"
	if rl.Status != "" && rl.Status != "allowed" {
		dot = s.StatusBusy.Render("●")
		statusText = rl.Status
	}
	parts := []string{
		s.HeaderDim.Render(fmt.Sprintf("%-3s", label)),
		dot,
		s.HeaderDim.Render(statusText),
	}
	if rs := resetHint(rl.ResetsAt); rs != "" {
		parts = append(parts, s.Hint.Render("· "+rs))
	}
	rows := []string{strings.Join(parts, " ")}
	if rl.IsUsingOverage {
		rows = append(rows, "    "+s.StatusBusy.Render("⚠ overage"))
	}
	_ = innerW // reserved for future bar-fitting; not used in fallback today
	return rows
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

// resetHint formats an absolute reset timestamp into a compact relative
// duration ("3h54m", "12m"), or "" when the timestamp is missing / past.
func resetHint(resetsAt int64) string {
	if resetsAt <= 0 {
		return ""
	}
	d := time.Until(time.Unix(resetsAt, 0))
	if d <= 0 {
		return ""
	}
	return shortDuration(d)
}

func shortDuration(d time.Duration) string {
	if d >= 24*time.Hour {
		// Long resets (7d window) — collapse to whole hours, no minutes.
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	if d >= time.Hour {
		hours := int(d.Hours())
		mins := int(d.Minutes()) - hours*60
		if mins == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh%dm", hours, mins)
	}
	if d >= time.Minute {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func renderPaneRow(p *terminal.Pane, active bool, s Styles) string {
	var icon string
	if p.Alive() {
		icon = s.StatusIdle.Render("▶")
	} else {
		icon = s.Hint.Render("□")
	}
	indicator := " "
	titleStyle := s.AssistantText
	if active {
		indicator = s.UserPrompt.Render("▎")
		titleStyle = s.AssistantText.Bold(true)
	}
	name := p.Title
	maxLen := sidebarWidth - 8
	if len(name) > maxLen && maxLen > 0 {
		name = name[:maxLen-1] + "…"
	}
	return indicator + icon + " " + titleStyle.Render(name)
}

func renderRunSummary(r *runs.Run, innerW int, s Styles) string {
	var icon string
	switch r.Status {
	case runs.StatusRunning:
		icon = s.StatusIdle.Render("●")
	case runs.StatusCrashed:
		icon = s.ResultError.Render("✗")
	default:
		icon = s.Hint.Render("○")
	}
	name := r.Name
	maxName := innerW - 6
	if maxName < 4 {
		maxName = 4
	}
	if len(name) > maxName {
		name = name[:maxName-1] + "…"
	}
	return " " + icon + " " + s.AssistantText.Render(name)
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
