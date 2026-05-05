package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noesrafa/sunny/internal/client"
)

// MonitorManagerDialog is the Ctrl+B modal: per-peer list of
// monitors with enable/disable toggle + jump-to-history. No
// create/edit/delete — the agent owns the YAML files.
type MonitorManagerDialog struct {
	host     string
	monitors []client.Monitor
	sel      int
	err      string
	styles   Styles
}

func NewMonitorManagerDialog(host string, mons []client.Monitor, s Styles) *MonitorManagerDialog {
	return &MonitorManagerDialog{host: host, monitors: mons, styles: s}
}

func (d *MonitorManagerDialog) SetStyles(s Styles) { d.styles = s }
func (d *MonitorManagerDialog) Init() tea.Cmd      { return nil }

func (d *MonitorManagerDialog) Update(msg tea.Msg) tea.Cmd {
	switch v := msg.(type) {
	case monitorsLoadedMsg:
		// Stale messages for other peers are ignored. v.Err == nil
		// AND v.Monitors == nil happens when the appmsg layer
		// triggers a re-fetch via this same shape; ignore those.
		if v.Host != d.host || v.Err != nil || v.Monitors == nil {
			return nil
		}
		d.monitors = v.Monitors
		if d.sel >= len(d.monitors) {
			d.sel = 0
		}
		return nil
	case monitorActionFailedMsg:
		if v.Host != d.host {
			return nil
		}
		d.err = fmt.Sprintf("%s %s: %s", v.Action, v.Name, v.Err.Error())
		return nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "esc", "ctrl+c":
		return func() tea.Msg { return CloseDialogMsg{} }
	case "up", "k", "ctrl+p":
		if d.sel > 0 {
			d.sel--
		}
		return nil
	case "down", "j", "ctrl+n":
		if d.sel < len(d.monitors)-1 {
			d.sel++
		}
		return nil
	}
	if len(d.monitors) == 0 {
		return nil
	}
	cur := d.monitors[d.sel]
	switch k.String() {
	case "s", " ":
		name, enabled := cur.Name, !cur.Enabled
		return func() tea.Msg { return ToggleMonitorMsg{Name: name, Enabled: enabled} }
	case "h", "enter":
		name := cur.Name
		return func() tea.Msg { return OpenMonitorHistoryMsg{Name: name} }
	}
	return nil
}

func (d *MonitorManagerDialog) View(width, height int) string {
	boxW := 80
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

	titleText := "Monitors"
	if d.host != "" && d.host != "local" {
		titleText += " · " + d.host
	}
	title := HatchedTitle(titleText, innerW, colPrimary, colAccent, d.styles.DialogTitle)

	hints := d.styles.StatusKey.Render("space/s") + d.styles.Hint.Render(" toggle  ") +
		d.styles.StatusKey.Render("enter/h") + d.styles.Hint.Render(" history  ") +
		d.styles.StatusKey.Render("esc") + d.styles.Hint.Render(" cerrar")

	hintAgent := d.styles.Hint.Render("(monitors are managed by the agent — write/edit ~/.sunny/monitors/<name>.yaml)")

	lines := []string{title, "", hintAgent, ""}
	lines = append(lines, d.renderList(listH, innerW)...)
	if d.err != "" {
		lines = append(lines, "", d.styles.ResultError.Render("✗ "+d.err))
	}
	lines = append(lines, "", hints)
	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}

func (d *MonitorManagerDialog) renderList(maxRows, innerW int) []string {
	if len(d.monitors) == 0 {
		return []string{"  " + d.styles.Hint.Render("(ningún monitor — el agente puede crear uno en ~/.sunny/monitors/)")}
	}
	rows := make([]string, 0, len(d.monitors))
	for i, mon := range d.monitors {
		rows = append(rows, d.renderRow(i, mon, innerW))
	}
	if len(rows) > maxRows {
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

func (d *MonitorManagerDialog) renderRow(i int, mon client.Monitor, innerW int) string {
	pill := monitorPill(mon, d.styles)
	state := "off"
	if mon.Enabled {
		state = "on "
	}
	when := ""
	if !mon.LastFire.IsZero() {
		when = "fired " + durShort(time.Since(mon.LastFire)) + " ago"
	} else if mon.Enabled {
		when = "waiting…"
	}
	if i == d.sel {
		marker := d.styles.UserPrompt.Render("›")
		return fmt.Sprintf("%s %s [%s] %s  %s  %s",
			marker,
			pill,
			d.styles.Hint.Render(state),
			d.styles.HeaderTitle.Render(mon.Name),
			d.styles.Hint.Render(mon.Interval),
			d.styles.Hint.Render(when),
		)
	}
	style := d.styles.AssistantText
	if !mon.Enabled {
		style = d.styles.HeaderDim
	}
	return fmt.Sprintf("  %s [%s] %s  %s  %s",
		pill,
		d.styles.Hint.Render(state),
		style.Render(mon.Name),
		d.styles.Hint.Render(mon.Interval),
		d.styles.Hint.Render(when),
	)
}

func monitorPill(mon client.Monitor, st Styles) string {
	if !mon.Enabled {
		return st.Hint.Render("○")
	}
	if mon.LastErr != "" {
		return lipgloss.NewStyle().Foreground(colDanger).Bold(true).Render("✗")
	}
	if mon.Running {
		return lipgloss.NewStyle().Foreground(colSuccess).Bold(true).Render("●")
	}
	return st.Hint.Render("○")
}
