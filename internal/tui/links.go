package tui

import (
	"regexp"
	"strings"
)

// linkify wraps every URL in s with an OSC 8 hyperlink escape sequence so
// modern terminals (Ghostty, iTerm2, Kitty, Alacritty, WezTerm, etc.)
// render it as a clickable link. Cmd-click on macOS, Ctrl-click elsewhere
// — the convention is up to the terminal, not us.
//
// We linkify plain text only. Glamour-rendered markdown already emits
// OSC 8 for [text](url) and bare https://... links, so callers should
// NOT pass glamour output through here — it would just be a no-op since
// the URLs are already wrapped.
//
// The OSC 8 form is:
//
//	ESC ] 8 ; ; <url> ESC \ <text> ESC ] 8 ; ; ESC \
//
// Terminals that don't support OSC 8 silently strip the escape and the
// link renders as plain text — no visible junk, no broken layout.
func linkify(s string) string {
	if s == "" {
		return s
	}
	if !strings.Contains(s, "://") {
		return s
	}
	return urlPattern.ReplaceAllStringFunc(s, wrapURL)
}

// urlPattern matches http(s):// URLs greedily up to whitespace or a small
// set of clearly-terminal characters. Trailing "sentence punctuation"
// like .,;:!? is trimmed off in wrapURL so "see https://example.com."
// links to https://example.com (without the period).
var urlPattern = regexp.MustCompile(`https?://[^\s<>"'\x60\x00-\x1f]+`)

// punctTrimSet are the trailing characters we strip from a matched URL
// before wrapping. Mirrors what most chat clients / terminals do so a
// URL pasted at the end of a sentence doesn't drag the period along.
const punctTrimSet = ".,;:!?)]}>\""

func wrapURL(raw string) string {
	url := raw
	tail := ""
	for len(url) > 0 && strings.ContainsRune(punctTrimSet, rune(url[len(url)-1])) {
		tail = string(url[len(url)-1]) + tail
		url = url[:len(url)-1]
	}
	if url == "" {
		return raw
	}
	return "\x1b]8;;" + url + "\x1b\\" + url + "\x1b]8;;\x1b\\" + tail
}
