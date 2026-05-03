package tui

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"charm.land/glamour/v2"
)

// TestMarkdownRepaintRefreshesChroma locks in the fix for the light-mode
// invisible-code bug. Glamour registers its chroma highlight style under
// the global key "charm" and skips re-registration if the entry already
// exists. Without resetChromaStyle, swapping the palette (e.g. dark→light)
// leaves code blocks painted in the *first* palette's colors — on a light
// terminal the dark palette's colText (#DFDBDD) reads as near-white-on-
// white and the variable names disappear.
//
// Glamour's terminal256 formatter emits chroma's syntax colors as palette
// indices like "\x1b[38;5;253m", whereas the outer indent/margin uses our
// truecolor RGB ("\x1b[38;2;31;27;46m") taken straight from StyleConfig.
// The outer margin always updates with the palette, even while bugged —
// so we have to assert on the chroma-emitted indexed colors specifically.
func TestMarkdownRepaintRefreshesChroma(t *testing.T) {
	const snippet = "```ts\nconst x = foo();\n```\n"

	render := func() string {
		resetChromaStyle()
		r, err := glamour.NewTermRenderer(
			glamour.WithStyles(markdownStyleConfig()),
			glamour.WithWordWrap(80),
		)
		if err != nil {
			t.Fatalf("glamour ctor: %v", err)
		}
		out, err := r.Render(snippet)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		return out
	}

	SetPalette(ThemeByID("charple-dark").P)
	darkColors := indexedColors(render())

	SetPalette(ThemeByID("charple-light").P)
	lightColors := indexedColors(render())

	if darkColors == lightColors {
		t.Fatalf("chroma syntax colors did not change between dark and light: %s\n"+
			"this means the global chroma styles.Registry kept the dark palette\n"+
			"and the new renderer reused it — resetChromaStyle isn't doing its job",
			darkColors)
	}

	SetPalette(Themes[0].P)
}

// indexedColors extracts the sorted set of "38;5;NNN" foreground codes
// that chroma emits for syntax highlighting. The truecolor "38;2;R;G;B"
// codes from glamour's outer style are deliberately ignored — those
// always update with the palette and would mask the chroma freeze bug.
var indexed256Re = regexp.MustCompile(`\x1b\[38;5;(\d+)m`)

func indexedColors(s string) string {
	matches := indexed256Re.FindAllStringSubmatch(s, -1)
	seen := make(map[string]bool, len(matches))
	for _, m := range matches {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}
