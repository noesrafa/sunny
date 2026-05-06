package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/noesrafa/sunny/internal/client"
)

// AgentPickerDialog lists every agent across the federation (local
// daemon + ~/.sunny/peers.yaml) and lets the user switch to one
// (enter), create a new one (n), edit (e), rename (r), or
// archive (a/d).
//
// The agent list loads asynchronously via Init() so the dialog opens
// instantly even if any peer is slow. Per-peer failures don't fail
// the whole load — they show up as a footer status line, the rest of
// the federation still renders.
//
// Edit, rename, and archive only target local agents in v0.19 —
// remote CRUD over federated peers is intentionally deferred (auth
// flow first).
type AgentPickerDialog struct {
	fed       *client.Federation
	currID    string // current session's agent — highlighted in the list
	currHost  string // current session's peer (used with currID for the highlight)
	agents    []client.FederatedAgent
	loadErrs  map[string]error
	multiHost bool // true when at least one peer beyond local is in the roster
	selected  int
	loading   bool
	statusMsg string // ephemeral feedback ("agent X archived", etc.)
	styles    Styles
}

// AgentsLoadedMsg carries the result of an async federated agent-list fetch.
type AgentsLoadedMsg struct {
	Agents []client.FederatedAgent
	Errors map[string]error
}

// SwitchAgentMsg asks the root model to spawn a new session bound to
// id on the named federation peer. Host empty = local.
type SwitchAgentMsg struct {
	ID   string
	Host string
}

// OpenAgentFormMsg opens the create/edit form. Empty EditID = create mode.
type OpenAgentFormMsg struct {
	EditID      string
	Name        string
	Description string
	Model       string
	Effort      string
	Provider    string
	Prompt      string
}

// OpenAgentRenameMsg opens the lightweight rename dialog (just Name).
// Local agents only.
type OpenAgentRenameMsg struct {
	ID   string
	Name string
}

// DeleteAgentMsg requests deletion of an agent, confirmed by the picker.
type DeleteAgentMsg struct{ ID string }

// AgentChangedMsg is emitted by the root after a mutation lands so any
// open AgentPickerDialog can refresh.
type AgentChangedMsg struct {
	Status string // human-readable, shown briefly under the list
}

func NewAgentPickerDialog(fed *client.Federation, currID, currHost string, s Styles) *AgentPickerDialog {
	multi := false
	if fed != nil {
		multi = len(fed.Names()) > 1
	}
	return &AgentPickerDialog{
		fed:       fed,
		currID:    currID,
		currHost:  currHost,
		loading:   true,
		multiHost: multi,
		styles:    s,
	}
}

func (d *AgentPickerDialog) SetStyles(s Styles) { d.styles = s }

func (d *AgentPickerDialog) Init() tea.Cmd {
	return d.loadCmd()
}

func (d *AgentPickerDialog) loadCmd() tea.Cmd {
	fed := d.fed
	if fed == nil {
		return func() tea.Msg {
			return AgentsLoadedMsg{Errors: map[string]error{"local": fmt.Errorf("no daemon connection")}}
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := fed.ListAgents(ctx)
		return AgentsLoadedMsg{Agents: r.Agents, Errors: r.Errors}
	}
}

func (d *AgentPickerDialog) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case AgentsLoadedMsg:
		d.loading = false
		d.loadErrs = m.Errors
		d.agents = m.Agents
		// Snap selection to the current (host, id) if visible.
		for i, a := range d.agents {
			if a.ID == d.currID && a.Host == d.currHost {
				d.selected = i
				break
			}
		}
		return nil
	case AgentChangedMsg:
		d.statusMsg = m.Status
		return d.loadCmd()
	}
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc":
			return func() tea.Msg { return CloseDialogMsg{} }
		case "up", "ctrl+p", "k":
			if d.selected > 0 {
				d.selected--
			}
			return nil
		case "down", "ctrl+n", "j":
			if d.selected < len(d.agents)-1 {
				d.selected++
			}
			return nil
		case "enter":
			if len(d.agents) == 0 {
				return nil
			}
			pick := d.agents[d.selected]
			return func() tea.Msg { return SwitchAgentMsg{ID: pick.ID, Host: pick.Host} }
		case "n":
			return func() tea.Msg {
				return OpenAgentFormMsg{Model: "claude-opus-4-7"}
			}
		case "e":
			if len(d.agents) == 0 {
				return nil
			}
			pick := d.agents[d.selected]
			if pick.Host != "local" {
				d.statusMsg = "Edit only works on local agents (v0.19)"
				return nil
			}
			return func() tea.Msg {
				return OpenAgentFormMsg{
					EditID:      pick.ID,
					Name:        pick.Name,
					Description: pick.Description,
					Model:       pick.Model,
					Effort:      pick.Effort,
					Provider:    pick.Provider,
					// Prompt is fetched by the form itself via GetAgent —
					// AgentSummary doesn't carry it.
				}
			}
		case "r":
			if len(d.agents) == 0 {
				return nil
			}
			pick := d.agents[d.selected]
			if pick.Host != "local" {
				d.statusMsg = "Rename only works on local agents (v0.19)"
				return nil
			}
			return func() tea.Msg {
				return OpenAgentRenameMsg{ID: pick.ID, Name: pick.Name}
			}
		case "d", "a":
			// Both 'd' (legacy "delete") and 'a' (archive) trigger the
			// archive flow. The action is reversible by moving the
			// directory back, so "archive" is the honest verb.
			if len(d.agents) == 0 {
				return nil
			}
			pick := d.agents[d.selected]
			if pick.Host != "local" {
				d.statusMsg = "Archive only works on local agents (v0.19)"
				return nil
			}
			body := []string{
				"Archive agent \"" + pick.Name + "\"?",
				"",
				"Moved to ~/.sunny/.archive/. Conversations go with it.",
				"Restore later by moving the folder back under ~/.sunny/agents/.",
			}
			confirm := NewConfirmDialog(d.styles, "Archive agent", body, DeleteAgentMsg{ID: pick.ID})
			return func() tea.Msg { return OpenSubDialogMsg{Dialog: confirm} }
		}
	}
	return nil
}

