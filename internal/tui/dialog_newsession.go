package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/noesrafa/sunny/internal/client"
)

// NewSessionDialog asks for two things: which agent + what cwd. Model
// and effort used to live here; in v0.9 they moved into the agent's
// own `agent.yaml`. The dialog reads them off the picked agent and
// passes them through CreateSessionMsg so the sidebar's "opus max"
// readout still works.
//
// The directory picker is delegated to DirPicker — extracted in the
// runs work so multiple dialogs (newsession, run form, …) share the
// same peer-aware browser without duplicating its state machine.
type newSessionFocus int

const (
	focusAgent newSessionFocus = iota
	focusPicker
	numNewSessionFocus
)

type NewSessionDialog struct {
	host   string // federation peer name (for CreateSessionMsg.Host)
	picker *DirPicker
	styles Styles
	focus  newSessionFocus
	err    string

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
	d := &NewSessionDialog{
		host:         host,
		picker:       NewDirPicker(c, defaultCwd, "", s),
		styles:       s,
		focus:        focusPicker,
		client:       c,
		defaultAgent: defaultAgent,
		agentLoading: c != nil,
	}
	// Initial focus is on the picker — match historical behaviour.
	d.applyFocus()
	return d
}

func (d *NewSessionDialog) SetStyles(s Styles) {
	d.styles = s
	d.picker.SetStyles(s)
}

func (d *NewSessionDialog) Init() tea.Cmd {
	cmds := []tea.Cmd{d.picker.Init()}
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
	// Picker-internal async completions land regardless of focus —
	// the picker filters stale generations on its own. Forward and
	// stop; nothing else cares about that type.
	if _, ok := msg.(dirPickerLoadedMsg); ok {
		return d.picker.Update(msg)
	}

	switch m := msg.(type) {
	case newSessionAgentsLoadedMsg:
		d.agentLoading = false
		if m.Err != nil {
			d.agentLoadErr = m.Err.Error()
			return nil
		}
		d.agents = m.Agents
		for i, a := range d.agents {
			if a.Slug == d.defaultAgent {
				d.agentIdx = i
				break
			}
		}
		return nil
	}

	if k, ok := msg.(tea.KeyMsg); ok {
		// Dialog-level keys win regardless of which field is focused.
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
			return nil
		}
		if d.focus == focusPicker {
			return d.picker.Update(msg)
		}
	}
	return nil
}

func (d *NewSessionDialog) applyFocus() {
	if d.focus == focusPicker {
		d.picker.Focus()
	} else {
		d.picker.Blur()
	}
}

func (d *NewSessionDialog) confirm() tea.Cmd {
	cwd := strings.TrimSpace(d.picker.Cwd())
	if cwd == "" {
		d.err = "directorio vacío"
		return nil
	}
	if d.picker.Loading() {
		d.err = "esperando listado del directorio"
		return nil
	}
	if e := d.picker.LoadErr(); e != "" {
		d.err = "directorio inválido: " + e
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
	d.picker.SetSearchWidth(innerW - 2)

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
	if cwd := d.picker.Cwd(); cwd != "" {
		pickerHeader += " · " + cwd
	}
	pickerLabel := d.fieldLabel(pickerHeader, d.focus == focusPicker)

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
		d.picker.SearchView(),
		d.picker.ListView(listH, innerW),
		d.picker.HintLine(),
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

func (d *NewSessionDialog) fieldLabel(text string, focused bool) string {
	if focused {
		return d.styles.UserPrompt.Render("▸ ") + d.styles.HeaderTitle.Render(text)
	}
	return "  " + d.styles.HeaderDim.Render(text)
}
