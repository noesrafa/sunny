package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/noesrafa/sunny/internal/client"
)

// MonitorHistoryDialog tails the last 100 history entries for one
// monitor. Each entry shows the rule that fired, the matched item
// summary, and each action's result/error. Read-only; ESC closes.
type MonitorHistoryDialog struct {
	host    string
	name    string
	entries []client.MonitorHistoryEntry
	err     string
	loading bool
	styles  Styles
	scroll  int
}

func NewMonitorHistoryDialog(host, name string, s Styles) *MonitorHistoryDialog {
	return &MonitorHistoryDialog{host: host, name: name, styles: s, loading: true}
}

func (d *MonitorHistoryDialog) SetStyles(s Styles) { d.styles = s }
func (d *MonitorHistoryDialog) Init() tea.Cmd      { return nil }

func (d *MonitorHistoryDialog) Update(msg tea.Msg) tea.Cmd {
	switch v := msg.(type) {
	case monitorHistoryLoadedMsg:
		if v.Host != d.host || v.Name != d.name {
			return nil
		}
		d.loading = false
		if v.Err != nil {
			d.err = v.Err.Error()
			return nil
		}
		d.err = ""
		d.entries = v.Entries
		return nil
	}
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc", "ctrl+c":
			return func() tea.Msg { return CloseDialogMsg{} }
		case "up", "k":
			if d.scroll > 0 {
				d.scroll--
			}
		case "down", "j":
			d.scroll++
		case "g":
			d.scroll = 0
		case "G":
			d.scroll = 1 << 30
		}
	}
	return nil
}

func (d *MonitorHistoryDialog) View(width, height int) string {
	boxW := width - 4
	if boxW < 60 {
		boxW = 60
	}
	if boxW > 140 {
		boxW = 140
	}
	innerW := boxW - 6
	listH := height - 8
	if listH < 8 {
		listH = 8
	}

	title := HatchedTitle("History · "+d.name, innerW, colPrimary, colAccent, d.styles.DialogTitle)
	hints := d.styles.StatusKey.Render("↑↓") + d.styles.Hint.Render(" scroll  ") +
		d.styles.StatusKey.Render("g/G") + d.styles.Hint.Render(" top/bottom  ") +
		d.styles.StatusKey.Render("esc") + d.styles.Hint.Render(" cerrar")

	body := d.renderBody(listH, innerW)

	lines := []string{title, "", body}
	if d.err != "" {
		lines = append(lines, "", d.styles.ResultError.Render("✗ "+d.err))
	}
	lines = append(lines, "", hints)
	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}

func (d *MonitorHistoryDialog) renderBody(rows, innerW int) string {
	if d.loading {
		return "  " + d.styles.Hint.Render("loading…")
	}
	if len(d.entries) == 0 {
		return "  " + d.styles.Hint.Render("(sin firings — el monitor todavía no ha disparado ninguna regla)")
	}
	// Newest first.
	var blocks []string
	for i := len(d.entries) - 1; i >= 0; i-- {
		blocks = append(blocks, d.renderEntry(d.entries[i], innerW))
	}
	flat := strings.Split(strings.Join(blocks, "\n"), "\n")
	if d.scroll > len(flat)-rows {
		d.scroll = len(flat) - rows
	}
	if d.scroll < 0 {
		d.scroll = 0
	}
	end := d.scroll + rows
	if end > len(flat) {
		end = len(flat)
	}
	out := flat[d.scroll:end]
	for len(out) < rows {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

func (d *MonitorHistoryDialog) renderEntry(e client.MonitorHistoryEntry, innerW int) string {
	ts := d.styles.Hint.Render(e.Ts.Local().Format("15:04:05"))
	rule := d.styles.HeaderTitle.Render(e.Rule)
	itemPreview := previewItem(e.Item, innerW-20)
	header := fmt.Sprintf("%s  %s  %s", ts, rule, d.styles.Hint.Render(itemPreview))
	rows := []string{header}
	for _, a := range e.Actions {
		mark := d.styles.AssistantText.Render("→")
		typ := d.styles.HeaderDim.Render(a.Type)
		var detail string
		if a.Err != "" {
			detail = d.styles.ResultError.Render("✗ " + a.Err)
		} else {
			detail = d.styles.AssistantText.Render(truncate(fmt.Sprint(a.Result), innerW-12))
		}
		rows = append(rows, fmt.Sprintf("    %s %s  %s", mark, typ, detail))
	}
	return strings.Join(rows, "\n")
}

func previewItem(item map[string]any, max int) string {
	if text, ok := item["text"].(string); ok && text != "" {
		return truncate(text, max)
	}
	// Fallback: serialize to JSON for opaque sources.
	j, _ := json.Marshal(item)
	return truncate(string(j), max)
}
