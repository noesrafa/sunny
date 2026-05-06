package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// HelpDialog is the keyboard cheat sheet that lives behind ctrl+/.
// All shortcuts moved here in v0.19 so the sidebar doesn't have to
// devote ~10 rows to a list users learn once and then ignore. The
// sidebar keeps only the three most-used keys plus a "ctrl+/  more"
// hint; this dialog is the long form.
type HelpDialog struct {
	styles Styles
}

func NewHelpDialog(s Styles) *HelpDialog { return &HelpDialog{styles: s} }

func (d *HelpDialog) SetStyles(s Styles)       { d.styles = s }
func (d *HelpDialog) Init() tea.Cmd            { return nil }
func (d *HelpDialog) Update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc", "ctrl+c", "q", "ctrl+/", "ctrl+_":
			return func() tea.Msg { return CloseDialogMsg{} }
		}
	}
	return nil
}

func (d *HelpDialog) View(width, height int) string {
	boxW := 56
	if boxW > width-4 {
		boxW = width - 4
	}
	if boxW < 40 {
		boxW = 40
	}
	innerW := boxW - 6

	title := HatchedTitle("Shortcuts", innerW, colPrimary, colAccent, d.styles.DialogTitle)

	type row struct{ key, desc string }
	type group struct {
		header string
		rows   []row
	}
	groups := []group{
		{"chat", []row{
			{"enter", "send"},
			{"ctrl+j / alt+enter", "newline"},
			{"ctrl+c", "cancel current turn"},
			{"ctrl+g", "regenerate last reply"},
			{"ctrl+l", "reset chat (new conversation)"},
			{"ctrl+v", "paste image/text"},
		}},
		{"sessions", []row{
			{"ctrl+n", "new chat"},
			{"ctrl+a", "agents"},
			{"ctrl+r", "rename current"},
			{"ctrl+w", "close current"},
			{"tab / shift+tab", "next / prev"},
			{"ctrl+k", "tab picker"},
		}},
		{"peers", []row{
			{"ctrl+1..9", "jump to peer N"},
		}},
		{"app", []row{
			{"ctrl+y", "secrets / api keys"},
			{"ctrl+s", "settings (theme)"},
			{"ctrl+d", "diff viewer"},
			{"ctrl+/", "this dialog"},
			{"esc", "quit (with confirm)"},
		}},
		{"scroll", []row{
			{"pgup / pgdn", "scroll chat"},
			{"home / end", "top / bottom"},
		}},
	}

	keyW := 0
	for _, g := range groups {
		for _, r := range g.rows {
			if w := lipgloss.Width(r.key); w > keyW {
				keyW = w
			}
		}
	}
	if keyW > innerW-3 {
		keyW = innerW - 3
	}

	var lines []string
	lines = append(lines, title, "")
	for i, g := range groups {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, d.styles.HeaderTitle.Render(g.header))
		for _, r := range g.rows {
			key := lipgloss.NewStyle().Width(keyW).Foreground(colSecondary).Render(r.key)
			desc := d.styles.HeaderDim.Render(r.desc)
			lines = append(lines, key+"  "+desc)
		}
	}

	lines = append(lines, "", d.styles.Hint.Render("esc / ctrl+/ to close"))

	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}
