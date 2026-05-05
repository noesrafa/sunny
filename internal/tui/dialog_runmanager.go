package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noesrafa/sunny/internal/client"
)

// RunManagerDialog is the Ctrl+U modal: per-peer list of background
// services with status pills + create/edit/delete + start/stop/
// restart + jump-to-logs. Receives runsLoadedMsg and
// runActionFailedMsg from the parent so it stays in lockstep with
// the model's per-peer state.
type RunManagerDialog struct {
	host   string
	runs   []client.Run
	sel    int
	err    string
	styles Styles
}

func NewRunManagerDialog(host string, runs []client.Run, s Styles) *RunManagerDialog {
	return &RunManagerDialog{host: host, runs: runs, styles: s}
}

func (d *RunManagerDialog) SetStyles(s Styles) { d.styles = s }
func (d *RunManagerDialog) Init() tea.Cmd      { return nil }

func (d *RunManagerDialog) Update(msg tea.Msg) tea.Cmd {
	switch v := msg.(type) {
	case runsLoadedMsg:
		// Parent already stored the runs; we just track the slice
		// for our own render. Stale host messages are ignored so
		// background fetches for other peers don't clobber us.
		if v.Host != d.host || v.Err != nil || v.Runs == nil {
			return nil
		}
		d.runs = v.Runs
		if d.sel >= len(d.runs) {
			d.sel = 0
		}
		return nil
	case runActionFailedMsg:
		if v.Host != d.host {
			return nil
		}
		d.err = fmt.Sprintf("%s %s: %s", v.Action, shortID(v.RunID), v.Err.Error())
		return nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "esc", "ctrl+c":
		return func() tea.Msg { return CloseDialogMsg{} }
	case "up", "ctrl+p", "k":
		if d.sel > 0 {
			d.sel--
		}
		return nil
	case "down", "ctrl+n", "j":
		if d.sel < len(d.runs)-1 {
			d.sel++
		}
		return nil
	case "n":
		return func() tea.Msg { return OpenRunFormMsg{} }
	}
	if len(d.runs) == 0 {
		return nil
	}
	cur := d.runs[d.sel]
	switch k.String() {
	case "e":
		id := cur.ID
		return func() tea.Msg { return OpenRunFormMsg{ID: id} }
	case "l":
		id := cur.ID
		return func() tea.Msg { return OpenRunLogsMsg{ID: id} }
	case "s", " ", "enter":
		// Toggle: running → stop, anything else → start.
		id := cur.ID
		if cur.State.Status == client.RunRunning {
			return func() tea.Msg { return StopRunMsg{ID: id} }
		}
		return func() tea.Msg { return StartRunMsg{ID: id} }
	case "r":
		id := cur.ID
		return func() tea.Msg { return RestartRunMsg{ID: id} }
	case "d":
		id := cur.ID
		body := []string{fmt.Sprintf("¿Borrar el run \"%s\"?", cur.Name)}
		if cur.State.Status == client.RunRunning {
			body = append(body, "", "⚠ está corriendo — se matará primero")
		}
		confirm := NewConfirmDialog(d.styles, "borrar run", body, DeleteRunMsg{ID: id})
		return func() tea.Msg { return OpenSubDialogMsg{Dialog: confirm} }
	}
	return nil
}

func (d *RunManagerDialog) View(width, height int) string {
	boxW := 76
	if boxW > width-4 {
		boxW = width - 4
	}
	if boxW < 50 {
		boxW = 50
	}
	innerW := boxW - 6
	listH := height - 14
	if listH > 16 {
		listH = 16
	}
	if listH < 5 {
		listH = 5
	}

	titleText := "Runs"
	if d.host != "" && d.host != "local" {
		titleText += " · " + d.host
	}
	title := HatchedTitle(titleText, innerW, colPrimary, colAccent, d.styles.DialogTitle)

	hints := d.styles.StatusKey.Render("enter/s") + d.styles.Hint.Render(" start/stop  ") +
		d.styles.StatusKey.Render("r") + d.styles.Hint.Render(" restart  ") +
		d.styles.StatusKey.Render("l") + d.styles.Hint.Render(" logs  ") +
		d.styles.StatusKey.Render("n") + d.styles.Hint.Render(" new  ") +
		d.styles.StatusKey.Render("e") + d.styles.Hint.Render(" edit  ") +
		d.styles.StatusKey.Render("d") + d.styles.Hint.Render(" delete  ") +
		d.styles.StatusKey.Render("esc") + d.styles.Hint.Render(" cerrar")

	lines := []string{title, ""}
	lines = append(lines, d.renderList(listH, innerW)...)
	if d.err != "" {
		lines = append(lines, "", d.styles.ResultError.Render("✗ "+d.err))
	}
	lines = append(lines, "", hints)

	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}

func (d *RunManagerDialog) renderList(maxRows, innerW int) []string {
	if len(d.runs) == 0 {
		return []string{
			"  " + d.styles.Hint.Render("(no hay runs — pulsá 'n' para crear uno)"),
		}
	}
	rows := make([]string, 0, len(d.runs))
	for i, r := range d.runs {
		rows = append(rows, d.renderRow(i, r, innerW))
	}
	if len(rows) > maxRows {
		// Window the list around the selection.
		start := d.sel - maxRows/2
		if start < 0 {
			start = 0
		}
		end := start + maxRows
		if end > len(rows) {
			end = len(rows)
			start = end - maxRows
		}
		rows = rows[start:end]
	}
	return rows
}

func (d *RunManagerDialog) renderRow(i int, r client.Run, innerW int) string {
	pill := statusPill(r.State.Status, d.styles)
	uptime := uptimeFor(r.State)
	name := r.Name
	if i == d.sel {
		marker := d.styles.UserPrompt.Render("›")
		nameStyled := d.styles.HeaderTitle.Render(name)
		return fmt.Sprintf("%s %s %s  %s  %s",
			marker,
			pill,
			nameStyled,
			d.styles.Hint.Render(uptime),
			d.styles.Hint.Render(truncate(r.Command, innerW-len(name)-22)),
		)
	}
	return fmt.Sprintf("  %s %s  %s  %s",
		pill,
		d.styles.AssistantText.Render(name),
		d.styles.Hint.Render(uptime),
		d.styles.Hint.Render(truncate(r.Command, innerW-len(name)-22)),
	)
}

// statusPill returns a colored single-character indicator for a
// run's current status. Same vocabulary as the sidebar so the user
// learns one mapping.
func statusPill(s client.RunStatus, st Styles) string {
	switch s {
	case client.RunRunning:
		return lipgloss.NewStyle().Foreground(colSuccess).Bold(true).Render("●")
	case client.RunFailed:
		return lipgloss.NewStyle().Foreground(colDanger).Bold(true).Render("✗")
	case client.RunExited:
		return lipgloss.NewStyle().Foreground(colSecondary).Render("○")
	default:
		return st.Hint.Render("○")
	}
}

// uptimeFor renders "Xs", "Xm", or "Xh" since the run started
// (when running) or since it exited (otherwise). Empty for runs
// that have never been started.
func uptimeFor(s client.RunState) string {
	if s.Status == client.RunRunning && s.StartedAt != nil {
		return durShort(time.Since(*s.StartedAt))
	}
	if s.ExitedAt != nil {
		return durShort(time.Since(*s.ExitedAt))
	}
	return ""
}

func durShort(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

func truncate(s string, max int) string {
	if max < 1 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	if max < 4 {
		return s[:max]
	}
	return s[:max-1] + "…"
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8] + "…"
	}
	return id
}
