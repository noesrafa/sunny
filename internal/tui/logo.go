package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// Version is the string rendered in the logo's top-right corner.
// cmd/sunny sets this once at startup from its main.version (which
// the linker injects at release time via -ldflags). Stays as "dev"
// for `go build` without flags.
var Version = "dev"

// Dot-matrix SUNNY: each letter is a 5×5 logical pixel grid where every
// "pixel" is half a terminal cell tall. We pack two logical rows into one
// terminal row using ▀ (top half), ▄ (bottom half), and █ (both) so a
// terminal cell ≈ a square dot in display pixels (terminal cells are 1:2
// W:H, half-blocks are 1:1). 5 logical rows pack into 3 terminal rows
// (rows 1+2, 3+4, 5+empty); 5 letters × 5 cols + 4 single-col gaps = 29.
//
// Logical grid for reference:
//
//	S       U       N       Y
//	 ████   █   █   █   █   █   █
//	 █      █   █   ██  █   .█.█.
//	 ████   █   █   █ █ █   ..█..
//	    █   █   █   █  ██   ..█..
//	 ████   .███.   █   █   ..█..
//
// (Y is centred via the joint going █...█ → .█.█. → ..█.. → ..█.. → ..█..)
var sunnyBlock = []string{
	"▄▀▀▀▀ █   █ █▄  █ █▄  █ ▀▄ ▄▀",
	" ▀▀▀▄ █   █ █ ▀▄█ █ ▀▄█   █  ",
	"▀▀▀▀   ▀▀▀  ▀   ▀ ▀   ▀   ▀  ",
}

const logoBlockW = 29

// Logo gradient ramp cache. The ramp itself is identical every frame —
// only the per-column phase shifts — so recomputing Blend1D on each tick
// is pure waste. Cache by (top, bot) color identity; when the active
// palette changes (settings dialog), the next render rebuilds.
var (
	cachedLogoRamp    []color.Color
	cachedLogoRampTop color.Color
	cachedLogoRampBot color.Color

	cachedBrandRamp    []color.Color
	cachedBrandRampW   int
	cachedBrandRampTop color.Color
	cachedBrandRampBot color.Color
)

func logoColorRamp() []color.Color {
	if cachedLogoRamp == nil || cachedLogoRampTop != colLogoTop || cachedLogoRampBot != colLogoBot {
		cachedLogoRamp = lipgloss.Blend1D(logoBlockW, colLogoTop, colLogoBot)
		cachedLogoRampTop = colLogoTop
		cachedLogoRampBot = colLogoBot
	}
	return cachedLogoRamp
}

// brandColorRamp returns a width-wide ramp used to paint the brand row
// (sunny™ … vX.Y.Z) so it shares the same gradient as the SUNNY
// letters. Cached by (width, top, bot) — invalidates on resize and on
// palette swap.
func brandColorRamp(width int) []color.Color {
	if width < 1 {
		width = 1
	}
	if cachedBrandRamp == nil || cachedBrandRampW != width || cachedBrandRampTop != colLogoTop || cachedBrandRampBot != colLogoBot {
		cachedBrandRamp = lipgloss.Blend1D(width, colLogoTop, colLogoBot)
		cachedBrandRampW = width
		cachedBrandRampTop = colLogoTop
		cachedBrandRampBot = colLogoBot
	}
	return cachedBrandRamp
}

