package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// AgentRenameDialog edits just an agent's display name. The agent's
// id is opaque and immutable, so renaming is purely a Name patch —
// no file moves, no journal rewrites, no broken references.
type AgentRenameDialog struct {
	id     string
	input  textinput.Model
	styles Styles
	err    string
}

// SubmitAgentRenameMsg is the form's output. The root model translates
// it into a PATCH /agents/{id} {"name":"…"} call.
type SubmitAgentRenameMsg struct {
	ID   string
	Name string
}

func NewAgentRenameDialog(id, currentName string, s Styles) *AgentRenameDialog {
	ti := textinput.New()
	ti.Placeholder = "nuevo nombre"
	ti.Prompt = "› "
	ti.CharLimit = 64
	ti.SetValue(currentName)
	ti.CursorEnd()
	ti.Focus()
	return &AgentRenameDialog{id: id, input: ti, styles: s}
}

func (d *AgentRenameDialog) SetStyles(s Styles) { d.styles = s }

func (d *AgentRenameDialog) Init() tea.Cmd { return textinput.Blink }

func (d *AgentRenameDialog) Update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc":
			return func() tea.Msg { return CloseDialogMsg{} }
		case "enter":
			name := strings.TrimSpace(d.input.Value())
			if name == "" {
				d.err = "name is required"
				return nil
			}
			id := d.id
			return func() tea.Msg { return SubmitAgentRenameMsg{ID: id, Name: name} }
		}
	}
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	return cmd
}

func (d *AgentRenameDialog) View(width, height int) string {
	boxW := 56
	if boxW > width-4 {
		boxW = width - 4
	}
	if boxW < 30 {
		boxW = 30
	}
	d.input.SetWidth(boxW - 6)

	innerW := boxW - 6
	title := HatchedTitle("Rename agent", innerW, colPrimary, colAccent, d.styles.DialogTitle)

	lines := []string{
		title,
		"",
		d.styles.Hint.Render("display name — the id stays the same"),
		"",
		d.input.View(),
	}
	if d.err != "" {
		lines = append(lines, "", "  "+d.styles.ResultError.Render("✗ "+d.err))
	}
	lines = append(lines,
		"",
		d.styles.StatusKey.Render("enter")+d.styles.Hint.Render(" save  ")+
			d.styles.StatusKey.Render("esc")+d.styles.Hint.Render(" cancel"),
	)
	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}
