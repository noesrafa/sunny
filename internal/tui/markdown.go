package tui

import (
	"fmt"
	"image/color"

	"charm.land/glamour/v2/ansi"
	"github.com/alecthomas/chroma/v2/styles"
)

// glamourChromaTheme is the name glamour hardcodes when it registers the
// chroma syntax-highlight style derived from our StyleConfig. It lives in
// chroma's *global* styles.Registry, so the first render of the session
// wins: subsequent glamour.NewTermRenderer calls find the entry already
// present and skip re-registration. That's the bug behind "code blocks
// stay frozen on the first theme" — switching to light leaves variable
// names painted in the dark palette's colText (#DFDBDD), which renders
// as near-white on a light terminal and becomes invisible. We clear the
// entry every time we rebuild the renderer so the new palette wins.
const glamourChromaTheme = "charm"

// resetChromaStyle drops glamour's cached chroma style from the global
// registry. Call this right before glamour.NewTermRenderer so the new
// palette gets re-registered instead of being silently ignored.
func resetChromaStyle() {
	delete(styles.Registry, glamourChromaTheme)
}

// markdownStyleConfig builds a glamour ansi.StyleConfig derived from the
// active palette globals (colPrimary, colSecondary, colMuted, …). Glamour's
// stock "dark" style is palette-agnostic — it bakes blue headers, magenta
// inline code, and so on regardless of the rest of the UI. We trade the
// preset for a config that reads from the current theme so when the user
// switches to Fallout the markdown reads phosphor green, Whisper reads
// near-white-on-gray, etc.
//
// The structure mirrors Crush's quickstyle.go (stripped of charmtone
// hardcodes — we map every color back to a palette slot).
func markdownStyleConfig() ansi.StyleConfig {
	return ansi.StyleConfig{
		Document: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: hexPtr(colText),
			},
		},
		BlockQuote: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: hexPtr(colMuted),
			},
			Indent:      uintPtr(1),
			IndentToken: strPtr("│ "),
		},
		List: ansi.StyleList{
			LevelIndent: 2,
		},
		Heading: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				BlockSuffix: "\n",
				Color:       hexPtr(colSecondary),
				Bold:        boolPtr(true),
			},
		},
		H1: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix:          " ",
				Suffix:          " ",
				Color:           hexPtr(colText),
				BackgroundColor: hexPtr(colPrimary),
				Bold:            boolPtr(true),
			},
		},
		H2: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix: "## ",
				Color:  hexPtr(colSecondary),
				Bold:   boolPtr(true),
			},
		},
		H3: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix: "### ",
				Color:  hexPtr(colSecondary),
				Bold:   boolPtr(true),
			},
		},
		H4: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix: "#### ",
				Color:  hexPtr(colTertiary),
				Bold:   boolPtr(true),
			},
		},
		H5: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix: "##### ",
				Color:  hexPtr(colTertiary),
				Bold:   boolPtr(true),
			},
		},
		H6: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix: "###### ",
				Color:  hexPtr(colMuted),
				Bold:   boolPtr(false),
			},
		},
		Strikethrough: ansi.StylePrimitive{
			CrossedOut: boolPtr(true),
		},
		Emph: ansi.StylePrimitive{
			Italic: boolPtr(true),
		},
		Strong: ansi.StylePrimitive{
			Bold: boolPtr(true),
		},
		HorizontalRule: ansi.StylePrimitive{
			Color:  hexPtr(colBorder),
			Format: "\n--------\n",
		},
		Item: ansi.StylePrimitive{
			BlockPrefix: "• ",
		},
		Enumeration: ansi.StylePrimitive{
			BlockPrefix: ". ",
		},
		Task: ansi.StyleTask{
			StylePrimitive: ansi.StylePrimitive{},
			Ticked:         "[✓] ",
			Unticked:       "[ ] ",
		},
		Link: ansi.StylePrimitive{
			Color:     hexPtr(colMuted),
			Underline: boolPtr(true),
		},
		LinkText: ansi.StylePrimitive{
			Color: hexPtr(colTertiary),
			Bold:  boolPtr(true),
		},
		Image: ansi.StylePrimitive{
			Color:     hexPtr(colAccent),
			Underline: boolPtr(true),
		},
		ImageText: ansi.StylePrimitive{
			Color:  hexPtr(colMuted),
			Format: "Image: {{.text}} →",
		},
		Code: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix: " ",
				Suffix: " ",
				Color:  hexPtr(colDanger),
			},
		},
		CodeBlock: ansi.StyleCodeBlock{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{
					Color: hexPtr(colText),
				},
				Margin: uintPtr(2),
			},
			Chroma: &ansi.Chroma{
				Text: ansi.StylePrimitive{
					Color: hexPtr(colText),
				},
				Error: ansi.StylePrimitive{
					Color:           hexPtr(colText),
					BackgroundColor: hexPtr(colDanger),
				},
				Comment: ansi.StylePrimitive{
					Color: hexPtr(colMuted),
				},
				CommentPreproc: ansi.StylePrimitive{
					Color: hexPtr(colAccent),
				},
				Keyword: ansi.StylePrimitive{
					Color: hexPtr(colSecondary),
				},
				KeywordReserved: ansi.StylePrimitive{
					Color: hexPtr(colSecondary),
				},
				KeywordNamespace: ansi.StylePrimitive{
					Color: hexPtr(colSecondary),
				},
				KeywordType: ansi.StylePrimitive{
					Color: hexPtr(colTertiary),
				},
				Operator: ansi.StylePrimitive{
					Color: hexPtr(colAccent),
				},
				Punctuation: ansi.StylePrimitive{
					Color: hexPtr(colMuted),
				},
				Name: ansi.StylePrimitive{
					Color: hexPtr(colText),
				},
				NameBuiltin: ansi.StylePrimitive{
					Color: hexPtr(colTertiary),
				},
				NameTag: ansi.StylePrimitive{
					Color: hexPtr(colSecondary),
				},
				NameAttribute: ansi.StylePrimitive{
					Color: hexPtr(colAccent),
				},
				NameClass: ansi.StylePrimitive{
					Color:     hexPtr(colTertiary),
					Underline: boolPtr(true),
					Bold:      boolPtr(true),
				},
				NameDecorator: ansi.StylePrimitive{
					Color: hexPtr(colWarning),
				},
				NameFunction: ansi.StylePrimitive{
					Color: hexPtr(colTertiary),
				},
				LiteralNumber: ansi.StylePrimitive{
					Color: hexPtr(colSuccess),
				},
				LiteralString: ansi.StylePrimitive{
					Color: hexPtr(colWarning),
				},
				LiteralStringEscape: ansi.StylePrimitive{
					Color: hexPtr(colAccent),
				},
				GenericDeleted: ansi.StylePrimitive{
					Color: hexPtr(colDanger),
				},
				GenericEmph: ansi.StylePrimitive{
					Italic: boolPtr(true),
				},
				GenericInserted: ansi.StylePrimitive{
					Color: hexPtr(colSuccess),
				},
				GenericStrong: ansi.StylePrimitive{
					Bold: boolPtr(true),
				},
				GenericSubheading: ansi.StylePrimitive{
					Color: hexPtr(colMuted),
				},
				Background: ansi.StylePrimitive{},
			},
		},
		Table: ansi.StyleTable{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{},
			},
		},
		DefinitionDescription: ansi.StylePrimitive{
			BlockPrefix: "\n ",
		},
	}
}

// hexPtr converts a color.Color to a *string suitable for glamour's ansi
// style fields. Glamour wants "#RRGGBB" hex; we read the RGBA the color
// surface exposes and format it ourselves so we don't depend on the
// concrete lipgloss.Color type.
func hexPtr(c color.Color) *string {
	if c == nil {
		return strPtr("")
	}
	r, g, b, _ := c.RGBA()
	s := fmt.Sprintf("#%02X%02X%02X", uint8(r>>8), uint8(g>>8), uint8(b>>8))
	return &s
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }
func uintPtr(u uint) *uint    { return &u }
