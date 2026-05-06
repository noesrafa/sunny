package tui

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/noesrafa/sunny/internal/client"
)

// agentFormFocus tracks which field has the cursor.
type agentFormFocus int

const (
	agFocusName agentFormFocus = iota
	agFocusDescription
	agFocusModel
	agFocusEffort
	agFocusProvider
	agFocusPrompt
	numAgentFocus
)

// agentFormEffortChoices and agentFormProviderChoices drive the radio
// rows for those two fields. Both use empty-string-defaults so a
// freshly-created agent inherits the daemon defaults.
var agentFormEffortChoices = []string{"low", "medium", "high", "xhigh", "max"}
var agentFormProviderChoices = []string{"(default)", "claude-code", "anthropic", "ollama"}

// AgentFormDialog is the create/edit form for an agent.
//
// Modes:
//   - editID == "" → create mode. The daemon mints an opaque id on
//     POST; the user never picks one.
//   - editID != "" → edit mode. The id is immutable; prompt is fetched
//     async via GetAgent (AgentSummary doesn't carry it).
type AgentFormDialog struct {
	client      *client.Client
	editID      string // empty = create
	name        textinput.Model
	desc        textinput.Model
	model       textinput.Model
	effortIdx   int
	providerIdx int
	prompt      textarea.Model
	focus       agentFormFocus
	err         string
	saving      bool
	loading     bool // true while we fetch the existing agent's prompt for edit mode
	styles      Styles
}

// AgentDetailLoadedMsg carries the prompt body for edit-mode prefill.
type AgentDetailLoadedMsg struct {
	ID     string
	Prompt string
	Err    error
}

// SubmitAgentFormMsg is emitted when the user presses ctrl+s. The root
// model performs the actual API call (CreateAgent or UpdateAgent) and
// emits AgentSavedMsg.
type SubmitAgentFormMsg struct {
	EditID      string // empty → create
	Name        string
	Description string
	Model       string
	Effort      string
	Provider    string
	Prompt      string
}

// AgentSavedMsg is the result of the API call. Listened to by both the
// root (to refresh) and the form itself (to show errors / close on
// success).
type AgentSavedMsg struct {
	EditID string
	ID     string
	Err    error
}

// NewAgentFormDialog builds a fresh form. Pass an OpenAgentFormMsg-shaped
// payload (any of the fields can be empty) to prefill.
func NewAgentFormDialog(c *client.Client, m OpenAgentFormMsg, s Styles) *AgentFormDialog {
	mk := func(placeholder, value string, limit int, focus bool) textinput.Model {
		ti := textinput.New()
		ti.Placeholder = placeholder
		ti.Prompt = "› "
		ti.CharLimit = limit
		ti.SetValue(value)
		if focus {
			ti.Focus()
		}
		return ti
	}
	defaultModel := m.Model
	if defaultModel == "" {
		defaultModel = "claude-opus-4-7"
	}

	ta := textarea.New()
	ta.Placeholder = "system prompt — define the agent's persona"
	ta.SetValue(m.Prompt)
	ta.CharLimit = 0
	ta.SetHeight(8)

	effortIdx := indexOfDefault(agentFormEffortChoices, m.Effort, "max")
	providerIdx := 0
	if m.Provider != "" {
		for i, p := range agentFormProviderChoices {
			if p == m.Provider {
				providerIdx = i
				break
			}
		}
	}

	d := &AgentFormDialog{
		client:      c,
		editID:      m.EditID,
		name:        mk("name", m.Name, 64, true),
		desc:        mk("description (optional)", m.Description, 240, false),
		model:       mk("model", defaultModel, 64, false),
		effortIdx:   effortIdx,
		providerIdx: providerIdx,
		prompt:      ta,
		styles:      s,
	}
	if m.EditID != "" {
		d.loading = true // fetch full prompt async
	}
	return d
}

// indexOfDefault returns the index of want in opts, or the index of
// fallback if want isn't present, or 0.
func indexOfDefault(opts []string, want, fallback string) int {
	for i, o := range opts {
		if o == want {
			return i
		}
	}
	for i, o := range opts {
		if o == fallback {
			return i
		}
	}
	return 0
}

func (d *AgentFormDialog) SetStyles(s Styles) { d.styles = s }

func (d *AgentFormDialog) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink}
	if d.editID != "" {
		// Fetch the full agent so we can prefill the prompt textarea —
		// the picker only had AgentSummary which omits the prompt body.
		cmds = append(cmds, d.fetchPromptCmd())
	}
	return tea.Batch(cmds...)
}

func (d *AgentFormDialog) fetchPromptCmd() tea.Cmd {
	c := d.client
	id := d.editID
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		detail, err := c.GetAgent(ctx, id)
		if err != nil {
			return AgentDetailLoadedMsg{ID: id, Err: err}
		}
		return AgentDetailLoadedMsg{ID: id, Prompt: detail.Prompt}
	}
}