// renderLogo paints the brand mark with an animated gradient sweep across
// the SUNNY letters. `frame` is a monotonically-increasing counter
// (driven by logoTick in model.go); each step shifts the gradient phase
// by one column so the colors flow continuously left → right and bounce
// back. When frame is 0 the logo renders as a static gradient (the same
// as before the animation was added), so it's safe to call from non-tea
// contexts too.
//
// Layout (unchanged):
//
//	╱╱╱╱╱╱╱╱…   ← top hatching
//	sunny™ … v0.x.y
//	█████ █ █ ███ █ █ █ █   ← SUNNY block letters
//	╱╱╱╱╱╱╱╱…   ← bottom hatching
func renderLogo(width int, s Styles, frame int) string {
	if width < logoBlockW {
		width = logoBlockW
	}
	hatchTop := s.LogoTop.Render(strings.Repeat("╱", width))
	hatchBot := s.LogoBot.Render(strings.Repeat("╱", width))

	pad := (width - logoBlockW) / 2
	if pad < 0 {
		pad = 0
	}
	padStr := strings.Repeat(" ", pad)

	brandText := "sunny™"
	verText := "v" + Version
	leftW := lipgloss.Width(brandText)
	rightW := lipgloss.Width(verText)
	gap := width - leftW - rightW
	if gap < 1 {
		gap = 1
	}
	bramp := brandColorRamp(width)
	brand := colorizeText(brandText, bramp, 0, lipgloss.NewStyle().Italic(true))
	ver := colorizeText(verText, bramp, width-rightW, lipgloss.NewStyle())
	brandRow := brand + strings.Repeat(" ", gap) + ver

	// Build the per-column color ramp by sliding the LogoTop→LogoBot
	// gradient across a palindromic 2× space. The bounce gives the
	// animation a calm, breathing feel — straight wraparound felt jumpy.
	// We sample one ramp for the whole letter block (all 5 rows share
	// columns), so the same column lights up the same color top-to-
	// bottom — reads as a coherent vertical band sliding across.
	ramp := logoColorRamp()
	cols := make([]color.Color, logoBlockW)
	span := 2 * logoBlockW
	for x := 0; x < logoBlockW; x++ {
		pos := (x + frame) % span
		if pos < 0 {
			pos += span
		}
		if pos >= logoBlockW {
			pos = span - 1 - pos
		}
		cols[x] = ramp[pos]
	}

	var letters []string
	for _, row := range sunnyBlock {
		letters = append(letters, padStr+colorizeLetterRow(row, cols))
	}

	lines := []string{hatchTop, brandRow}
	lines = append(lines, letters...)
	lines = append(lines, hatchBot)
	return strings.Join(lines, "\n")
}

// colorizeLetterRow paints each filled cell ("█") with cols[x] and leaves
// spaces untouched. Building the styles per-cell would explode tokens for
// no visual gain, so we batch consecutive cells of the same color into
// one Render call.
func colorizeLetterRow(row string, cols []color.Color) string {
	var b strings.Builder
	runes := []rune(row)
	i := 0
	for i < len(runes) {
		// Skip blank runs verbatim.
		if runes[i] == ' ' {
			j := i
			for j < len(runes) && runes[j] == ' ' {
				j++
			}
			b.WriteString(string(runes[i:j]))
			i = j
			continue
		}
		// Group consecutive filled cells that share the same column color
		// (rare: identical adjacent ramp entries from rounding).
		j := i
		c := cols[i]
		for j < len(runes) && runes[j] != ' ' && j-i < 16 && colorsEqual(cols[j], c) {
			j++
		}
		b.WriteString(lipgloss.NewStyle().Foreground(c).Render(string(runes[i:j])))
		i = j
	}
	return b.String()
}

// colorizeText paints each rune of text with ramp[startCol+i], inheriting
// base (e.g. italic for the brand). Adjacent runes that share the same
// ramp color get batched into a single Render to keep the escape soup
// down. Assumes text is single-column-wide runes (true for "sunny™"
// and "vX.Y.Z").
func colorizeText(text string, ramp []color.Color, startCol int, base lipgloss.Style) string {
	runes := []rune(text)
	if len(runes) == 0 || len(ramp) == 0 {
		return text
	}
	var b strings.Builder
	i := 0
	for i < len(runes) {
		col := startCol + i
		if col < 0 {
			col = 0
		}
		if col >= len(ramp) {
			col = len(ramp) - 1
		}
		c := ramp[col]
		j := i + 1
		for j < len(runes) {
			ncol := startCol + j
			if ncol < 0 {
				ncol = 0
			}
			if ncol >= len(ramp) {
				ncol = len(ramp) - 1
			}
			if !colorsEqual(ramp[ncol], c) {
				break
			}
			j++
		}
		b.WriteString(base.Foreground(c).Render(string(runes[i:j])))
		i = j
	}
	return b.String()
}

// colorsEqual compares two color.Color values via their RGBA tuples.
// Cheap enough for the per-frame logo render and avoids importing reflect.
func colorsEqual(a, b color.Color) bool {
	if a == b {
		return true
	}
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}