func (d *AgentPickerDialog) View(width, height int) string {
	boxW := 64
	if boxW > width-4 {
		boxW = width - 4
	}
	if boxW < 36 {
		boxW = 36
	}
	innerW := boxW - 6

	title := HatchedTitle("Agents", innerW, colPrimary, colAccent, d.styles.DialogTitle)

	lines := []string{title, ""}

	switch {
	case d.loading:
		lines = append(lines, "  "+d.styles.Hint.Render("loading…"))
	case len(d.agents) == 0 && len(d.loadErrs) == 0:
		lines = append(lines, "  "+d.styles.Hint.Render("(no agents — press n to create one)"))
	default:
		for i, a := range d.agents {
			row := d.renderRow(a, i == d.selected)
			lines = append(lines, row)
		}
	}
	// Per-peer load failures show as a footer so degraded peers don't
	// hide the rest of the federation.
	for peer, err := range d.loadErrs {
		lines = append(lines, "  "+d.styles.ResultError.Render("✗ "+peer+": "+err.Error()))
	}

	if d.statusMsg != "" {
		lines = append(lines, "", "  "+d.styles.Hint.Render(d.statusMsg))
	}

	hints := d.styles.StatusKey.Render("enter") + d.styles.Hint.Render(" use  ") +
		d.styles.StatusKey.Render("n") + d.styles.Hint.Render(" new  ") +
		d.styles.StatusKey.Render("e") + d.styles.Hint.Render(" edit  ") +
		d.styles.StatusKey.Render("r") + d.styles.Hint.Render(" rename  ") +
		d.styles.StatusKey.Render("a") + d.styles.Hint.Render(" archive  ") +
		d.styles.StatusKey.Render("esc") + d.styles.Hint.Render(" close")
	lines = append(lines, "", hints)
	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}

func (d *AgentPickerDialog) renderRow(a client.FederatedAgent, selected bool) string {
	marker := "  "
	titleStyle := d.styles.AssistantText
	if selected {
		marker = d.styles.UserPrompt.Render("› ")
		titleStyle = d.styles.HeaderTitle
	}
	suffix := ""
	if a.ID == d.currID && a.Host == d.currHost {
		suffix = " " + d.styles.StatusIdle.Render("●")
	}
	// Display name only. For multi-host federations append the peer
	// label so the user can disambiguate same-named agents on
	// different daemons. The opaque id never surfaces in the UI.
	first := marker + titleStyle.Render(a.Name) + suffix
	if d.multiHost {
		first += " " + d.styles.Hint.Render("·"+a.Host)
	}
	desc := a.Description
	if desc == "" {
		desc = "(no description)"
	}
	return first + "\n    " + d.styles.Hint.Render(desc)
}

// OpenSubDialogMsg lets a dialog push another dialog onto the overlay
// stack without going back to the root model first. Used by the picker
// to launch confirm dialogs and forms.
type OpenSubDialogMsg struct{ Dialog Dialog }
