package tui

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

var modelChoices = []string{"opus", "sonnet", "haiku"}
var effortChoices = []string{"low", "medium", "high", "xhigh", "max"}

type newSessionFocus int

const (
	focusPicker newSessionFocus = iota
	focusModel
	focusEffort
	numNewSessionFocus
)

type NewSessionDialog struct {
	cwd       string
	entries   []string // directory names in cwd (sorted)
	filtered  []int    // indices into entries
	selected  int
	search    textinput.Model
	styles    Styles
	focus     newSessionFocus
	modelIdx  int
	effortIdx int
	err       string
}

func NewNewSessionDialog(defaultCwd, defaultModel, defaultEffort string, s Styles) *NewSessionDialog {
	cwd := defaultCwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	ti := textinput.New()
	ti.Placeholder = "buscar carpeta…"
	ti.Prompt = "› "
	ti.CharLimit = 0
	ti.SetWidth(50)
	ti.Focus()

	d := &NewSessionDialog{
		cwd:       cwd,
		search:    ti,
		styles:    s,
		focus:     focusPicker,
		modelIdx:  max0(slices.Index(modelChoices, defaultModel)),
		effortIdx: max0(slices.Index(effortChoices, defaultEffort)),
	}
	d.loadDir()
	return d
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func (d *NewSessionDialog) loadDir() {
	d.entries = d.entries[:0]
	items, err := os.ReadDir(d.cwd)
	if err == nil {
		for _, it := range items {
			name := it.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			if !it.IsDir() {
				continue
			}
			d.entries = append(d.entries, name)
		}
		sort.Slice(d.entries, func(i, j int) bool {
			return strings.ToLower(d.entries[i]) < strings.ToLower(d.entries[j])
		})
	}
	d.refilter()
	d.selected = 0
}

func (d *NewSessionDialog) refilter() {
	q := strings.ToLower(strings.TrimSpace(d.search.Value()))
	d.filtered = d.filtered[:0]
	for i, name := range d.entries {
		if q == "" || strings.Contains(strings.ToLower(name), q) {
			d.filtered = append(d.filtered, i)
		}
	}
	if d.selected >= len(d.filtered) {
		d.selected = 0
	}
}

func (d *NewSessionDialog) descend() {
	if len(d.filtered) == 0 {
		return
	}
	name := d.entries[d.filtered[d.selected]]
	next := filepath.Join(d.cwd, name)
	if info, err := os.Stat(next); err == nil && info.IsDir() {
		d.cwd = next
		d.search.SetValue("")
		d.loadDir()
	}
}

func (d *NewSessionDialog) ascend() {
	parent := filepath.Dir(d.cwd)
	if parent == d.cwd {
		return
	}
	prev := filepath.Base(d.cwd)
	d.cwd = parent
	d.search.SetValue("")
	d.loadDir()
	// Try to land on the directory we just came from.
	for i, idx := range d.filtered {
		if d.entries[idx] == prev {
			d.selected = i
			break
		}
	}
}

func (d *NewSessionDialog) SetStyles(s Styles) { d.styles = s }

func (d *NewSessionDialog) Init() tea.Cmd {
	return textinput.Blink
}

func (d *NewSessionDialog) Update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc":
			return func() tea.Msg { return CloseDialogMsg{} }
		case "enter":
			return d.confirm()
		case "tab":
			d.focus = (d.focus + 1) % numNewSessionFocus
			d.applyFocus()
			return nil
		case "shift+tab":
			d.focus = (d.focus + numNewSessionFocus - 1) % numNewSessionFocus
			d.applyFocus()
			return nil
		}

		if d.focus == focusPicker {
			switch k.String() {
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
			case "right":
				d.descend()
				return nil
			case "left":
				if d.search.Value() == "" {
					d.ascend()
					return nil
				}
			case "backspace":
				if d.search.Value() == "" {
					d.ascend()
					return nil
				}
			}
			prev := d.search.Value()
			var cmd tea.Cmd
			d.search, cmd = d.search.Update(msg)
			if d.search.Value() != prev {
				d.refilter()
			}
			return cmd
		}

		if d.focus == focusModel {
			switch k.String() {
			case "left", "h":
				if d.modelIdx > 0 {
					d.modelIdx--
				}
				return nil
			case "right", "l", " ":
				if d.modelIdx < len(modelChoices)-1 {
					d.modelIdx++
				}
				return nil
			}
		}
		if d.focus == focusEffort {
			switch k.String() {
			case "left", "h":
				if d.effortIdx > 0 {
					d.effortIdx--
				}
				return nil
			case "right", "l", " ":
				if d.effortIdx < len(effortChoices)-1 {
					d.effortIdx++
				}
				return nil
			}
		}
	}
	return nil
}

