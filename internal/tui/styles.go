package tui

import (
	"image/color"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Fixed-palette colors. We avoid lipgloss.AdaptiveColor on purpose because
// adaptive colors trigger a synchronous OSC 11 query from inside lipgloss
// at runtime to detect the terminal background; that query's response was
// leaking back into the textarea on terminals that don't immediately
// drain it. Instead the model asks bubbletea for the background via
// tea.RequestBackgroundColor — bubbletea's input parser owns the response
// and surfaces it as tea.BackgroundColorMsg, never leaking to the app.
//
// These vars hold the *active* palette and are mutable: the settings
// dialog calls SetPalette() to swap them, then DefaultStyles() rebuilds
// every Style. Treat them as read-only outside SetPalette.
var (
	colPrimary   color.Color
	colSecondary color.Color
	colTertiary  color.Color
	colAccent    color.Color
	colSuccess   color.Color
	colWarning   color.Color
	colDanger    color.Color
	colMuted     color.Color
	colText      color.Color
	colBorder    color.Color
	colOnAccent  color.Color
	colLogoTop   color.Color
	colLogoBot   color.Color
	colLogoVer   color.Color
)

func init() {
	SetPalette(Themes[0].P)
}

// SetPalette swaps the active palette. Call DefaultStyles() afterwards to
// pick up the new colors in pre-built styles, and clear any markdown
// caches that bake colors into rendered output.
func SetPalette(p Palette) {
	colPrimary = p.Primary
	colSecondary = p.Secondary
	colTertiary = p.Tertiary
	colAccent = p.Accent
	colSuccess = p.Success
	colWarning = p.Warning
	colDanger = p.Danger
	colMuted = p.Muted
	colText = p.Text
	colBorder = p.Border
	colOnAccent = p.OnAccent
	if colOnAccent == nil {
		colOnAccent = p.Text // legacy fallback for any caller that constructs a Palette without OnAccent
	}
	colLogoTop = p.LogoTop
	colLogoBot = p.LogoBot
	colLogoVer = p.LogoVer
}

type Styles struct {
	HeaderLogo    lipgloss.Style
	HeaderTitle   lipgloss.Style
	HeaderDim     lipgloss.Style
	HeaderSep     lipgloss.Style

	UserPrompt lipgloss.Style
	UserText   lipgloss.Style

	AssistantPrompt lipgloss.Style
	AssistantText   lipgloss.Style
	AssistantThink  lipgloss.Style

	ToolPrompt lipgloss.Style
	ToolName   lipgloss.Style
	ToolInput  lipgloss.Style
	ToolResult lipgloss.Style

	ResultOK    lipgloss.Style
	ResultError lipgloss.Style
	ResultMeta  lipgloss.Style

	StatusBar  lipgloss.Style
	StatusBusy lipgloss.Style
	StatusIdle lipgloss.Style
	StatusKey  lipgloss.Style
	StatusDesc lipgloss.Style

	Input        lipgloss.Style
	InputFocused lipgloss.Style
	Hint         lipgloss.Style

	LogoTop   lipgloss.Style
	LogoBot   lipgloss.Style
	LogoVer   lipgloss.Style
	LogoBrand lipgloss.Style

	BtnSelected lipgloss.Style
	BtnPlain    lipgloss.Style

	// SelectModeBadge is the loud reverse-video pill in the status bar that
	// tells the user mouse capture is off and the terminal handles selection.
	SelectModeBadge lipgloss.Style

	DialogBox     lipgloss.Style
	DialogTitle   lipgloss.Style
	DialogWarning lipgloss.Style

	EditorTextarea textarea.Styles

	// Crush-style chat row prefixes (left border + padding).
	// User msg = thick block ▌ (focused) or thin │ (blurred), Charple color.
	// Assistant msg = padding-left 2, no border.
	UserMsgFocused      lipgloss.Style
	UserMsgBlurred      lipgloss.Style
	AssistantMsgBlurred lipgloss.Style
	AssistantMsgFocused lipgloss.Style

	// Attribution row: ◇ <model> · <duration>
	AttribIcon     lipgloss.Style
	AttribModel    lipgloss.Style
	AttribDuration lipgloss.Style
}

func DefaultStyles() Styles {
	return Styles{
		HeaderLogo:    lipgloss.NewStyle().Foreground(colWarning).Bold(true),
		HeaderTitle:   lipgloss.NewStyle().Foreground(colPrimary).Bold(true),
		HeaderDim:     lipgloss.NewStyle().Foreground(colMuted),
		HeaderSep:     lipgloss.NewStyle().Foreground(colBorder),

		UserPrompt: lipgloss.NewStyle().Foreground(colSecondary).Bold(true),
		UserText:   lipgloss.NewStyle().Foreground(colText),

		AssistantPrompt: lipgloss.NewStyle().Foreground(colPrimary).Bold(true),
		AssistantText:   lipgloss.NewStyle().Foreground(colText),
		AssistantThink:  lipgloss.NewStyle().Foreground(colMuted).Italic(true),

		ToolPrompt: lipgloss.NewStyle().Foreground(colAccent).Bold(true),
		ToolName:   lipgloss.NewStyle().Foreground(colAccent).Bold(true),
		ToolInput:  lipgloss.NewStyle().Foreground(colMuted),
		ToolResult: lipgloss.NewStyle().Foreground(colMuted),

		ResultOK:    lipgloss.NewStyle().Foreground(colSuccess),
		ResultError: lipgloss.NewStyle().Foreground(colDanger).Bold(true),
		ResultMeta:  lipgloss.NewStyle().Foreground(colMuted),

		StatusBar:  lipgloss.NewStyle().Foreground(colMuted),
		StatusBusy: lipgloss.NewStyle().Foreground(colWarning).Bold(true),
		StatusIdle: lipgloss.NewStyle().Foreground(colSuccess),
		StatusKey:  lipgloss.NewStyle().Foreground(colText).Bold(true),
		StatusDesc: lipgloss.NewStyle().Foreground(colMuted),

		Input: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colBorder).
			Padding(0, 1),
		InputFocused: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colPrimary).
			Padding(0, 1),
		Hint: lipgloss.NewStyle().Foreground(colMuted).Italic(true),

		LogoTop:   lipgloss.NewStyle().Foreground(colLogoTop).Bold(true),
		LogoBot:   lipgloss.NewStyle().Foreground(colLogoBot).Bold(true),
		LogoVer:   lipgloss.NewStyle().Foreground(colLogoVer),
		LogoBrand: lipgloss.NewStyle().Foreground(colMuted).Italic(true),

		BtnSelected: lipgloss.NewStyle().
			Foreground(colOnAccent).
			Background(colPrimary).
			Bold(true).
			Padding(0, 2),
		BtnPlain: lipgloss.NewStyle().
			Foreground(colMuted).
			Padding(0, 2),

		SelectModeBadge: lipgloss.NewStyle().
			Foreground(colOnAccent).
			Background(colSecondary).
			Bold(true).
			Padding(0, 1),

		DialogBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colPrimary).
			Padding(1, 2),
		DialogTitle:   lipgloss.NewStyle().Foreground(colPrimary).Bold(true),
		DialogWarning: lipgloss.NewStyle().Foreground(colWarning).Bold(true),

		EditorTextarea: crushTextareaStyles(),

		// User message: ▌/│ left-border in Charple, 1 col padding.
		// Mirrors Crush's Messages.UserFocused/UserBlurred (quickstyle.go:797–803).
		UserMsgFocused: lipgloss.NewStyle().
			PaddingLeft(1).
			BorderLeft(true).
			BorderForeground(colPrimary).
			BorderStyle(lipgloss.Border{Left: "▌"}),
		UserMsgBlurred: lipgloss.NewStyle().
			PaddingLeft(1).
			BorderLeft(true).
			BorderForeground(colPrimary).
			BorderStyle(lipgloss.NormalBorder()),
		// Assistant message: just padding-left so the text aligns with the
		// space inside user borders. No border. (quickstyle.go:805)
		AssistantMsgBlurred: lipgloss.NewStyle().PaddingLeft(2),
		AssistantMsgFocused: lipgloss.NewStyle().
			PaddingLeft(1).
			BorderLeft(true).
			BorderForeground(colTertiary). // mint
			BorderStyle(lipgloss.Border{Left: "▌"}),

		// Attribution row at the end of each assistant turn:
		// "◇ Opus 4.7 · 7s"
		AttribIcon:     lipgloss.NewStyle().Foreground(colMuted),
		AttribModel:    lipgloss.NewStyle().Foreground(colMuted),
		AttribDuration: lipgloss.NewStyle().Foreground(colMuted),
	}
}

