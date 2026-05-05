package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/noesrafa/sunny/internal/client"
)

// RunFormDialog is the create/edit modal for a single run. Reuses
// DirPicker for the cwd field so the picker behaves identically to
// the new-session dialog (peer-aware, async /fs/list, search).
//
// editID is empty for "create" mode and non-empty when editing —
// the only behavior difference is the message emitted on submit
// (CreateRunMsg vs UpdateRunMsg).
type runFormFocus int

const (
	runFormName runFormFocus = iota
	runFormCwd
	runFormCommand
	numRunFormFocus
)

type RunFormDialog struct {
	host    string
	editID  string
	name    textinput.Model
	command textinput.Model
	picker  *DirPicker
	focus   runFormFocus
	err     string
	styles  Styles
}

// NewRunFormDialog opens the form. existing == nil means create
// mode; non-nil pre-fills the fields with the run's current values
// and switches the submit message to UpdateRunMsg.
func NewRunFormDialog(c *client.Client, host string, existing *client.Run, s Styles) *RunFormDialog {
	name := textinput.New()
	name.Placeholder = "ej. dev-server"
	name.Prompt = "› "
	name.CharLimit = 0
	name.SetWidth(50)

	cmd := textinput.New()
	cmd.Placeholder = "ej. bun dev, npm run dev, cargo run"
	cmd.Prompt = "› "
	cmd.CharLimit = 0
	cmd.SetWidth(50)

	d := &RunFormDialog{
		host:    host,
		name:    name,
		command: cmd,
		styles:  s,
	}
	cwd := ""
	if existing != nil {
		d.editID = existing.ID
		d.name.SetValue(existing.Name)
		d.command.SetValue(existing.Command)
		cwd = existing.Cwd
	}
	d.picker = NewDirPicker(c, cwd, "buscar carpeta…", s)
	// Default focus on name (first field) on open so the user can
	// type immediately without tabbing.
	d.focus = runFormName
	d.applyFocus()
	return d
}

func (d *RunFormDialog) SetStyles(s Styles) {
	d.styles = s
	d.picker.SetStyles(s)
}

func (d *RunFormDialog) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, d.picker.Init())
}

func (d *RunFormDialog) Update(msg tea.Msg) tea.Cmd {
	if _, ok := msg.(dirPickerLoadedMsg); ok {
		return d.picker.Update(msg)
	}
	if v, ok := msg.(runActionFailedMsg); ok && (v.Action == "create" || v.Action == "update") {
		d.err = v.Err.Error()
		return nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "esc":
		return func() tea.Msg { return CloseDialogMsg{} }
	case "tab":
		d.focus = (d.focus + 1) % numRunFormFocus
		d.applyFocus()
		return nil
	case "shift+tab":
		d.focus = (d.focus + numRunFormFocus - 1) % numRunFormFocus
		d.applyFocus()
		return nil
	case "enter":
		// In the picker, Enter is a no-op (descend uses → arrow);
		// the submit owns Enter for the whole form so the user can
		// hit it from any field.
		return d.submit()
	}
	switch d.focus {
	case runFormName:
		var cmd tea.Cmd
		d.name, cmd = d.name.Update(msg)
		return cmd
	case runFormCwd:
		return d.picker.Update(msg)
	case runFormCommand:
		var cmd tea.Cmd
		d.command, cmd = d.command.Update(msg)
		return cmd
	}
	return nil
}

func (d *RunFormDialog) applyFocus() {
	d.name.Blur()
	d.command.Blur()
	d.picker.Blur()
	switch d.focus {
	case runFormName:
		d.name.Focus()
	case runFormCwd:
		d.picker.Focus()
	case runFormCommand:
		d.command.Focus()
	}
}

func (d *RunFormDialog) submit() tea.Cmd {
	name := strings.TrimSpace(d.name.Value())
	cwd := strings.TrimSpace(d.picker.Cwd())
	cmd := strings.TrimSpace(d.command.Value())
	switch {
	case name == "":
		d.err = "nombre requerido"
		d.focus = runFormName
		d.applyFocus()
		return nil
	case cwd == "":
		d.err = "directorio requerido"
		d.focus = runFormCwd
		d.applyFocus()
		return nil
	case d.picker.Loading():
		d.err = "esperando listado del directorio"
		return nil
	case d.picker.LoadErr() != "":
		d.err = "directorio inválido: " + d.picker.LoadErr()
		return nil
	case cmd == "":
		d.err = "comando requerido"
		d.focus = runFormCommand
		d.applyFocus()
		return nil
	}
	if d.editID == "" {
		return func() tea.Msg { return CreateRunMsg{Name: name, Cwd: cwd, Command: cmd} }
	}
	id := d.editID
	return func() tea.Msg { return UpdateRunMsg{ID: id, Name: name, Cwd: cwd, Command: cmd} }
}

func (d *RunFormDialog) View(width, height int) string {
	boxW := 76
	if boxW > width-4 {
		boxW = width - 4
	}
	if boxW < 50 {
		boxW = 50
	}
	innerW := boxW - 6
	d.name.SetWidth(innerW - 2)
	d.command.SetWidth(innerW - 2)
	d.picker.SetSearchWidth(innerW - 2)

	listH := height - 18
	if listH > 10 {
		listH = 10
	}
	if listH < 4 {
		listH = 4
	}

	titleText := "New Run"
	if d.editID != "" {
		titleText = "Edit Run"
	}
	if d.host != "" && d.host != "local" {
		titleText += " · " + d.host
	}
	title := HatchedTitle(titleText, innerW, colPrimary, colAccent, d.styles.DialogTitle)

	cwdHeader := "directorio"
	if cwd := d.picker.Cwd(); cwd != "" {
		cwdHeader += " · " + cwd
	}

	hints := d.styles.StatusKey.Render("enter") + d.styles.Hint.Render(" guardar  ") +
		d.styles.StatusKey.Render("tab") + d.styles.Hint.Render(" siguiente  ") +
		d.styles.StatusKey.Render("esc") + d.styles.Hint.Render(" cancelar")

	lines := []string{
		title, "",
		d.fieldLabel("nombre", d.focus == runFormName),
		"  " + d.name.View(),
		"",
		d.fieldLabel(cwdHeader, d.focus == runFormCwd),
		d.picker.SearchView(),
		d.picker.ListView(listH, innerW),
		d.picker.HintLine(),
		"",
		d.fieldLabel("comando (sh -c)", d.focus == runFormCommand),
		"  " + d.command.View(),
	}
	if d.err != "" {
		lines = append(lines, "", d.styles.ResultError.Render("✗ "+d.err))
	}
	lines = append(lines, "", hints)

	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}

func (d *RunFormDialog) fieldLabel(text string, focused bool) string {
	if focused {
		return d.styles.UserPrompt.Render("▸ ") + d.styles.HeaderTitle.Render(text)
	}
	return "  " + d.styles.HeaderDim.Render(text)
}