func (d *NewSessionDialog) applyFocus() {
	if d.focus == focusPicker {
		d.search.Focus()
	} else {
		d.search.Blur()
	}
}

func (d *NewSessionDialog) confirm() tea.Cmd {
	cwd := strings.TrimSpace(d.cwd)
	if cwd == "" {
		d.err = "directorio vacío"
		return nil
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		d.err = err.Error()
		return nil
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		d.err = "no es un directorio: " + abs
		return nil
	}
	model := modelChoices[d.modelIdx]
	effort := effortChoices[d.effortIdx]
	return func() tea.Msg {
		return CreateSessionMsg{Cwd: abs, Model: model, Effort: effort}
	}
}

func (d *NewSessionDialog) View(width, height int) string {
	boxW := 72
	if boxW > width-4 {
		boxW = width - 4
	}
	if boxW < 40 {
		boxW = 40
	}
	innerW := boxW - 6
	d.search.SetWidth(innerW - 2)

	listH := height - 22
	if listH > 12 {
		listH = 12
	}
	if listH < 5 {
		listH = 5
	}

	title := HatchedTitle("New Session", innerW, colPrimary, colAccent, d.styles.DialogTitle)
	pickerLabel := d.fieldLabel("directorio · "+d.cwd, d.focus == focusPicker)
	searchView := "  " + d.search.View()
	listView := d.renderList(listH, innerW)
	pickerHints := d.styles.Hint.Render("↑↓ navegar · → descender · ← atrás · type para filtrar")

	modelLabel := d.fieldLabel("modelo", d.focus == focusModel)
	modelRow := d.radioRow(modelChoices, d.modelIdx, d.focus == focusModel)

	effortLabel := d.fieldLabel("effort", d.focus == focusEffort)
	effortRow := d.radioRow(effortChoices, d.effortIdx, d.focus == focusEffort)

	hints := d.styles.StatusKey.Render("enter") + d.styles.Hint.Render(" crear  ") +
		d.styles.StatusKey.Render("tab") + d.styles.Hint.Render(" siguiente campo  ") +
		d.styles.StatusKey.Render("←/→") + d.styles.Hint.Render(" cambiar opción  ") +
		d.styles.StatusKey.Render("esc") + d.styles.Hint.Render(" cancelar")

	lines := []string{
		title, "",
		pickerLabel,
		searchView,
		listView,
		pickerHints,
		"",
		modelLabel,
		modelRow,
		"",
		effortLabel,
		effortRow,
	}
	if d.err != "" {
		lines = append(lines, "", d.styles.ResultError.Render("✗ "+d.err))
	}
	lines = append(lines, "", hints)

	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}

func (d *NewSessionDialog) renderList(maxRows, innerW int) string {
	if len(d.filtered) == 0 {
		empty := "  " + d.styles.Hint.Render("(sin coincidencias)")
		// Pad to the requested height so layout stays stable.
		pad := strings.Repeat("\n", maxRows-1)
		return empty + pad
	}

	// Window the list around the selection.
	start := 0
	if d.selected >= maxRows {
		start = d.selected - maxRows + 1
	}
	end := start + maxRows
	if end > len(d.filtered) {
		end = len(d.filtered)
	}

	var rows []string
	for i := start; i < end; i++ {
		name := d.entries[d.filtered[i]]
		if i == d.selected {
			marker := d.styles.UserPrompt.Render("›")
			rows = append(rows, marker+" "+d.styles.HeaderTitle.Render(name))
		} else {
			rows = append(rows, "  "+d.styles.AssistantText.Render(name))
		}
	}
	// Pad to maxRows so the layout below doesn't jump as the list shrinks.
	for len(rows) < maxRows {
		rows = append(rows, "")
	}
	if len(d.filtered) > maxRows {
		extra := len(d.filtered) - maxRows
		rows = append(rows, d.styles.Hint.Render("  …"+strconv.Itoa(extra)+" más"))
	}
	return strings.Join(rows, "\n")
}

func (d *NewSessionDialog) fieldLabel(text string, focused bool) string {
	if focused {
		return d.styles.UserPrompt.Render("▸ ") + d.styles.HeaderTitle.Render(text)
	}
	return "  " + d.styles.HeaderDim.Render(text)
}

func (d *NewSessionDialog) radioRow(opts []string, sel int, focused bool) string {
	var parts []string
	for i, o := range opts {
		mark := "○"
		st := d.styles.Hint
		if i == sel {
			mark = "●"
			st = d.styles.StatusIdle
			if focused {
				st = d.styles.UserPrompt
			}
		}
		parts = append(parts, st.Render(mark+" "+o))
	}
	return "  " + strings.Join(parts, "  ")
}
