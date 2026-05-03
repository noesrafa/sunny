package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// dirPicker is the shared "navigate folders" widget extracted from the
// new-session dialog. NewSessionDialog and RunEditDialog both need the
// exact same UX (filter, descend with →, climb with ←/backspace, type to
// fuzzy-match) so the implementation lives in one place.
//
// Lifecycle: caller creates one with newDirPicker, forwards key events
// through Update while focused, and reads Cwd() at submit time. Render
// returns a fixed-height block so the surrounding dialog layout is stable.
type dirPicker struct {
	cwd      string
	entries  []string // directory names in cwd (sorted)
	filtered []int    // indices into entries
	selected int
	search   textinput.Model
	styles   Styles
}

func newDirPicker(initialCwd string, s Styles) *dirPicker {
	cwd := initialCwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	ti := textinput.New()
	ti.Placeholder = "buscar carpeta…"
	ti.Prompt = "› "
	ti.CharLimit = 0
	ti.SetWidth(50)

	p := &dirPicker{cwd: cwd, search: ti, styles: s}
	p.loadDir()
	return p
}

// Cwd returns the currently-selected directory.
func (p *dirPicker) Cwd() string { return p.cwd }

// Focus / Blur toggle the embedded search input's focus, used by parent
// dialogs that have multiple focusable fields.
func (p *dirPicker) Focus() tea.Cmd { return p.search.Focus() }
func (p *dirPicker) Blur()          { p.search.Blur() }

// setStyles is called by parent dialogs after a theme swap so the picker
// repaints with the new palette instead of staying frozen on the colors
// it captured at construction.
func (p *dirPicker) setStyles(s Styles) { p.styles = s }

// SetSearchWidth lets parents resize the embedded search box on layout.
func (p *dirPicker) SetSearchWidth(w int) { p.search.SetWidth(w) }

func (p *dirPicker) loadDir() {
	p.entries = p.entries[:0]
	items, err := os.ReadDir(p.cwd)
	if err == nil {
		for _, it := range items {
			name := it.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			if !it.IsDir() {
				continue
			}
			p.entries = append(p.entries, name)
		}
		sort.Slice(p.entries, func(i, j int) bool {
			return strings.ToLower(p.entries[i]) < strings.ToLower(p.entries[j])
		})
	}
	p.refilter()
	p.selected = 0
}

func (p *dirPicker) refilter() {
	q := strings.ToLower(strings.TrimSpace(p.search.Value()))
	p.filtered = p.filtered[:0]
	for i, name := range p.entries {
		if q == "" || strings.Contains(strings.ToLower(name), q) {
			p.filtered = append(p.filtered, i)
		}
	}
	if p.selected >= len(p.filtered) {
		p.selected = 0
	}
}

func (p *dirPicker) descend() {
	if len(p.filtered) == 0 {
		return
	}
	name := p.entries[p.filtered[p.selected]]
	next := filepath.Join(p.cwd, name)
	if info, err := os.Stat(next); err == nil && info.IsDir() {
		p.cwd = next
		p.search.SetValue("")
		p.loadDir()
	}
}

func (p *dirPicker) ascend() {
	parent := filepath.Dir(p.cwd)
	if parent == p.cwd {
		return
	}
	prev := filepath.Base(p.cwd)
	p.cwd = parent
	p.search.SetValue("")
	p.loadDir()
	for i, idx := range p.filtered {
		if p.entries[idx] == prev {
			p.selected = i
			break
		}
	}
}

// Update consumes a key event. Returns the textinput command (cursor blink,
// etc.) and a `consumed` flag — when true the parent should NOT pass the
// event to anything else, when false the parent can fall through (e.g. to
// run its own focus-cycling).
func (p *dirPicker) Update(msg tea.Msg) (tea.Cmd, bool) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil, false
	}
	switch k.String() {
	case "up", "ctrl+p":
		if p.selected > 0 {
			p.selected--
		}
		return nil, true
	case "down", "ctrl+n":
		if p.selected < len(p.filtered)-1 {
			p.selected++
		}
		return nil, true
	case "right":
		p.descend()
		return nil, true
	case "left":
		if p.search.Value() == "" {
			p.ascend()
			return nil, true
		}
	case "backspace":
		if p.search.Value() == "" {
			p.ascend()
			return nil, true
		}
	}
	prev := p.search.Value()
	var cmd tea.Cmd
	p.search, cmd = p.search.Update(msg)
	if p.search.Value() != prev {
		p.refilter()
	}
	return cmd, true
}

// Render lays out the picker as a fixed-height block: search row, list,
// hint row. Caller passes maxRows for the list area so dialog height is
// predictable as the user filters.
func (p *dirPicker) Render(maxRows, innerW int) string {
	p.SetSearchWidth(innerW - 2)
	searchView := "  " + p.search.View()
	listView := p.renderList(maxRows, innerW)
	hint := p.styles.Hint.Render("↑↓ navegar · → descender · ← atrás · type para filtrar")
	return strings.Join([]string{searchView, listView, hint}, "\n")
}

func (p *dirPicker) renderList(maxRows, innerW int) string {
	if len(p.filtered) == 0 {
		empty := "  " + p.styles.Hint.Render("(sin coincidencias)")
		pad := strings.Repeat("\n", maxRows-1)
		return empty + pad
	}

	start := 0
	if p.selected >= maxRows {
		start = p.selected - maxRows + 1
	}
	end := start + maxRows
	if end > len(p.filtered) {
		end = len(p.filtered)
	}

	var rows []string
	for i := start; i < end; i++ {
		name := p.entries[p.filtered[i]]
		if i == p.selected {
			marker := p.styles.UserPrompt.Render("›")
			rows = append(rows, marker+" "+p.styles.HeaderTitle.Render(name))
		} else {
			rows = append(rows, "  "+p.styles.AssistantText.Render(name))
		}
	}
	for len(rows) < maxRows {
		rows = append(rows, "")
	}
	if len(p.filtered) > maxRows {
		extra := len(p.filtered) - maxRows
		rows = append(rows, p.styles.Hint.Render("  …"+strconv.Itoa(extra)+" más"))
	}
	return strings.Join(rows, "\n")
}
