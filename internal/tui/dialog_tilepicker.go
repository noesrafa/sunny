package tui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// TileItem describes one switchable tab in the picker.
type TileItem struct {
	Kind   string // "claude" | "pane"
	Index  int    // index inside its manager
	Label  string // shown in the list
	Detail string // e.g. cwd or command
	Active bool
}

// TilePickerDialog is Crush-inspired command-palette: textinput on top,
// filtered list below. Substring match on label+detail (case-insensitive).
type TilePickerDialog struct {
	all      []TileItem
	filtered []int // indices into all
	selected int
	input    textinput.Model
	styles   Styles
}

func NewTilePickerDialog(items []TileItem, s Styles) *TilePickerDialog {
	ti := textinput.New()
	ti.Placeholder = "search tabs…"
	ti.Prompt = "› "
	ti.CharLimit = 0
	ti.SetWidth(50)
	ti.Focus()
	d := &TilePickerDialog{all: items, styles: s, input: ti}
	d.refilter()
	// Default selection: the active tab if visible.
	for i, idx := range d.filtered {
		if d.all[idx].Active {
			d.selected = i
			break
		}
	}
	return d
}

func (d *TilePickerDialog) SetStyles(s Styles) { d.styles = s }

func (d *TilePickerDialog) Init() tea.Cmd { return textinput.Blink }

func (d *TilePickerDialog) Update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc", "ctrl+c":
			return func() tea.Msg { return CloseDialogMsg{} }
		case "up", "ctrl+p":
			if d.selected > 0 {
				d.selected--
			}
			return nil
		case "down", "ctrl+n":
			if d.selected < len(d.filtered)-1 {
				d.selected++
			}
			return nil
		case "enter":
			if len(d.filtered) == 0 {
				return nil
			}
			pick := d.all[d.filtered[d.selected]]
			return func() tea.Msg { return SwitchTabMsg{Kind: pick.Kind, Index: pick.Index} }
		}
	}
	prev := d.input.Value()
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	if d.input.Value() != prev {
		d.refilter()
		d.selected = 0
	}
	return cmd
}

func (d *TilePickerDialog) refilter() {
	q := strings.ToLower(strings.TrimSpace(d.input.Value()))
	d.filtered = d.filtered[:0]
	for i, t := range d.all {
		if q == "" || strings.Contains(strings.ToLower(t.Label), q) ||
			strings.Contains(strings.ToLower(t.Detail), q) {
			d.filtered = append(d.filtered, i)
		}
	}
}

func (d *TilePickerDialog) View(width, height int) string {
	boxW := 60
	if boxW > width-4 {
		boxW = width - 4
	}
	if boxW < 40 {
		boxW = 40
	}
	innerW := boxW - 6

	d.input.SetWidth(innerW - 2)
	title := HatchedTitle("Switch Tab", innerW, colPrimary, colSecondary, d.styles.DialogTitle)

	rows := []string{title, "", "  " + d.input.View(), ""}

	// Group sections in the list: claude vs pane.
	max := 12
	if len(d.filtered) < max {
		max = len(d.filtered)
	}
	if len(d.filtered) == 0 {
		rows = append(rows, d.styles.Hint.Render("  (sin resultados)"))
	}
	for i := 0; i < max; i++ {
		t := d.all[d.filtered[i]]
		marker := "  "
		nameStyle := d.styles.AssistantText
		if i == d.selected {
			marker = d.styles.UserPrompt.Render("▎ ")
			nameStyle = d.styles.AssistantText.Bold(true).Background(colBorder)
		}
		kindIcon := d.styles.Hint.Render("◇")
		if t.Kind == "pane" {
			kindIcon = d.styles.StatusIdle.Render("▶")
		}
		line := marker + kindIcon + " " + nameStyle.Render(padRight(t.Label, 18))
		if t.Detail != "" {
			detail := t.Detail
			maxDet := innerW - 26
			if maxDet > 0 && len(detail) > maxDet {
				detail = "…" + detail[len(detail)-(maxDet-1):]
			}
			line += d.styles.Hint.Render(detail)
		}
		rows = append(rows, line)
	}
	if len(d.filtered) > max {
		rows = append(rows, d.styles.Hint.Render(
			"  …"+strconv.Itoa(len(d.filtered)-max)+" more"))
	}

	hints := strings.Join([]string{
		d.styles.StatusKey.Render("↑↓") + d.styles.Hint.Render(" navigate"),
		d.styles.StatusKey.Render("enter") + d.styles.Hint.Render(" switch"),
		d.styles.StatusKey.Render("esc") + d.styles.Hint.Render(" cancel"),
	}, d.styles.Hint.Render(" · "))
	rows = append(rows, "", hints)
	return d.styles.DialogBox.Width(boxW).Render(strings.Join(rows, "\n"))
}

