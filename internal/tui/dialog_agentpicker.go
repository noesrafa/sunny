package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/noesrafa/sunny/internal/client"
)

// AgentPickerDialog lists every agent on the daemon and lets the user
// switch to one (enter), create a new one (n), edit (e), or delete (d).
//
// The agent list loads asynchronously via Init() so the dialog opens
// instantly even if the daemon is slow. The "(loading…)" placeholder
// disappears once AgentsLoadedMsg arrives.
type AgentPickerDialog struct {
	client    *client.Client
	currSlug  string // current session's agent — highlighted in the list
	agents    []client.AgentSummary
	selected  int
	loading   bool
	loadErr   string
	statusMsg string // ephemeral feedback ("agent X deleted", etc.)
	styles    Styles
}

// AgentsLoadedMsg carries the result of an async agent-list fetch.
type AgentsLoadedMsg struct {
	Agents []client.AgentSummary
	Err    error
}

// SwitchAgentMsg asks the root model to spawn a new session bound to slug.
type SwitchAgentMsg struct{ Slug string }

// OpenAgentFormMsg opens the create/edit form. Empty Slug = create mode.
type OpenAgentFormMsg struct {
	EditSlug    string
	Name        string
	Description string
	Model       string
	Prompt      string
}

// DeleteAgentMsg requests deletion of an agent, confirmed by the picker.
type DeleteAgentMsg struct{ Slug string }

// AgentChangedMsg is emitted by the root after a mutation lands so any
// open AgentPickerDialog can refresh.
type AgentChangedMsg struct {
	Status string // human-readable, shown briefly under the list
}

func NewAgentPickerDialog(c *client.Client, currSlug string, s Styles) *AgentPickerDialog {
	return &AgentPickerDialog{client: c, currSlug: currSlug, loading: true, styles: s}
}

func (d *AgentPickerDialog) SetStyles(s Styles) { d.styles = s }

func (d *AgentPickerDialog) Init() tea.Cmd {
	return d.loadCmd()
}

func (d *AgentPickerDialog) loadCmd() tea.Cmd {
	c := d.client
	if c == nil {
		return func() tea.Msg {
			return AgentsLoadedMsg{Err: fmt.Errorf("no daemon connection")}
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ag, err := c.ListAgents(ctx)
		return AgentsLoadedMsg{Agents: ag, Err: err}
	}
}

func (d *AgentPickerDialog) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case AgentsLoadedMsg:
		d.loading = false
		if m.Err != nil {
			d.loadErr = m.Err.Error()
			return nil
		}
		d.loadErr = ""
		d.agents = m.Agents
		// Snap selection to the current agent if visible.
		for i, a := range d.agents {
			if a.Slug == d.currSlug {
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
			return func() tea.Msg { return SwitchAgentMsg{Slug: pick.Slug} }
		case "n":
			return func() tea.Msg {
				return OpenAgentFormMsg{Model: "claude-opus-4-7"}
			}
		case "e":
			if len(d.agents) == 0 {
				return nil
			}
			pick := d.agents[d.selected]
			return func() tea.Msg {
				return OpenAgentFormMsg{
					EditSlug:    pick.Slug,
					Name:        pick.Name,
					Description: pick.Description,
					Model:       pick.Model,
					// Prompt is fetched by the form itself via GetAgent —
					// AgentSummary doesn't carry it.
				}
			}
		case "d", "a":
			// Both 'd' (legacy "delete") and 'a' (archive) trigger the
			// archive flow. The action is reversible by moving the
			// directory back, so "archive" is the honest verb.
			if len(d.agents) == 0 {
				return nil
			}
			pick := d.agents[d.selected]
			body := []string{
				"Archive agent \"" + pick.Name + "\" (slug " + pick.Slug + ")?",
				"",
				"Moved to ~/.sunny/.archive/. Conversations go with it.",
				"Restore later by moving the folder back under ~/.sunny/agents/.",
			}
			confirm := NewConfirmDialog(d.styles, "Archive agent", body, DeleteAgentMsg{Slug: pick.Slug})
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
	case d.loadErr != "":
		lines = append(lines, "  "+d.styles.ResultError.Render("✗ "+d.loadErr))
	case len(d.agents) == 0:
		lines = append(lines, "  "+d.styles.Hint.Render("(no agents — press n to create one)"))
	default:
		for i, a := range d.agents {
			row := d.renderRow(a, i == d.selected)
			lines = append(lines, row)
		}
	}

	if d.statusMsg != "" {
		lines = append(lines, "", "  "+d.styles.Hint.Render(d.statusMsg))
	}

	hints := d.styles.StatusKey.Render("enter") + d.styles.Hint.Render(" use  ") +
		d.styles.StatusKey.Render("n") + d.styles.Hint.Render(" new  ") +
		d.styles.StatusKey.Render("e") + d.styles.Hint.Render(" edit  ") +
		d.styles.StatusKey.Render("a") + d.styles.Hint.Render(" archive  ") +
		d.styles.StatusKey.Render("esc") + d.styles.Hint.Render(" close")
	lines = append(lines, "", hints)
	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}

func (d *AgentPickerDialog) renderRow(a client.AgentSummary, selected bool) string {
	marker := "  "
	titleStyle := d.styles.AssistantText
	if selected {
		marker = d.styles.UserPrompt.Render("› ")
		titleStyle = d.styles.HeaderTitle
	}
	suffix := ""
	if a.Slug == d.currSlug {
		suffix = " " + d.styles.StatusIdle.Render("●")
	}
	first := marker + titleStyle.Render(a.Name) + " " + d.styles.Hint.Render("·"+a.Slug) + suffix
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
