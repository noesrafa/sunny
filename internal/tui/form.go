package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// inputSpec describes one row of a form: placeholder, initial value, and
// optional fixed width. Used by newTextInput.
type inputSpec struct {
	Placeholder string
	Value       string
	Width       int
}

// newTextInput is the standard "› " prompted text input used by every form
// dialog. Centralized so the prompt char and base width stay consistent.
func newTextInput(spec inputSpec) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = spec.Placeholder
	ti.Prompt = "› "
	ti.SetValue(spec.Value)
	w := spec.Width
	if w <= 0 {
		w = 60
	}
	ti.SetWidth(w)
	return ti
}

// formInputs is a small helper for dialogs that have N text inputs with
// rotating focus (tab / shift+tab) and pass-through msg dispatch to the
// active one. It owns the inputs by value because textinput.Model is itself
// a value type.
type formInputs struct {
	inputs []textinput.Model
	focus  int
}

// newFormInputs builds N inputs from specs and focuses the first one.
func newFormInputs(specs []inputSpec) formInputs {
	inputs := make([]textinput.Model, len(specs))
	for i, sp := range specs {
		inputs[i] = newTextInput(sp)
	}
	if len(inputs) > 0 {
		inputs[0].Focus()
	}
	return formInputs{inputs: inputs}
}

// advance rotates focus by `by` (typically +1 for tab, -1 for shift+tab).
// Wraps both directions.
func (f *formInputs) advance(by int) {
	if len(f.inputs) == 0 {
		return
	}
	f.inputs[f.focus].Blur()
	n := len(f.inputs)
	f.focus = (f.focus + by + n) % n
	f.inputs[f.focus].Focus()
}

// updateActive forwards a message to the currently focused input, which is
// what callers do for any non-shortcut key.
func (f *formInputs) updateActive(msg tea.Msg) tea.Cmd {
	if len(f.inputs) == 0 {
		return nil
	}
	var cmd tea.Cmd
	f.inputs[f.focus], cmd = f.inputs[f.focus].Update(msg)
	return cmd
}

// setWidth resizes every input — call from View() with the dialog's inner
// content width.
func (f *formInputs) setWidth(w int) {
	for i := range f.inputs {
		f.inputs[i].SetWidth(w)
	}
}

func (f formInputs) value(i int) string { return f.inputs[i].Value() }
func (f formInputs) view(i int) string  { return f.inputs[i].View() }

// formFieldView renders one labeled row: a focused arrow prompt for the
// active field, dimmed otherwise, with the input view indented underneath.
func formFieldView(label string, focused bool, view string, s Styles) []string {
	var head string
	if focused {
		head = s.UserPrompt.Render("▸ ") + s.HeaderTitle.Render(label)
	} else {
		head = "  " + s.HeaderDim.Render(label)
	}
	return []string{head, "  " + view}
}

// renderRadioRow draws a horizontal radio: ●/○ marks + label per
// option, two spaces apart, with the active slot highlighted when the
// row is focused. Used wherever a dialog needs a one-of-N picker.
func renderRadioRow(opts []string, sel int, focused bool, s Styles) string {
	parts := make([]string, len(opts))
	for i, o := range opts {
		mark := "○"
		st := s.Hint
		if i == sel {
			mark = "●"
			st = s.StatusIdle
			if focused {
				st = s.UserPrompt
			}
		}
		parts[i] = st.Render(mark + " " + o)
	}
	return "  " + strings.Join(parts, "  ")
}