// crushTextareaStyles mirrors Crush's textarea.Styles config so the input
// field feels identical: muted prompt when blurred, accent prompt when
// focused, real block cursor that blinks. Source:
// /tmp/charm-crush/internal/ui/styles/quickstyle.go (s.Editor.Textarea).
func crushTextareaStyles() textarea.Styles {
	base := lipgloss.NewStyle()
	return textarea.Styles{
		Focused: textarea.StyleState{
			Base:             base,
			Text:             base.Foreground(colText),
			LineNumber:       base.Foreground(colMuted),
			CursorLine:       base,
			CursorLineNumber: base.Foreground(colMuted),
			Placeholder:      base.Foreground(colMuted).Italic(true),
			Prompt:           base.Foreground(colTertiary), // Bok mint, like Crush
		},
		Blurred: textarea.StyleState{
			Base:             base,
			Text:             base.Foreground(colMuted),
			LineNumber:       base.Foreground(colMuted),
			CursorLine:       base,
			CursorLineNumber: base.Foreground(colMuted),
			Placeholder:      base.Foreground(colMuted).Italic(true),
			Prompt:           base.Foreground(colMuted),
		},
		Cursor: textarea.CursorStyle{
			Color: colSecondary, // Dolly magenta — Crush's cursor exactly
			Shape: tea.CursorBlock,
			Blink: true,
		},
	}
}
