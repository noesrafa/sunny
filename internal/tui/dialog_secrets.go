package tui

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/noesrafa/sunny/internal/client"
)

// knownProviders is the catalogue the secrets dialog renders. Each
// entry declares which fields the user is expected to fill in. The
// list is intentionally hard-coded — every provider sunny knows about
// has its own driver and integration code anyway, so this list is
// never longer than what code already supports.
var knownProviders = []providerSpec{
	{Slug: "anthropic", Label: "Anthropic API", Fields: []fieldSpec{{Key: "api_key", Hint: "sk-ant-…"}}},
	{Slug: "openai", Label: "OpenAI API", Fields: []fieldSpec{{Key: "api_key", Hint: "sk-…"}}},
	{Slug: "ollama", Label: "Ollama Cloud", Fields: []fieldSpec{
		{Key: "api_key", Hint: "ollama.com key"},
		{Key: "base_url", Hint: "https://ollama.com (default)"},
	}},
}

type providerSpec struct {
	Slug   string
	Label  string
	Fields []fieldSpec
}

type fieldSpec struct {
	Key  string
	Hint string
}

// SecretsDialog is the top-level secrets manager: a list of known
// providers with status badges, plus a paste sub-form for whichever
// row the user activates.
type SecretsDialog struct {
	client    *client.Client
	loading   bool
	loadErr   string
	configured map[string][]string // provider → fields configured
	selected  int
	statusMsg string
	styles    Styles
}

// SecretsLoadedMsg lands the async list of configured providers.
type SecretsLoadedMsg struct {
	Items []client.SecretInfo
	Err   error
}

// SecretsSavedMsg fires after a successful PUT /secrets/{provider}.
type SecretsSavedMsg struct {
	Provider string
	Err      error
}

// OpenSecretsFormMsg asks the dialog stack to open the paste form for
// a specific provider. Carries the spec for label + fields.
type OpenSecretsFormMsg struct {
	Spec providerSpec
}

func NewSecretsDialog(c *client.Client, s Styles) *SecretsDialog {
	return &SecretsDialog{client: c, loading: true, configured: map[string][]string{}, styles: s}
}

func (d *SecretsDialog) SetStyles(s Styles) { d.styles = s }

func (d *SecretsDialog) Init() tea.Cmd {
	return d.loadCmd()
}

func (d *SecretsDialog) loadCmd() tea.Cmd {
	c := d.client
	return func() tea.Msg {
		if c == nil {
			return SecretsLoadedMsg{Err: errNoClient}
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		items, err := c.ListSecrets(ctx)
		return SecretsLoadedMsg{Items: items, Err: err}
	}
}

func (d *SecretsDialog) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case SecretsLoadedMsg:
		d.loading = false
		if m.Err != nil {
			d.loadErr = m.Err.Error()
			return nil
		}
		d.loadErr = ""
		d.configured = map[string][]string{}
		for _, it := range m.Items {
			d.configured[it.Provider] = it.Fields
		}
		return nil
	case SecretsSavedMsg:
		if m.Err != nil {
			d.statusMsg = "save failed: " + m.Err.Error()
			return nil
		}
		d.statusMsg = "saved " + m.Provider
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
			if d.selected < len(knownProviders)-1 {
				d.selected++
			}
			return nil
		case "enter":
			spec := knownProviders[d.selected]
			return func() tea.Msg {
				return OpenSubDialogMsg{Dialog: NewSecretsFormDialog(d.client, spec, d.styles)}
			}
		case "d", "delete":
			spec := knownProviders[d.selected]
			return func() tea.Msg { return DeleteSecretsMsg{Provider: spec.Slug} }
		}
	}
	return nil
}

func (d *SecretsDialog) View(width, height int) string {
	boxW := 60
	if boxW > width-4 {
		boxW = width - 4
	}
	if boxW < 36 {
		boxW = 36
	}
	innerW := boxW - 6
	title := HatchedTitle("Secrets", innerW, colPrimary, colAccent, d.styles.DialogTitle)

	lines := []string{title, ""}
	if d.loading {
		lines = append(lines, "  "+d.styles.Hint.Render("loading…"))
	} else if d.loadErr != "" {
		lines = append(lines, "  "+d.styles.ResultError.Render("✗ "+d.loadErr))
	} else {
		for i, p := range knownProviders {
			lines = append(lines, d.renderRow(p, i == d.selected))
		}
	}
	if d.statusMsg != "" {
		lines = append(lines, "", "  "+d.styles.Hint.Render(d.statusMsg))
	}
	hints := d.styles.StatusKey.Render("enter") + d.styles.Hint.Render(" set  ") +
		d.styles.StatusKey.Render("d") + d.styles.Hint.Render(" remove  ") +
		d.styles.StatusKey.Render("esc") + d.styles.Hint.Render(" close")
	lines = append(lines, "", hints)
	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}

