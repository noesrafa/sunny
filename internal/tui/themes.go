package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Palette is the bag of colors the rest of the styles reference. We keep
// it as a flat struct (not nested) so swapping at runtime is a single
// assignment, and so package-level helpers (e.g. modal title gradients)
// can read named fields without threading a Styles instance.
//
// Each entry is built from a hex string at construction via the helper hex().
// We store color.Color (lipgloss v2's actual color type) rather than raw
// strings so styles can use them directly.
type Palette struct {
	Primary   color.Color
	Secondary color.Color
	Tertiary  color.Color
	Accent    color.Color
	Success   color.Color
	Warning   color.Color
	Danger    color.Color
	Muted     color.Color
	Text      color.Color
	Border    color.Color

	// OnAccent is the foreground color used on top of Primary/Secondary
	// backgrounds (selected buttons, badges). It is NOT the same as Text:
	// Text needs to read on the terminal background, OnAccent needs to read
	// on a saturated UI accent. For most themes both happen to be a near-
	// white, but Whisper Dark (Primary = near-white) needs OnAccent dark.
	OnAccent color.Color

	// Logo gradient endpoints. Most themes reuse Secondary/Primary, but
	// having dedicated fields lets a theme tune the brand mark separately.
	LogoTop color.Color
	LogoBot color.Color
	LogoVer color.Color
}

// hex is a thin alias for lipgloss.Color so theme literals stay readable.
func hex(s string) color.Color { return lipgloss.Color(s) }

// Theme bundles a palette with display metadata for the settings picker.
type Theme struct {
	ID     string  // stable key persisted in state.json
	Name   string  // shown in the picker
	Light  bool    // true if the theme is designed for a light terminal background
	PairID string  // ID of the dark/light counterpart, used by Auto mode
	P      Palette // the actual colors
}

// AutoThemeID is a sentinel theme id meaning "follow the terminal
// background". When this is the active id, repaint() resolves it at
// runtime via ResolveTheme based on the most recent BackgroundColorMsg.
const AutoThemeID = "auto"

// AutoDarkDefaultID and AutoLightDefaultID are the concrete themes Auto
// mode falls back to. We pick Charple in both modes so the brand feel
// stays consistent across light/dark switches.
const (
	AutoDarkDefaultID  = "charple-dark"
	AutoLightDefaultID = "charple-light"
)

