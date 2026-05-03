package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ConfirmDialog is a Yep!/Nope modal styled to match QuitDialog. The caller
// supplies the title (hatched) and body lines plus the message to fire on
// confirm; "nope" / esc just close the overlay.
type ConfirmDialog struct {
	styles    Styles
	title     string
	body      []string
	confirm   tea.Msg
	selected  int // 0 = Yep!, 1 = Nope (default focused on Yep)
}

func NewConfirmDialog(s Styles, title string, body []string, confirm tea.Msg) *ConfirmDialog {
	return &ConfirmDialog{
		styles:   s,
		title:    title,
		body:     body,
		confirm:  confirm,
		selected: 0,
	}
}

func (d *ConfirmDialog) SetStyles(s Styles) { d.styles = s }

func (d *ConfirmDialog) Init() tea.Cmd { return nil }

func (d *ConfirmDialog) Update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "left", "h", "shift+tab":
			d.selected = 0
		case "right", "l", "tab":
			d.selected = 1
		case "y", "Y":
			confirm := d.confirm
			return func() tea.Msg { return confirm }
		case "n", "N", "esc", "ctrl+c":
			return func() tea.Msg { return CloseDialogMsg{} }
		case "enter":
			if d.selected == 0 {
				confirm := d.confirm
				return func() tea.Msg { return confirm }
			}
			return func() tea.Msg { return CloseDialogMsg{} }
		}
	}
	return nil
}

func (d *ConfirmDialog) View(width, height int) string {
	boxW := 56
	if boxW > width-4 {
		boxW = width - 4
	}
	if boxW < 36 {
		boxW = 36
	}

	innerW := boxW - 6
	title := HatchedTitle(d.title, innerW, colPrimary, colAccent, d.styles.DialogTitle)

	lines := []string{title, ""}
	for _, l := range d.body {
		lines = append(lines, d.styles.AssistantText.Render(l))
	}
	lines = append(lines, "")

	yepText := "Yep!"
	nopeText := "Nope"
	var yep, nope string
	if d.selected == 0 {
		yep = d.styles.BtnSelected.Render(yepText)
		nope = d.styles.BtnPlain.Render(nopeText)
	} else {
		yep = d.styles.BtnPlain.Render(yepText)
		nope = d.styles.BtnSelected.Render(nopeText)
	}
	btns := yep + "  " + nope
	btnsLine := lipgloss.PlaceHorizontal(boxW-6, lipgloss.Center, btns)
	lines = append(lines, btnsLine)

	hint := d.styles.Hint.Render("←/→ cambiar · enter confirmar · y/n atajos")
	lines = append(lines, "", hint)

	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}
