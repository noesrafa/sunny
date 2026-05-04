package tui

import (
	"context"
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
//
// The directory picker walks the *daemon's* filesystem (not the local
// process'). Even for the local daemon we go through GET /fs/list —
// it keeps the local/remote code paths identical, the round-trip on
// loopback is sub-millisecond, and remote peers Just Work.
type newSessionFocus int

const (
	focusAgent newSessionFocus = iota
	focusPicker
	numNewSessionFocus
)

type NewSessionDialog struct {
	host     string // federation peer name (for CreateSessionMsg.Host)
	cwd      string // current path being browsed (daemon-side)
	entries  []string
	filtered []int // indices into entries
	selected int
	search   textinput.Model
	styles   Styles
	focus    newSessionFocus
	err      string

	// Dir loader (async). loading is true while the first response for
	// the current cwd is in flight; loadGen guards against stale
	// responses landing after the user has already navigated past
	// them (only the latest gen's response is applied).
	dirLoading bool
	dirLoadErr string
	loadGen    int

	// Agent picker — loaded async via Init(). Until the load returns
	// the row shows "(loading…)"; Enter still works once it does.
	client       *client.Client
	defaultAgent string
	agents       []client.AgentSummary
	agentIdx     int
	agentLoading bool
	agentLoadErr string
}

// NewNewSessionDialog wires the dialog against one peer's client.
// host is the federation peer name (e.g. "local", "vmi3091691"); it
// rides along on the emitted CreateSessionMsg so the model spawns the
// session against the right daemon.
//
// defaultCwd may be empty — when it is, the dialog falls back to the
// daemon's home dir (returned by the first /fs/list response).
func NewNewSessionDialog(c *client.Client, host, defaultCwd, defaultAgent string, s Styles) *NewSessionDialog {
	ti := textinput.New()
	ti.Placeholder = "buscar carpeta…"
	ti.Prompt = "› "
	ti.CharLimit = 0
	ti.SetWidth(50)
	ti.Focus()

	d := &NewSessionDialog{
		host:         host,
		cwd:          defaultCwd,
		search:       ti,
		styles:       s,
		focus:        focusPicker,
		client:       c,
		defaultAgent: defaultAgent,
		agentLoading: c != nil,
		dirLoading:   c != nil,
	}
	return d
}

// dirLoadedMsg carries one /fs/list response. Tagged with gen so the
// dialog can drop responses for a path the user has already navigated
// past.
type dirLoadedMsg struct {
	Gen     int
	Path    string
	Entries []string
	Err     error
}

func (d *NewSessionDialog) loadDirCmd(path string) tea.Cmd {
	c := d.client
	if c == nil {
		return nil
	}
	d.loadGen++
	gen := d.loadGen
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		resp, err := c.ListDir(ctx, path)
		if err != nil {
			return dirLoadedMsg{Gen: gen, Path: path, Err: err}
		}
		names := make([]string, 0, len(resp.Entries))
		for _, e := range resp.Entries {
			names = append(names, e.Name)
		}
		// Daemon already sorts case-insensitive; sort defensively in
		// case a future server version stops doing it.
		sort.Slice(names, func(i, j int) bool {
			return strings.ToLower(names[i]) < strings.ToLower(names[j])
		})
		return dirLoadedMsg{Gen: gen, Path: resp.Path, Entries: names}
	}
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

// descend dives into the highlighted directory. The /fs/list response
// will replace the entry list; until it arrives the picker stays on
// the old listing with dirLoading true.
func (d *NewSessionDialog) descend() tea.Cmd {
	if len(d.filtered) == 0 || d.cwd == "" {
		return nil
	}
	name := d.entries[d.filtered[d.selected]]
	next := joinPath(d.cwd, name)
	d.cwd = next
	d.search.SetValue("")
	d.dirLoading = true
	d.dirLoadErr = ""
	return d.loadDirCmd(next)
}

// ascend walks up one level. The current dir's basename is remembered
// so the parent listing snaps to that entry once it lands.
func (d *NewSessionDialog) ascend() tea.Cmd {
	if d.cwd == "" {
		return nil
	}
	parent := parentPath(d.cwd)
	if parent == "" || parent == d.cwd {
		return nil
	}
	d.cwd = parent
	d.search.SetValue("")
	d.dirLoading = true
	d.dirLoadErr = ""
	return d.loadDirCmd(parent)
}

func (d *NewSessionDialog) SetStyles(s Styles) { d.styles = s }

func (d *NewSessionDialog) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink}
	if d.client != nil {
		cmds = append(cmds, d.loadAgentsCmd())
		cmds = append(cmds, d.loadDirCmd(d.cwd))
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
	switch m := msg.(type) {
	case dirLoadedMsg:
		// Stale response (user moved on) — drop.
		if m.Gen != d.loadGen {
			return nil
		}
		d.dirLoading = false
		if m.Err != nil {
			d.dirLoadErr = m.Err.Error()
			d.entries = nil
			d.filtered = nil
			return nil
		}
		d.dirLoadErr = ""
		d.cwd = m.Path
		d.entries = m.Entries
		d.refilter()
		d.selected = 0
		return nil
	case newSessionAgentsLoadedMsg:
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
				return d.descend()
			case "left":
				if d.search.Value() == "" {
					return d.ascend()
				}
			case "backspace":
				if d.search.Value() == "" {
					return d.ascend()
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
	if d.dirLoading {
		d.err = "esperando listado del directorio"
		return nil
	}
	if d.dirLoadErr != "" {
		d.err = "directorio inválido: " + d.dirLoadErr
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
	host := d.host
	return func() tea.Msg {
		return CreateSessionMsg{
			Cwd:       cwd,
			Model:     model,
			Effort:    effort,
			AgentSlug: agentSlug,
			Host:      host,
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

	titleText := "New Session"
	if d.host != "" && d.host != "local" {
		titleText += " · " + d.host
	}
	title := HatchedTitle(titleText, innerW, colPrimary, colAccent, d.styles.DialogTitle)

	agentLabel := d.fieldLabel("agent", d.focus == focusAgent)
	agentRow := d.renderAgentRow(d.focus == focusAgent)

	pickerHeader := "directorio"
	if d.cwd != "" {
		pickerHeader += " · " + d.cwd
	}
	pickerLabel := d.fieldLabel(pickerHeader, d.focus == focusPicker)
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
	if d.dirLoading && len(d.entries) == 0 {
		empty := "  " + d.styles.Hint.Render("loading…")
		pad := strings.Repeat("\n", maxRows-1)
		return empty + pad
	}
	if d.dirLoadErr != "" && len(d.entries) == 0 {
		empty := "  " + d.styles.ResultError.Render("✗ "+d.dirLoadErr)
		pad := strings.Repeat("\n", maxRows-1)
		return empty + pad
	}
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

// joinPath appends name to base using the daemon's path separator.
// We use forward slashes regardless of TUI host because every sunny
// daemon target today is unix-y (linux/macOS); switching to remote
// Windows daemons is out of scope.
func joinPath(base, name string) string {
	if base == "" {
		return name
	}
	if strings.HasSuffix(base, "/") {
		return base + name
	}
	return base + "/" + name
}

// parentPath returns the parent directory of p, or "" if p is the
// filesystem root. Mirrors filepath.Dir for unix paths but stays
// host-agnostic so paths returned from a remote daemon are walked
// correctly regardless of the local TUI's OS.
func parentPath(p string) string {
	if p == "" || p == "/" {
		return ""
	}
	p = strings.TrimRight(p, "/")
	idx := strings.LastIndex(p, "/")
	if idx < 0 {
		return ""
	}
	if idx == 0 {
		return "/"
	}
	return p[:idx]
}