// Themes is the curated list, in display order. Every flavor ships with
// both a dark and a light Palette and they are paired via PairID so
// ResolveTheme can flip them based on the terminal background. The
// settings dialog renders one row per *flavor* (the dark canonical),
// not one row per Palette — users pick a vibe, not a polarity.
//
//   - Charple        — vibrant electric (default)
//   - Sunset         — warm romantic
//   - Tokyo Night    — cool moody, blue/violet/cyan band
//   - Synthwave      — neon party
//   - Fallout        — Pip-Boy CRT, monochrome green discipline
//   - Whisper        — quiet mono gray, almost no color
//
// Constraints applied to every entry:
//   - Primary, Secondary, Tertiary, Accent are visually distinct hues so
//     the spinner / cursor / focus underline don't collapse together.
//   - Success/Warning/Danger keep semantic meaning regardless of the rest.
//   - Light variants deepen Primary/Secondary so OnAccent=#FFFFFF reads
//     cleanly when used as a button foreground.
var Themes = []Theme{
	{
		ID:     "charple-dark",
		Name:   "Charple",
		PairID: "charple-light",
		P: Palette{
			Primary:   hex("#6B50FF"), // Charple purple
			Secondary: hex("#FF60FF"), // Dolly magenta
			Tertiary:  hex("#68FFD6"), // Bok mint
			Accent:    hex("#BD93F9"),
			Success:   hex("#68FFD6"),
			Warning:   hex("#FFB86C"),
			Danger:    hex("#FF5555"),
			Muted:     hex("#858392"),
			Text:      hex("#DFDBDD"),
			Border:    hex("#44475A"),
			OnAccent:  hex("#FFFFFF"),
			LogoTop:   hex("#FF60FF"),
			LogoBot:   hex("#6B50FF"),
			LogoVer:   hex("#68FFD6"),
		},
	},
	{
		ID:     "charple-light",
		Name:   "Charple Light",
		Light:  true,
		PairID: "charple-dark",
		P: Palette{
			// Same brand DNA (purple → pink → mint) but every accent is
			// deepened so it reads on a near-white terminal background.
			Primary:   hex("#5B3FE0"), // deep Charple purple
			Secondary: hex("#C026D3"), // deep magenta — readable on white
			Tertiary:  hex("#0D9488"), // deep teal-mint
			Accent:    hex("#7C3AED"), // royal violet
			Success:   hex("#0F766E"),
			Warning:   hex("#B45309"),
			Danger:    hex("#B91C1C"),
			Muted:     hex("#6B7280"), // medium gray — readable on white
			Text:      hex("#1F1B2E"), // near-black with subtle purple tint
			Border:    hex("#D4D0E0"), // soft purple-gray rule
			OnAccent:  hex("#FFFFFF"), // white reads on every accent above
			LogoTop:   hex("#C026D3"),
			LogoBot:   hex("#5B3FE0"),
			LogoVer:   hex("#0D9488"),
		},
	},
	{
		ID:     "sunset-dark",
		Name:   "Sunset",
		PairID: "sunset-light",
		P: Palette{
			Primary:   hex("#F97316"), // orange
			Secondary: hex("#EC4899"), // hot pink (cursor)
			Tertiary:  hex("#FBBF24"), // amber (focused prompt)
			Accent:    hex("#C084FC"), // lavender (tools)
			Success:   hex("#34D399"),
			Warning:   hex("#F59E0B"),
			Danger:    hex("#F87171"),
			Muted:     hex("#94A3B8"),
			Text:      hex("#F8FAFC"),
			Border:    hex("#334155"),
			OnAccent:  hex("#FFFFFF"),
			LogoTop:   hex("#EC4899"),
			LogoBot:   hex("#F97316"),
			LogoVer:   hex("#FBBF24"),
		},
	},
	{
		ID:     "sunset-light",
		Name:   "Sunset Light",
		Light:  true,
		PairID: "sunset-dark",
		P: Palette{
			Primary:   hex("#EA580C"), // deep orange — readable on white
			Secondary: hex("#DB2777"), // deep hot-pink
			Tertiary:  hex("#D97706"), // deep amber
			Accent:    hex("#9333EA"), // deep lavender
			Success:   hex("#059669"),
			Warning:   hex("#B45309"),
			Danger:    hex("#DC2626"),
			Muted:     hex("#64748B"),
			Text:      hex("#0F172A"),
			Border:    hex("#E2E8F0"),
			OnAccent:  hex("#FFFFFF"),
			LogoTop:   hex("#DB2777"),
			LogoBot:   hex("#EA580C"),
			LogoVer:   hex("#D97706"),
		},
	},
	{
		// Premium / monochromatic-blue feel: every accent sits in the
		// blue→violet→cyan band so the UI reads as one cohesive palette.
		// Success/Warning/Danger are pulled toward the same value range
		// (muted teal, brushed gold, dusty rose) so they signal clearly
		// without breaking the cool-blue mood. Border is barely visible
		// against a dark terminal — the negative space carries the work
		// instead of competing rules.
		// Premium / monochromatic-blue feel: every accent sits in the
		// blue→violet→cyan band so the UI reads as one cohesive palette.
		// Success/Warning/Danger are pulled toward the same value range
		// (muted teal, brushed gold, dusty rose) so they signal clearly
		// without breaking the cool-blue mood. Border is barely visible
		// against a dark terminal — the negative space carries the work
		// instead of competing rules.
		ID:     "tokyo-night",
		Name:   "Tokyo Night",
		PairID: "tokyo-night-light",
		P: Palette{
			Primary:   hex("#7AA2F7"), // soft blue
			Secondary: hex("#BB9AF7"), // violet (cursor)
			Tertiary:  hex("#7DCFFF"), // cyan (focused prompt)
			Accent:    hex("#9D7CD8"), // deep purple (tools)
			Success:   hex("#73DACA"), // muted teal — same value as blue accents
			Warning:   hex("#C9A769"), // brushed gold — desaturated, not loud yellow
			Danger:    hex("#E06C75"), // dusty rose-red, calmer than #F7768E
			Muted:     hex("#545C7E"), // deep blue-gray, dims confidently
			Text:      hex("#C0CAF5"), // cool off-white with blue cast
			Border:    hex("#1F2335"), // near-black so panel rules recede
			OnAccent:  hex("#1A1B26"), // near-black reads on the soft blue Primary
			LogoTop:   hex("#BB9AF7"),
			LogoBot:   hex("#7AA2F7"),
			LogoVer:   hex("#7DCFFF"),
		},
	},
	{
		// Tokyo Night Day: the blue/violet/cyan band, but every accent
		// deepened so it reads on a near-white terminal. Body text is the
		// canonical Tokyo Day navy (#3760BF) — keeps the palette feeling
		// "Tokyo Night" instead of generic light.
		ID:     "tokyo-night-light",
		Name:   "Tokyo Night Light",
		Light:  true,
		PairID: "tokyo-night",
		P: Palette{
			Primary:   hex("#2E7DE9"), // deep Tokyo blue
			Secondary: hex("#9854F1"), // deep violet
			Tertiary:  hex("#007197"), // deep cyan
			Accent:    hex("#5C30C2"), // royal violet
			Success:   hex("#587539"), // sage
			Warning:   hex("#8C6C3E"), // brushed gold, deepened
			Danger:    hex("#C64343"), // rust red
			Muted:     hex("#687387"), // medium blue-gray
			Text:      hex("#3760BF"), // Tokyo Day navy body copy
			Border:    hex("#C1C2CE"), // soft blue-gray rule
			OnAccent:  hex("#FFFFFF"),
			LogoTop:   hex("#9854F1"),
			LogoBot:   hex("#2E7DE9"),
			LogoVer:   hex("#007197"),
		},
	},
	{
		ID:     "synthwave",
		Name:   "Synthwave",
		PairID: "synthwave-light",
		P: Palette{
			Primary:   hex("#FF7EDB"), // neon pink
			Secondary: hex("#FF6EC7"), // hot magenta (cursor)
			Tertiary:  hex("#36F9F6"), // electric cyan
			Accent:    hex("#B084EB"), // electric lavender
			Success:   hex("#72F1B8"), // mint
			Warning:   hex("#FEDE5D"), // canary
			Danger:    hex("#FE4450"), // signal red
			Muted:     hex("#848BBD"),
			Text:      hex("#F8F8F2"),
			Border:    hex("#3B305B"),
			OnAccent:  hex("#241B2F"), // deep purple reads on neon pink
			LogoTop:   hex("#FF7EDB"),
			LogoBot:   hex("#36F9F6"),
			LogoVer:   hex("#FEDE5D"),
		},
	},
	{
		// Synthwave Daylight: same neon DNA (pink → magenta → cyan)
		// punched down a couple of stops so it pops on white. Borders
		// are barely-there pink so panel rules don't lose the vibe.
		ID:     "synthwave-light",
		Name:   "Synthwave Light",
		Light:  true,
		PairID: "synthwave",
		P: Palette{
			Primary:   hex("#C026D3"), // deep neon pink
			Secondary: hex("#BE185D"), // deep magenta
			Tertiary:  hex("#0891B2"), // deep cyan
			Accent:    hex("#6D28D9"), // electric lavender
			Success:   hex("#047857"), // deep mint
			Warning:   hex("#B45309"), // deep canary-gold
			Danger:    hex("#BE123C"), // signal red, deepened
			Muted:     hex("#71717A"),
			Text:      hex("#18181B"), // near-black
			Border:    hex("#F0ABFC"), // pale pink rule keeps the synth vibe
			OnAccent:  hex("#FFFFFF"),
			LogoTop:   hex("#C026D3"),
			LogoBot:   hex("#0891B2"),
			LogoVer:   hex("#B45309"),
		},
	},
	{
		// Pip-Boy 3000: full phosphor green monochrome on near-black. The
		// real Pip-Boy is a single-channel CRT — every glyph is the same
		// phosphor, only the brightness/saturation varies. We mirror that:
		// every accent sits in the green band, no amber rescue color. The
		// logo sweeps from a brighter scanline tip into deep phosphor for
		// that "RobCo terminal warming up" look.
		ID:     "fallout",
		Name:   "Fallout",
		PairID: "fallout-light",
		P: Palette{
			Primary:   hex("#33FF66"), // pure phosphor green
			Secondary: hex("#7FFFA0"), // brighter scanline lime (cursor)
			Tertiary:  hex("#5AFF8C"), // mid green (focused prompt)
			Accent:    hex("#9FFFB8"), // pale phosphor (tools)
			Success:   hex("#33FF66"),
			Warning:   hex("#A8FF60"), // yellow-green — same band, brighter
			Danger:    hex("#5AFF8C"), // bold green for errors too — Pip-Boy never breaks the palette
			Muted:     hex("#1F8040"), // dim green scanline
			Text:      hex("#7FFF99"), // soft phosphor body copy
			Border:    hex("#0E3318"), // near-black green bezel
			OnAccent:  hex("#0A1F0F"), // CRT-bezel black reads on the bright phosphor
			LogoTop:   hex("#9FFFB8"), // bright tip
			LogoBot:   hex("#1F8040"), // deep base
			LogoVer:   hex("#5AFF8C"),
		},
	},
	{
		// Fallout Light: same monochrome-green discipline (no rescue
		// color, danger stays in-band) but inverted — deep forest greens
		// on a near-white "lab terminal" rather than a CRT. The brand
		// rule still applies: every accent sits in the green band, so
		// the UI reads as one phosphor regardless of polarity.
		ID:     "fallout-light",
		Name:   "Fallout Light",
		Light:  true,
		PairID: "fallout",
		P: Palette{
			Primary:   hex("#15803D"), // deep forest green
			Secondary: hex("#166534"), // darker forest (cursor)
			Tertiary:  hex("#16A34A"), // vivid green (focused prompt)
			Accent:    hex("#14532D"), // very dark green (tools)
			Success:   hex("#15803D"),
			Warning:   hex("#65A30D"), // lime warning — still in-band
			Danger:    hex("#166534"), // dark green errors — Pip-Boy never breaks the palette
			Muted:     hex("#4D7C0F"), // olive
			Text:      hex("#052E16"), // near-black green body copy
			Border:    hex("#BBF7D0"), // pale green rule
			OnAccent:  hex("#F0FDF4"), // off-white reads on the deep forest Primary
			LogoTop:   hex("#16A34A"), // bright tip
			LogoBot:   hex("#14532D"), // deep base
			LogoVer:   hex("#15803D"),
		},
	},
	{
		// Whisper Dark: deliberately quiet light-on-dark monochrome.
		// Every accent is a different value of cool gray, with one
		// warm-white as Primary so the cursor and key ticks still pop.
		// Errors are the only break: a desaturated rust so they read as
		// "wrong" without nuking the calm.
		ID:     "whisper",
		Name:   "Whisper",
		PairID: "whisper-light",
		P: Palette{
			Primary:   hex("#F5F5F5"), // near-white
			Secondary: hex("#C8C8C8"), // light gray (cursor)
			Tertiary:  hex("#9E9E9E"), // mid gray (focused prompt)
			Accent:    hex("#7A7A7A"), // soft gray (tools)
			Success:   hex("#B0B0B0"), // gray "ok" — no green
			Warning:   hex("#D0D0D0"), // brighter gray for caution
			Danger:    hex("#C97D6E"), // muted rust — only color in the palette
			Muted:     hex("#5A5A5A"), // shadow gray
			Text:      hex("#E5E5E5"), // body copy
			Border:    hex("#2A2A2A"), // barely-there panel rule
			OnAccent:  hex("#1A1A1A"), // dark text on near-white Primary
			LogoTop:   hex("#F5F5F5"),
			LogoBot:   hex("#7A7A7A"),
			LogoVer:   hex("#9E9E9E"),
		},
	},
	{
		// Whisper Light: same hush, inverted. Body text is near-black on a
		// light terminal, every accent is a different value of warm gray
		// so nothing yells. Danger is the only break — a deeper rust that
		// signals "wrong" without raising the volume.
		ID:     "whisper-light",
		Name:   "Whisper Light (Mono)",
		Light:  true,
		PairID: "whisper",
		P: Palette{
			Primary:   hex("#1F2937"), // near-black slate (the loud one)
			Secondary: hex("#374151"), // dark gray (cursor)
			Tertiary:  hex("#4B5563"), // mid gray (focused prompt)
			Accent:    hex("#6B7280"), // soft mid gray (tools)
			Success:   hex("#52525B"), // gray "ok" — no green
			Warning:   hex("#71717A"), // mid gray for caution
			Danger:    hex("#B91C1C"), // rust red — the one break
			Muted:     hex("#9CA3AF"), // body-muted gray, readable on white
			Text:      hex("#111827"), // near-black body copy
			Border:    hex("#D1D5DB"), // soft rule
			OnAccent:  hex("#FFFFFF"), // white reads on the dark Primary
			LogoTop:   hex("#1F2937"),
			LogoBot:   hex("#6B7280"),
			LogoVer:   hex("#4B5563"),
		},
	},
}