func (d *AgentFormDialog) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case AgentDetailLoadedMsg:
		d.loading = false
		if m.Err != nil {
			d.err = "load: " + m.Err.Error()
			return nil
		}
		// Only prefill if the user hasn't typed anything yet — don't
		// clobber edits that arrived during the round-trip.
		if d.prompt.Value() == "" && m.Prompt != "" {
			d.prompt.SetValue(m.Prompt)
		}
		return nil
	case AgentSavedMsg:
		d.saving = false
		if m.Err != nil {
			d.err = m.Err.Error()
			return nil
		}
		return func() tea.Msg { return CloseDialogMsg{} }
	}

	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc":
			return func() tea.Msg { return CloseDialogMsg{} }
		case "tab":
			d.focusNext(1)
			return nil
		case "shift+tab":
			d.focusNext(-1)
			return nil
		case "ctrl+s":
			return d.submit()
		}
	}

	// Effort and Provider focus levels use left/right arrows to cycle
	// through the radio. Intercept those before routing to the
	// generic textinput handler below.
	if k, ok := msg.(tea.KeyMsg); ok {
		key := k.String()
		switch d.focus {
		case agFocusEffort:
			if key == "left" || key == "h" {
				if d.effortIdx > 0 {
					d.effortIdx--
				}
				return nil
			}
			if key == "right" || key == "l" || key == " " {
				if d.effortIdx < len(agentFormEffortChoices)-1 {
					d.effortIdx++
				}
				return nil
			}
		case agFocusProvider:
			if key == "left" || key == "h" {
				if d.providerIdx > 0 {
					d.providerIdx--
				}
				return nil
			}
			if key == "right" || key == "l" || key == " " {
				if d.providerIdx < len(agentFormProviderChoices)-1 {
					d.providerIdx++
				}
				return nil
			}
		}
	}

	// Route key/text events to the focused textinput / textarea.
	var cmd tea.Cmd
	switch d.focus {
	case agFocusName:
		d.name, cmd = d.name.Update(msg)
	case agFocusDescription:
		d.desc, cmd = d.desc.Update(msg)
	case agFocusModel:
		d.model, cmd = d.model.Update(msg)
	case agFocusPrompt:
		d.prompt, cmd = d.prompt.Update(msg)
	}
	return cmd
}

func (d *AgentFormDialog) focusNext(delta int) {
	d.focus = agentFormFocus((int(d.focus) + delta + int(numAgentFocus)) % int(numAgentFocus))
	d.applyFocus()
}

func (d *AgentFormDialog) applyFocus() {
	d.name.Blur()
	d.desc.Blur()
	d.model.Blur()
	d.prompt.Blur()
	switch d.focus {
	case agFocusName:
		d.name.Focus()
	case agFocusDescription:
		d.desc.Focus()
	case agFocusModel:
		d.model.Focus()
	case agFocusPrompt:
		d.prompt.Focus()
	}
}

func (d *AgentFormDialog) submit() tea.Cmd {
	name := strings.TrimSpace(d.name.Value())
	if name == "" {
		d.err = "name is required"
		return nil
	}
	model := strings.TrimSpace(d.model.Value())
	if model == "" {
		d.err = "model is required"
		return nil
	}
	if d.saving {
		return nil
	}
	d.saving = true
	d.err = ""
	provider := agentFormProviderChoices[d.providerIdx]
	if provider == "(default)" {
		provider = ""
	}
	payload := SubmitAgentFormMsg{
		EditID:      d.editID,
		Name:        name,
		Description: strings.TrimSpace(d.desc.Value()),
		Model:       model,
		Effort:      agentFormEffortChoices[d.effortIdx],
		Provider:    provider,
		Prompt:      d.prompt.Value(),
	}
	return func() tea.Msg { return payload }
}

func (d *AgentFormDialog) View(width, height int) string {
	boxW := 72
	if boxW > width-4 {
		boxW = width - 4
	}
	if boxW < 40 {
		boxW = 40
	}
	innerW := boxW - 6
	d.name.SetWidth(innerW)
	d.desc.SetWidth(innerW)
	d.model.SetWidth(innerW)
	d.prompt.SetWidth(innerW)

	titleText := "New agent"
	if d.editID != "" {
		titleText = "Edit agent"
	}
	title := HatchedTitle(titleText, innerW, colPrimary, colAccent, d.styles.DialogTitle)

	saveLabel := "save"
	if d.saving {
		saveLabel = "saving…"
	}
	hints := d.styles.StatusKey.Render("ctrl+s") + d.styles.Hint.Render(" "+saveLabel+"  ") +
		d.styles.StatusKey.Render("tab") + d.styles.Hint.Render(" next field  ") +
		d.styles.StatusKey.Render("esc") + d.styles.Hint.Render(" cancel")

	lines := []string{
		title,
		"",
		d.fieldLabel("name", d.focus == agFocusName),
		d.name.View(),
		d.fieldLabel("description", d.focus == agFocusDescription),
		d.desc.View(),
		d.fieldLabel("model", d.focus == agFocusModel),
		d.model.View(),
		d.fieldLabel("effort", d.focus == agFocusEffort),
		renderRadioRow(agentFormEffortChoices, d.effortIdx, d.focus == agFocusEffort, d.styles),
		d.fieldLabel("provider", d.focus == agFocusProvider),
		renderRadioRow(agentFormProviderChoices, d.providerIdx, d.focus == agFocusProvider, d.styles),
		d.fieldLabel("prompt", d.focus == agFocusPrompt),
		d.prompt.View(),
	}
	if d.loading {
		lines = append(lines, "", "  "+d.styles.Hint.Render("loading existing agent…"))
	}
	if d.err != "" {
		lines = append(lines, "", "  "+d.styles.ResultError.Render("✗ "+d.err))
	}
	lines = append(lines, "", hints)

	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}

func (d *AgentFormDialog) fieldLabel(text string, focused bool) string {
	if focused {
		return d.styles.UserPrompt.Render("▸ ") + d.styles.HeaderTitle.Render(text)
	}
	return "  " + d.styles.HeaderDim.Render(text)
}