func (d *SecretsDialog) renderRow(p providerSpec, selected bool) string {
	marker := "  "
	titleStyle := d.styles.AssistantText
	if selected {
		marker = d.styles.UserPrompt.Render("› ")
		titleStyle = d.styles.HeaderTitle
	}
	badge := d.styles.Hint.Render("○ empty")
	if fs, ok := d.configured[p.Slug]; ok && len(fs) > 0 {
		badge = d.styles.StatusIdle.Render("✓ " + strings.Join(fs, ", "))
	}
	return marker + titleStyle.Render(p.Label) + " " + d.styles.Hint.Render("·"+p.Slug) + "\n    " + badge
}

// DeleteSecretsMsg requests deletion of a provider's secrets section.
// Handled at the model level which calls the client.
type DeleteSecretsMsg struct{ Provider string }

// SubmitSecretsMsg is emitted by the form dialog when the user saves.
// The model handles the API call asynchronously and returns
// SecretsSavedMsg (consumed by both this dialog and the form).
type SubmitSecretsMsg struct {
	Provider string
	Fields   map[string]string
}

// SecretsFormDialog is the per-provider paste form. Each field gets
// its own textinput; tab cycles. Values are echoed as the user types
// (they pasted them deliberately, no point hiding them now —
// the file is mode 0600 anyway, and full masking interferes with
// paste verification). Trade-off discussed in CLAUDE.md.
type SecretsFormDialog struct {
	client *client.Client
	spec   providerSpec
	inputs []textinput.Model
	focus  int
	saving bool
	err    string
	styles Styles
}

func NewSecretsFormDialog(c *client.Client, spec providerSpec, s Styles) *SecretsFormDialog {
	inputs := make([]textinput.Model, len(spec.Fields))
	for i, f := range spec.Fields {
		ti := textinput.New()
		ti.Placeholder = f.Hint
		ti.Prompt = "› "
		ti.CharLimit = 0
		if i == 0 {
			ti.Focus()
		}
		inputs[i] = ti
	}
	return &SecretsFormDialog{client: c, spec: spec, inputs: inputs, styles: s}
}

func (d *SecretsFormDialog) SetStyles(s Styles) { d.styles = s }

func (d *SecretsFormDialog) Init() tea.Cmd { return textinput.Blink }

func (d *SecretsFormDialog) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case SecretsSavedMsg:
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
	if d.focus >= 0 && d.focus < len(d.inputs) {
		var cmd tea.Cmd
		d.inputs[d.focus], cmd = d.inputs[d.focus].Update(msg)
		return cmd
	}
	return nil
}

func (d *SecretsFormDialog) focusNext(delta int) {
	d.inputs[d.focus].Blur()
	d.focus = (d.focus + delta + len(d.inputs)) % len(d.inputs)
	d.inputs[d.focus].Focus()
}

func (d *SecretsFormDialog) submit() tea.Cmd {
	fields := map[string]string{}
	for i, sp := range d.spec.Fields {
		v := strings.TrimSpace(d.inputs[i].Value())
		if v != "" {
			fields[sp.Key] = v
		}
	}
	if len(fields) == 0 {
		d.err = "at least one field is required"
		return nil
	}
	if d.saving {
		return nil
	}
	d.saving = true
	d.err = ""
	provider := d.spec.Slug
	return func() tea.Msg { return SubmitSecretsMsg{Provider: provider, Fields: fields} }
}

func (d *SecretsFormDialog) View(width, height int) string {
	boxW := 64
	if boxW > width-4 {
		boxW = width - 4
	}
	if boxW < 40 {
		boxW = 40
	}
	innerW := boxW - 6
	for i := range d.inputs {
		d.inputs[i].SetWidth(innerW)
	}

	title := HatchedTitle(d.spec.Label, innerW, colPrimary, colAccent, d.styles.DialogTitle)
	lines := []string{title, ""}
	for i, sp := range d.spec.Fields {
		lines = append(lines,
			d.fieldLabel(sp.Key, i == d.focus),
			d.inputs[i].View(),
		)
	}
	if d.err != "" {
		lines = append(lines, "", "  "+d.styles.ResultError.Render("✗ "+d.err))
	}
	saveLabel := "save"
	if d.saving {
		saveLabel = "saving…"
	}
	hints := d.styles.StatusKey.Render("ctrl+s") + d.styles.Hint.Render(" "+saveLabel+"  ") +
		d.styles.StatusKey.Render("tab") + d.styles.Hint.Render(" next field  ") +
		d.styles.StatusKey.Render("esc") + d.styles.Hint.Render(" cancel")
	lines = append(lines, "", hints)
	return d.styles.DialogBox.Width(boxW).Render(strings.Join(lines, "\n"))
}

func (d *SecretsFormDialog) fieldLabel(text string, focused bool) string {
	if focused {
		return d.styles.UserPrompt.Render("▸ ") + d.styles.HeaderTitle.Render(text)
	}
	return "  " + d.styles.HeaderDim.Render(text)
}
