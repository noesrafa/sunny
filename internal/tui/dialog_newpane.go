package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/noesrafa/sunny/internal/favs"
)

const (
	paneFieldName = iota
	paneFieldCommand
	paneFieldCwd
)

type NewPaneDialog struct {
	form      formInputs
	styles    Styles
	err       string
	saved     string // ephemeral "saved as fav" message
	favorites []favs.Favorite
}

func NewNewPaneDialog(defaultCwd string, s Styles) *NewPaneDialog {
	defaultName := "shell"
	if defaultCwd != "" {
		defaultName = filepath.Base(defaultCwd)
	}
	favorites, _ := favs.Load()
	return &NewPaneDialog{
		form: newFormInputs([]inputSpec{
			{Placeholder: defaultName, Value: defaultName},
			{Placeholder: "(blank → " + defaultShell() + ")"},
			{Placeholder: defaultCwd, Value: defaultCwd},
		}),
		styles:    s,
		favorites: favorites,
	}
}

func (d *NewPaneDialog) SetStyles(s Styles) { d.styles = s }

func (d *NewPaneDialog) Init() tea.Cmd { return textinput.Blink }

func (d *NewPaneDialog) Update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok {
		s := k.String()
		// Number keys 1-9 → instantly fill from favorites at that index.
		if len(s) == 1 && s >= "1" && s <= "9" {
			idx := int(s[0] - '1')
			if idx < len(d.favorites) {
				d.applyFavorite(d.favorites[idx])
				return nil
			}
		}
		switch s {
		case "esc":
			return func() tea.Msg { return CloseDialogMsg{} }
		case "tab":
			d.form.advance(1)
			return nil
		case "shift+tab":
			d.form.advance(-1)
			return nil
		case "ctrl+f":
			d.saveFavorite()
			return nil
		case "enter":
			return d.submit()
		}
	}
	return d.form.updateActive(msg)
}

func (d *NewPaneDialog) applyFavorite(f favs.Favorite) {
	d.form.inputs[paneFieldName].SetValue(f.Name)
	d.form.inputs[paneFieldCommand].SetValue(f.Command)
	if f.Cwd != "" {
		d.form.inputs[paneFieldCwd].SetValue(f.Cwd)
	}
	d.saved = ""
}

func (d *NewPaneDialog) saveFavorite() {
	name := strings.TrimSpace(d.form.value(paneFieldName))
	cmd := strings.TrimSpace(d.form.value(paneFieldCommand))
	if name == "" || cmd == "" {
		d.err = "fill name+command before saving fav"
		return
	}
	updated, err := favs.Add(name, cmd, strings.TrimSpace(d.form.value(paneFieldCwd)))
	if err != nil {
		d.err = err.Error()
		return
	}
	d.favorites = updated
	d.saved = "★ saved as " + name
	d.err = ""
}

func (d *NewPaneDialog) submit() tea.Cmd {
	name := strings.TrimSpace(d.form.value(paneFieldName))
	command := strings.TrimSpace(d.form.value(paneFieldCommand))
	cwd := strings.TrimSpace(d.form.value(paneFieldCwd))
	if name == "" {
		name = "shell"
	}
	if command == "" {
		command = defaultShell()
	}
	if cwd != "" {
		info, err := os.Stat(cwd)
		if err != nil || !info.IsDir() {
			d.err = "cwd no es directorio: " + cwd
			return nil
		}
	}
	return func() tea.Msg {
		return CreatePaneMsg{Name: name, Command: command, Cwd: cwd}
	}
}

func (d *NewPaneDialog) View(width, height int) string {
	boxW := 76
	if boxW > width-4 {
		boxW = width - 4
	}
	if boxW < 40 {
		boxW = 40
	}
	innerW := boxW - 6
	title := HatchedTitle("New Terminal", innerW, colPrimary, colSecondary, d.styles.DialogTitle)

	d.form.setWidth(innerW - 4)

	lines := []string{title, ""}

	// Favorites quick-pick row: "1 claude · 2 yolo · 3 shell …"
	if len(d.favorites) > 0 {
		var picks []string
		max := len(d.favorites)
		if max > 9 {
			max = 9
		}
		for i := 0; i < max; i++ {
			f := d.favorites[i]
			picks = append(picks, d.styles.StatusKey.Render(fmt.Sprintf("%d", i+1))+
				" "+d.styles.AssistantText.Render(f.Name))
		}
		lines = append(lines,
			d.styles.HeaderDim.Render("favorites"),
			"  "+strings.Join(picks, d.styles.Hint.Render(" · ")),
			"")
	}

	lines = append(lines, formFieldView("name", d.form.focus == paneFieldName, d.form.view(paneFieldName), d.styles)...)
	lines = append(lines, "")
	lines = append(lines, formFieldView("command", d.form.focus == paneFieldCommand, d.form.view(paneFieldCommand), d.styles)...)
	lines = append(lines, "")
	lines = append(lines, formFieldView("cwd", d.form.focus == paneFieldCwd, d.form.view(paneFieldCwd), d.styles)...)

	if d.saved != "" {
		lines = append(lines, "", d.styles.StatusIdle.Render(d.saved))
	}
	if d.err != "" {
		lines = append(lines, "", d.styles.ResultError.Render("✗ "+d.err))
	}
	hints := strings.Join([]string{
		d.styles.StatusKey.Render("enter") + d.styles.Hint.Render(" spawn"),
		d.styles.StatusKey.Render("1-9") + d.styles.Hint.Render(" pick fav"),
		d.styles.StatusKey.Render("ctrl+f") + d.styles.Hint.Render(" save fav"),
		d.styles.StatusKey.Render("tab") + d.styles.Hint.Render(" next"),
		d.styles.StatusKey.Render("esc") + d.styles.Hint.Render(" cancel"),
	}, d.styles.Hint.Render(" · "))
	lines = append(lines, "", hints)
	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}
