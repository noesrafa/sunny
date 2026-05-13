package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestSanitizeRender(t *testing.T) {
	cases := map[string]struct {
		in, want string
	}{
		"empty":        {"", ""},
		"plain":        {"hola mundo", "hola mundo"},
		"newline kept": {"a\nb", "a\nb"},
		// HTTP headers from curl -i — \r must be dropped or the terminal
		// cursor jumps back to col 0 mid-line.
		"strip cr": {"HTTP/1.1 401\r\nfoo", "HTTP/1.1 401\nfoo"},
		// ANSI color escapes must not survive into the chat layout.
		"strip ansi color": {"\x1b[31mred\x1b[0m line", "red line"},
		// Cursor-motion / screen-clear escapes are the scariest.
		"strip cursor cleanup": {"before\x1b[2Jafter", "beforeafter"},
		// Tab → 4 spaces so width math agrees with what the terminal paints.
		"tab to spaces": {"a\tb", "a    b"},
		// Other C0 controls + DEL drop silently (NUL, BEL, BS, VT, FF, DEL).
		"drop controls": {"a\x00b\x07c\x08d\x0be\x0cf\x7fg", "abcdefg"},
		// Multibyte UTF-8 must pass through untouched.
		"keep multibyte": {"≡≡≡mac mini≡≡≡", "≡≡≡mac mini≡≡≡"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := sanitizeRender(c.in)
			if got != c.want {
				t.Fatalf("sanitizeRender(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestTruncateVisualRuneSafe(t *testing.T) {
	// Each ≡ is 3 bytes UTF-8 / 1 cell wide. A byte slice at width 4 on
	// "≡≡≡≡≡" would land mid-codepoint and produce invalid UTF-8; the
	// rune-safe path keeps 3 codepoints + "…".
	in := "≡≡≡≡≡"
	got := truncateVisual(in, 4, "…")
	want := "≡≡≡…"
	if got != want {
		t.Fatalf("truncateVisual = %q, want %q", got, want)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("missing ellipsis tail: %q", got)
	}
}

func TestTruncateVisualNoCutWhenShort(t *testing.T) {
	in := "short"
	if got := truncateVisual(in, 20, "…"); got != in {
		t.Fatalf("short input mutated: %q", got)
	}
}

func TestTruncateVisualAnsiAware(t *testing.T) {
	// linkify output: OSC 8 wraps "https://example.com" with escape
	// sequences that don't take visible cells. ansi.Truncate must not
	// count them and must not chop them mid-sequence.
	url := "https://example.com"
	wrapped := "\x1b]8;;" + url + "\x1b\\" + url + "\x1b]8;;\x1b\\"
	got := truncateVisual(wrapped, len(url), "")
	if ansi.StringWidth(got) > len(url) {
		t.Fatalf("over-width: got width %d, max %d", ansi.StringWidth(got), len(url))
	}
}

func TestTruncateLinesHttpHeaders(t *testing.T) {
	// Real-world failure: curl -i output. Each line ended with \r before
	// the fix, which made the terminal repaint columns on top of each
	// other and produced the "scattered text" symptom in chat.
	raw := "HTTP/1.1 401 Unauthorized\r\nContent-Type: text/plain\r\n\r\nunauthorized\r\n"
	got := truncateLines(raw, 8, 80)
	if strings.ContainsRune(got, '\r') {
		t.Fatalf("CR survived truncateLines: %q", got)
	}
	if !strings.Contains(got, "401 Unauthorized") {
		t.Fatalf("missing payload: %q", got)
	}
}

func TestTruncateLinesMaxLines(t *testing.T) {
	in := strings.Repeat("a\n", 12)
	got := truncateLines(in, 3, 80)
	lines := strings.Split(got, "\n")
	// 3 kept lines + 1 ellipsis sentinel + 1 trailing empty from the
	// final \n of the input survives the split-then-cap.
	if len(lines) != 4 {
		t.Fatalf("want 4 lines (3 + ellipsis), got %d: %#v", len(lines), lines)
	}
	if lines[3] != "…" {
		t.Fatalf("want ellipsis sentinel last, got %q", lines[3])
	}
}

func TestCompactJSONSanitizes(t *testing.T) {
	// A model that emits ANSI inside a tool_use input would have broken
	// the header row pre-fix.
	raw := json.RawMessage("{\"cmd\":\"echo\t\x1b[31mhi\x1b[0m\"}")
	got := compactJSON(raw, 80)
	if strings.ContainsRune(got, '\x1b') {
		t.Fatalf("ANSI escape leaked through: %q", got)
	}
	if strings.ContainsRune(got, '\t') {
		t.Fatalf("tab leaked through: %q", got)
	}
}
