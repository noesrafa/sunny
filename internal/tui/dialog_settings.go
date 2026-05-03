package tui

import (
	"image/color"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// SettingsDialog is the central preferences modal. Today it owns just the
// theme picker, but the layout (single section header + a list of choices)
// is sized to grow into multiple sections later (model defaults, keymap,
// etc.) without redesigning the dialog.
//
// Themes ship paired (a dark + a light Palette per flavor) and ResolveTheme
// auto-flips at runtime based on the terminal background. The picker
// renders ONE row per flavor — the dark canonical — and the swatches
// reflect whatever Palette is actually live right now (so on a light
// terminal you preview light swatches). Users pick a vibe, not a polarity.
type SettingsDialog struct {
	styles    Styles
	current   string         // active theme id at open time, used to mark "(actual)"
	cursor    int            // index into entries
	entries   []themeEntry   // flat list of selectable rows (canonical dark id per flavor)
	bgIsLight bool           // most recent bg reading; drives swatch palette per row
}

// themeEntry is a row in the picker. id is always the dark canonical id
// for the flavor — ResolveTheme will swap to the light pair at runtime
// when bg flips.
type themeEntry struct {
	id    string // dark canonical id passed back via Apply/Preview msgs
	theme Theme  // dark Theme used for the row name
}

// NewSettingsDialog wires the picker. bgIsLight is the most recent bg
// reading so the swatches accurately preview what each flavor will look
// like on the user's current terminal.
func NewSettingsDialog(currentThemeID string, bgIsLight bool, s Styles) *SettingsDialog {
	entries := buildThemeEntries()
	cursor := 0
	for i, e := range entries {
		// Match either the dark canonical or its light pair so the cursor
		// lands on the correct flavor regardless of which polarity is
		// currently persisted.
		if e.id == currentThemeID || e.theme.PairID == currentThemeID {
			cursor = i
			break
		}
	}
	return &SettingsDialog{
		styles:    s,
		current:   currentThemeID,
		cursor:    cursor,
		entries:   entries,
		bgIsLight: bgIsLight,
	}
}

// buildThemeEntries flattens the Themes catalog into one row per flavor.
// We pick the dark canonical (Light=false) as the row id; light pairs
// are skipped because ResolveTheme handles polarity at apply time.
// Dark themes without a PairID still get a row (they just won't flip).
func buildThemeEntries() []themeEntry {
	out := make([]themeEntry, 0, len(Themes))
	for _, t := range Themes {
		if t.Light {
			continue
		}
		out = append(out, themeEntry{id: t.ID, theme: t})
	}
	return out
}

// SetStyles refreshes the cached Styles when the root model rebuilds the
// palette mid-preview. Without this the dialog box keeps painting in the
// theme it was constructed with while the chat behind it already swapped.
func (d *SettingsDialog) SetStyles(s Styles) { d.styles = s }

// SetBgIsLight refreshes the polarity used to resolve the per-row swatches
// so they match what the user would actually see if they applied that row.
func (d *SettingsDialog) SetBgIsLight(bgIsLight bool) { d.bgIsLight = bgIsLight }

func (d *SettingsDialog) Init() tea.Cmd { return nil }

func (d *SettingsDialog) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "esc", "ctrl+c", "q":
		// Revert the live preview back to whatever was active when the
		// dialog opened, then close.
		original := d.current
		return func() tea.Msg { return CancelSettingsMsg{OriginalID: original} }
	case "up", "k":
		if d.cursor > 0 {
			d.cursor--
			return d.preview()
		}
	case "down", "j":
		if d.cursor < len(d.entries)-1 {
			d.cursor++
			return d.preview()
		}
	case "enter", " ":
		choice := d.entries[d.cursor]
		return func() tea.Msg { return ApplyThemeMsg{ID: choice.id} }
	}
	return nil
}

// preview emits a non-persisting theme swap so the user sees the hovered
// theme applied live. The dialog stays open and `current` is unchanged
// (so esc still knows which theme to roll back to).
func (d *SettingsDialog) preview() tea.Cmd {
	id := d.entries[d.cursor].id
	return func() tea.Msg { return PreviewThemeMsg{ID: id} }
}

func (d *SettingsDialog) View(width, _ int) string {
	boxW := 56
	if boxW > width-4 {
		boxW = width - 4
	}
	if boxW < 44 {
		boxW = 44
	}
	innerW := boxW - 6

	title := HatchedTitle("Settings", innerW, colPrimary, colAccent, d.styles.DialogTitle)

	header := d.styles.HeaderTitle.Render("theme")
	rule := d.styles.HeaderSep.Render(strings.Repeat("─", innerW))

	var rows []string
	rows = append(rows, title, "", header, rule)

	for i, e := range d.entries {
		rows = append(rows, d.renderRow(e, i == d.cursor, d.isCurrent(e), innerW))
	}

	footer := d.styles.Hint.Render("todos siguen el bg del terminal automáticamente")
	hint := d.styles.Hint.Render("↑↓ navegar · enter aplicar · esc cerrar")
	rows = append(rows, "", footer, "", hint)

	return d.styles.DialogBox.Width(boxW).Render(strings.Join(rows, "\n"))
}

// isCurrent matches the row against the persisted theme id, treating a
// light pair as equivalent to its dark canonical so the "(actual)" tag
// stays attached to the right flavor across bg flips.
func (d *SettingsDialog) isCurrent(e themeEntry) bool {
	if e.id == d.current {
		return true
	}
	return e.theme.PairID != "" && e.theme.PairID == d.current
}

// renderRow draws one option: a cursor caret, the flavor name, an
// "(actual)" marker on the active one, and a strip of color swatches
// rendered from whichever Palette would actually apply right now (the
// dark Palette on a dark terminal, the light Palette on a light one).
func (d *SettingsDialog) renderRow(e themeEntry, focused, active bool, innerW int) string {
	caret := "  "
	nameStyle := d.styles.AssistantText
	if focused {
		caret = d.styles.UserPrompt.Render("▸ ")
		nameStyle = d.styles.AssistantText.Bold(true)
	}

	tag := ""
	if active {
		tag = " " + d.styles.Hint.Render("(actual)")
	}

	// Resolve the flavor against the current bg so swatches match what
	// the user would see if they applied this row. ResolveTheme handles
	// the dark→light swap when PairID exists.
	livePalette := ResolveTheme(e.id, d.bgIsLight).P
	swatches := buildSwatches(livePalette)

	leftPart := caret + nameStyle.Render(e.theme.Name) + tag
	pad := innerW - lipgloss.Width(leftPart) - lipgloss.Width(swatches)
	if pad < 1 {
		pad = 1
	}
	return leftPart + strings.Repeat(" ", pad) + swatches
}

// buildSwatches paints a tiny "●" per palette accent so the user can see
// at a glance what they're picking. Order kept stable across themes.
func buildSwatches(p Palette) string {
	colors := []color.Color{p.Primary, p.Secondary, p.Tertiary, p.Accent, p.Warning, p.Danger}
	var sb strings.Builder
	for i, c := range colors {
		if i > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(lipgloss.NewStyle().Foreground(c).Render("●"))
	}
	return sb.String()
}
