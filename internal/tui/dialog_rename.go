package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

type RenameDialog struct {
	input   textinput.Model
	current string
	styles  Styles
}

func NewRenameDialog(currentTitle string, s Styles) *RenameDialog {
	ti := textinput.New()
	ti.Placeholder = "nuevo nombre"
	ti.Prompt = "› "
	ti.CharLimit = 64
	ti.SetValue(currentTitle)
	ti.CursorEnd()
	ti.Focus()
	return &RenameDialog{input: ti, current: currentTitle, styles: s}
}

func (d *RenameDialog) SetStyles(s Styles) { d.styles = s }

func (d *RenameDialog) Init() tea.Cmd { return textinput.Blink }

func (d *RenameDialog) Update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc":
			return func() tea.Msg { return CloseDialogMsg{} }
		case "enter":
			name := strings.TrimSpace(d.input.Value())
			if name == "" {
				return nil
			}
			return func() tea.Msg { return RenameSessionMsg{NewTitle: name} }
		}
	}
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	return cmd
}

func (d *RenameDialog) View(width, height int) string {
	boxW := 56
	if boxW > width-4 {
		boxW = width - 4
	}
	if boxW < 30 {
		boxW = 30
	}
	d.input.SetWidth(boxW - 6)

	innerW := boxW - 6
	title := HatchedTitle("Rename", innerW, colPrimary, colAccent, d.styles.DialogTitle)

	lines := []string{
		title,
		"",
		d.styles.Hint.Render("nombre que aparece en el sidebar"),
		"",
		d.input.View(),
		"",
		d.styles.StatusKey.Render("enter") + d.styles.Hint.Render(" guardar  ") +
			d.styles.StatusKey.Render("esc") + d.styles.Hint.Render(" cancelar"),
	}
	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}
