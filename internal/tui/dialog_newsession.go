package tui

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/noesrafa/sunny/internal/client"
)

// NewSessionDialog asks for two things: which agent + what cwd. Model
// and effort used to live here; in v0.9 they moved into the agent's
// own `agent.yaml`. The dialog reads them off the picked agent and
// passes them through CreateSessionMsg so the sidebar's "opus max"
// readout still works.
type newSessionFocus int

const (
	focusAgent newSessionFocus = iota
	focusPicker
	numNewSessionFocus
)

type NewSessionDialog struct {
	cwd      string
	entries  []string // directory names in cwd (sorted)
	filtered []int    // indices into entries
	selected int
	search   textinput.Model
	styles   Styles
	focus    newSessionFocus
	err      string

	// Agent picker — loaded async via Init(). Until the load returns
	// the row shows "(loading…)"; Enter still works once it does.
	client       *client.Client
	defaultAgent string
	agents       []client.AgentSummary
	agentIdx     int
	agentLoading bool
	agentLoadErr string
}

func NewNewSessionDialog(c *client.Client, defaultCwd, defaultAgent string, s Styles) *NewSessionDialog {
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
		cwd:          cwd,
		search:       ti,
		styles:       s,
		focus:        focusPicker,
		client:       c,
		defaultAgent: defaultAgent,
		agentLoading: c != nil,
	}
	d.loadDir()
	return d
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
	for i, idx := range d.filtered {
		if d.entries[idx] == prev {
			d.selected = i
			break
		}
	}
}

func (d *NewSessionDialog) SetStyles(s Styles) { d.styles = s }

func (d *NewSessionDialog) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink}
	if d.client != nil {
		cmds = append(cmds, d.loadAgentsCmd())
	}
	return tea.Batch(cmds...)
}

// newSessionAgentsLoadedMsg carries the agent list once the async
// fetch returns. Distinct from AgentsLoadedMsg so it doesn't clash
// with the agent picker if both happen to be open.
type newSessionAgentsLoadedMsg struct {
	Agents []client.AgentSummary
	Err    error
}

func (d *NewSessionDialog) loadAgentsCmd() tea.Cmd {
	c := d.client
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ag, err := c.ListAgents(ctx)
		return newSessionAgentsLoadedMsg{Agents: ag, Err: err}
	}
}

func (d *NewSessionDialog) Update(msg tea.Msg) tea.Cmd {
	if m, ok := msg.(newSessionAgentsLoadedMsg); ok {
		d.agentLoading = false
		if m.Err != nil {
			d.agentLoadErr = m.Err.Error()
			return nil
		}
		d.agents = m.Agents
		// Snap selection to the default agent if visible.
		for i, a := range d.agents {
			if a.Slug == d.defaultAgent {
				d.agentIdx = i
				break
			}
		}
		return nil
	}
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

		if d.focus == focusAgent {
			switch k.String() {
			case "left", "h":
				if d.agentIdx > 0 {
					d.agentIdx--
				}
				return nil
			case "right", "l", " ":
				if d.agentIdx < len(d.agents)-1 {
					d.agentIdx++
				}
				return nil
			}
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
	// Pull model + effort from the picked agent so the sidebar's
	// readout shows the right thing immediately. Empty if the agent
	// list hasn't loaded yet — createSession falls back to the model
	// defaults in that case.
	agentSlug := d.defaultAgent
	model, effort := "", ""
	if len(d.agents) > 0 && d.agentIdx >= 0 && d.agentIdx < len(d.agents) {
		picked := d.agents[d.agentIdx]
		agentSlug = picked.Slug
		model = picked.Model
		effort = picked.Effort
	}
	return func() tea.Msg {
		return CreateSessionMsg{
			Cwd:       abs,
			Model:     model,
			Effort:    effort,
			AgentSlug: agentSlug,
		}
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

	listH := height - 14
	if listH > 12 {
		listH = 12
	}
	if listH < 5 {
		listH = 5
	}

	title := HatchedTitle("New Session", innerW, colPrimary, colAccent, d.styles.DialogTitle)

	agentLabel := d.fieldLabel("agent", d.focus == focusAgent)
	agentRow := d.renderAgentRow(d.focus == focusAgent)

	pickerLabel := d.fieldLabel("directorio · "+d.cwd, d.focus == focusPicker)
	searchView := "  " + d.search.View()
	listView := d.renderList(listH, innerW)
	pickerHints := d.styles.Hint.Render("↑↓ navegar · → descender · ← atrás · type para filtrar")

	hints := d.styles.StatusKey.Render("enter") + d.styles.Hint.Render(" crear  ") +
		d.styles.StatusKey.Render("tab") + d.styles.Hint.Render(" siguiente campo  ") +
		d.styles.StatusKey.Render("←/→") + d.styles.Hint.Render(" cambiar opción  ") +
		d.styles.StatusKey.Render("esc") + d.styles.Hint.Render(" cancelar")

	lines := []string{
		title, "",
		agentLabel,
		agentRow,
		"",
		pickerLabel,
		searchView,
		listView,
		pickerHints,
	}
	if d.err != "" {
		lines = append(lines, "", d.styles.ResultError.Render("✗ "+d.err))
	}
	lines = append(lines, "", hints)

	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}

// renderAgentRow renders the agent picker as a horizontal radio.
// Loading shows a hint; on error shows the message; on success uses
// the shared renderRadioRow.
func (d *NewSessionDialog) renderAgentRow(focused bool) string {
	if d.agentLoading {
		return "  " + d.styles.Hint.Render("loading agents…")
	}
	if d.agentLoadErr != "" {
		return "  " + d.styles.ResultError.Render("✗ "+d.agentLoadErr)
	}
	if len(d.agents) == 0 {
		return "  " + d.styles.Hint.Render("(no agents — use ctrl+a to create one first)")
	}
	opts := make([]string, len(d.agents))
	for i, a := range d.agents {
		opts[i] = a.Slug
	}
	return renderRadioRow(opts, d.agentIdx, focused, d.styles)
}

func (d *NewSessionDialog) renderList(maxRows, innerW int) string {
	if len(d.filtered) == 0 {
		empty := "  " + d.styles.Hint.Render("(sin coincidencias)")
		pad := strings.Repeat("\n", maxRows-1)
		return empty + pad
	}

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
