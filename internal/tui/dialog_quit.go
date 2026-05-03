package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type QuitDialog struct {
	styles      Styles
	sessions    int
	anyThinking bool
	selected    int // 0 = Yep!, 1 = Nope (default focused on Yep)
}

func NewQuitDialog(s Styles, sessions int, anyThinking bool) *QuitDialog {
	return &QuitDialog{
		styles:      s,
		sessions:    sessions,
		anyThinking: anyThinking,
		selected:    0,
	}
}

func (d *QuitDialog) SetStyles(s Styles) { d.styles = s }

func (d *QuitDialog) Init() tea.Cmd { return nil }

func (d *QuitDialog) Update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "left", "h", "shift+tab":
			d.selected = 0
		case "right", "l", "tab":
			d.selected = 1
		case "y", "Y":
			return func() tea.Msg { return ConfirmQuitMsg{} }
		case "n", "N", "esc", "ctrl+c":
			return func() tea.Msg { return CloseDialogMsg{} }
		case "enter":
			if d.selected == 0 {
				return func() tea.Msg { return ConfirmQuitMsg{} }
			}
			return func() tea.Msg { return CloseDialogMsg{} }
		}
	}
	return nil
}

func (d *QuitDialog) View(width, height int) string {
	boxW := 50
	if boxW > width-4 {
		boxW = width - 4
	}
	if boxW < 36 {
		boxW = 36
	}

	// Crush-style hatched title bar.
	innerW := boxW - 6 // box border(2) + padding(4)
	title := HatchedTitle("Quit?", innerW, colPrimary, colAccent, d.styles.DialogTitle)

	var lines []string
	lines = append(lines, title, "")
	lines = append(lines, d.styles.AssistantText.Render("¿Salir de sunny?"), "")

	if d.anyThinking {
		lines = append(lines, d.styles.DialogWarning.Render("⚠ hay sesiones aún pensando"))
	}
	stats := fmt.Sprintf("%d sesiones", d.sessions)
	lines = append(lines, d.styles.HeaderDim.Render(stats), "")

	// Buttons row
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