// ThemeByID looks up a theme by its persisted ID. Returns the default
// (Themes[0]) when the id is unknown — keeps state.json forwards/backwards
// compatible if a theme gets renamed or removed.
func ThemeByID(id string) Theme {
	for _, t := range Themes {
		if t.ID == id {
			return t
		}
	}
	return Themes[0]
}

// ResolveTheme returns the concrete theme to apply given a user-selected
// id and the most recent terminal-background reading. Every paired theme
// auto-flips to its counterpart when the bg polarity doesn't match —
// Auto isn't a special id anymore, it's the default behavior.
//
//   - id == AutoThemeID (legacy state.json) → resolve as Charple
//   - any other id → look up, then swap to PairID if polarity mismatches
//
// bgIsLight defaults to false when no BackgroundColorMsg has arrived yet,
// so an unknown terminal lands on the dark variant of whatever flavor
// the user picked.
func ResolveTheme(id string, bgIsLight bool) Theme {
	if id == AutoThemeID {
		// Legacy: state.json from v0.11.x persisted "auto". Map it to
		// the Charple flavor; the polarity swap below handles bg.
		id = AutoDarkDefaultID
	}
	t := ThemeByID(id)
	if t.PairID != "" && bgIsLight != t.Light {
		return ThemeByID(t.PairID)
	}
	return t
}
