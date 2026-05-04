package tui

import "strings"

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
