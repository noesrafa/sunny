package tui

import (
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// runEditFocus enumerates the focusable fields in the new-run form.
type runEditFocus int

const (
	focusRunName runEditFocus = iota
	focusRunCommand
	focusRunCwd
	numRunEditFocus
)

// RunEditDialog is the "new run" form. Three fields, navigable with tab /
// shift-tab. The cwd field is a full directory picker (same UX as
// NewSessionDialog) so users don't have to type or paste paths — they
// browse to the folder, hit tab, and submit.
type RunEditDialog struct {
	name    textinput.Model
	command textinput.Model
	cwd     *dirPicker

	styles Styles
	focus  runEditFocus
	err    string
}

func NewRunEditDialog(defaultCwd string, s Styles) *RunEditDialog {
	name := textinput.New()
	name.Placeholder = "e.g. web"
	name.Prompt = "› "
	name.CharLimit = 0

	command := textinput.New()
	command.Placeholder = "e.g. bun run dev"
	command.Prompt = "› "
	command.CharLimit = 0

	d := &RunEditDialog{
		name:    name,
		command: command,
		cwd:     newDirPicker(defaultCwd, s),
		styles:  s,
		focus:   focusRunName,
	}
	d.applyFocus()
	return d
}

// SetStyles refreshes the cached palette and propagates to the embedded
// dirpicker, which keeps its own copy.
func (d *RunEditDialog) SetStyles(s Styles) {
	d.styles = s
	if d.cwd != nil {
		d.cwd.setStyles(s)
	}
}

func (d *RunEditDialog) Init() tea.Cmd { return textinput.Blink }

func (d *RunEditDialog) Update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc":
			return func() tea.Msg { return CloseDialogMsg{} }
		case "tab":
			d.focus = (d.focus + 1) % numRunEditFocus
			d.applyFocus()
			return nil
		case "shift+tab":
			d.focus = (d.focus + numRunEditFocus - 1) % numRunEditFocus
			d.applyFocus()
			return nil
		case "enter":
			// On the picker, enter doesn't submit until we're past it —
			// users often want to press enter to "lock in" the current
			// folder and move to the form. Match the cwd to the picker
			// state and let the user tab back / re-enter to submit.
			if d.focus == focusRunCwd {
				return d.submit()
			}
			return d.submit()
		}
	}

	switch d.focus {
	case focusRunName:
		var cmd tea.Cmd
		d.name, cmd = d.name.Update(msg)
		return cmd
	case focusRunCommand:
		var cmd tea.Cmd
		d.command, cmd = d.command.Update(msg)
		return cmd
	case focusRunCwd:
		cmd, _ := d.cwd.Update(msg)
		return cmd
	}
	return nil
}

func (d *RunEditDialog) applyFocus() {
	d.name.Blur()
	d.command.Blur()
	d.cwd.Blur()
	switch d.focus {
	case focusRunName:
		d.name.Focus()
	case focusRunCommand:
		d.command.Focus()
	case focusRunCwd:
		d.cwd.Focus()
	}
}

func (d *RunEditDialog) submit() tea.Cmd {
	name := strings.TrimSpace(d.name.Value())
	cmd := strings.TrimSpace(d.command.Value())
	cwd := strings.TrimSpace(d.cwd.Cwd())
	if name == "" {
		d.err = "name requerido"
		d.focus = focusRunName
		d.applyFocus()
		return nil
	}
	if cmd == "" {
		d.err = "comando requerido"
		d.focus = focusRunCommand
		d.applyFocus()
		return nil
	}
	if cwd != "" {
		abs, err := filepath.Abs(cwd)
		if err != nil {
			d.err = err.Error()
			return nil
		}
		info, statErr := os.Stat(abs)
		if statErr != nil || !info.IsDir() {
			d.err = "cwd no es directorio: " + abs
			return nil
		}
		cwd = abs
	}
	return func() tea.Msg {
		return CreateRunMsg{Name: name, Command: cmd, Cwd: cwd}
	}
}

func (d *RunEditDialog) View(width, height int) string {
	boxW := 72
	if boxW > width-4 {
		boxW = width - 4
	}
	if boxW < 40 {
		boxW = 40
	}
	innerW := boxW - 6

	listH := height - 22
	if listH > 10 {
		listH = 10
	}
	if listH < 5 {
		listH = 5
	}

	taW := innerW - 4
	d.name.SetWidth(taW)
	d.command.SetWidth(taW)

	title := HatchedTitle("New Run", innerW, colPrimary, colAccent, d.styles.DialogTitle)

	nameLabel := d.fieldLabel("name", d.focus == focusRunName)
	nameView := "  " + d.name.View()

	cmdLabel := d.fieldLabel("command", d.focus == focusRunCommand)
	cmdView := "  " + d.command.View()

	cwdLabel := d.fieldLabel("cwd · "+d.cwd.Cwd(), d.focus == focusRunCwd)
	cwdView := d.cwd.Render(listH, innerW)

	hints := strings.Join([]string{
		d.styles.StatusKey.Render("enter") + d.styles.Hint.Render(" save"),
		d.styles.StatusKey.Render("tab") + d.styles.Hint.Render(" siguiente campo"),
		d.styles.StatusKey.Render("→/←") + d.styles.Hint.Render(" navegar carpetas"),
		d.styles.StatusKey.Render("esc") + d.styles.Hint.Render(" cancelar"),
	}, d.styles.Hint.Render(" · "))

	lines := []string{
		title, "",
		nameLabel,
		nameView,
		"",
		cmdLabel,
		cmdView,
		"",
		cwdLabel,
		cwdView,
	}
	if d.err != "" {
		lines = append(lines, "", d.styles.ResultError.Render("✗ "+d.err))
	}
	lines = append(lines, "", hints)

	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}

func (d *RunEditDialog) fieldLabel(text string, focused bool) string {
	if focused {
		return d.styles.UserPrompt.Render("▸ ") + d.styles.HeaderTitle.Render(text)
	}
	return "  " + d.styles.HeaderDim.Render(text)
}
