package terminal

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// KeyToBytes translates a Bubble Tea v2 key event into the byte sequence a
// terminal expects on its TTY. Covers the common subset (printable runes,
// control chars, arrows/home/end/pgup/pgdn, function keys, alt-prefix).
//
// Returns nil for keys we don't know how to encode — caller should ignore.
func KeyToBytes(msg tea.KeyPressMsg) []byte {
	s := msg.String()

	// Multi-rune paste / regular printable text comes as runes.
	if len(msg.Text) > 0 && !isControlString(s) {
		return []byte(msg.Text)
	}

	if seq, ok := simpleKeys[s]; ok {
		return []byte(seq)
	}

	// Alt+key — prefix ESC then encode as the bare key.
	if mod := msg.Mod; mod&tea.ModAlt != 0 {
		stripped := strings.TrimPrefix(s, "alt+")
		if seq, ok := simpleKeys[stripped]; ok {
			return append([]byte{0x1B}, []byte(seq)...)
		}
		if len(stripped) == 1 {
			return append([]byte{0x1B}, stripped[0])
		}
	}

	// Ctrl+letter → control character (1..26).
	if c := ctrlByte(s); c != 0 {
		return []byte{c}
	}

	// Fallback: send raw rune if it's a single printable char.
	if len(msg.Text) == 1 {
		return []byte(msg.Text)
	}
	return nil
}

// simpleKeys maps Bubble Tea key strings to literal byte sequences.
var simpleKeys = map[string]string{
	"enter":     "\r",
	"tab":       "\t",
	"backspace": "\x7f",
	"esc":       "\x1b",
	"space":     " ",

	"up":    "\x1b[A",
	"down":  "\x1b[B",
	"right": "\x1b[C",
	"left":  "\x1b[D",

	"home":   "\x1b[H",
	"end":    "\x1b[F",
	"pgup":   "\x1b[5~",
	"pgdown": "\x1b[6~",
	"insert": "\x1b[2~",
	"delete": "\x1b[3~",

	"shift+up":    "\x1b[1;2A",
	"shift+down":  "\x1b[1;2B",
	"shift+right": "\x1b[1;2C",
	"shift+left":  "\x1b[1;2D",
	"shift+tab":   "\x1b[Z",

	"ctrl+up":    "\x1b[1;5A",
	"ctrl+down":  "\x1b[1;5B",
	"ctrl+right": "\x1b[1;5C",
	"ctrl+left":  "\x1b[1;5D",

	"f1":  "\x1bOP",
	"f2":  "\x1bOQ",
	"f3":  "\x1bOR",
	"f4":  "\x1bOS",
	"f5":  "\x1b[15~",
	"f6":  "\x1b[17~",
	"f7":  "\x1b[18~",
	"f8":  "\x1b[19~",
	"f9":  "\x1b[20~",
	"f10": "\x1b[21~",
	"f11": "\x1b[23~",
	"f12": "\x1b[24~",
}

// ctrlByte returns the control byte (1..26) for "ctrl+a"..."ctrl+z", or 0.
func ctrlByte(s string) byte {
	if len(s) == 6 && s[:5] == "ctrl+" {
		c := s[5]
		if c >= 'a' && c <= 'z' {
			return c - 'a' + 1
		}
		if c >= 'A' && c <= 'Z' {
			return c - 'A' + 1
		}
	}
	// Special ctrl combos
	switch s {
	case "ctrl+@", "ctrl+ ":
		return 0x00
	case "ctrl+[":
		return 0x1b
	case "ctrl+\\":
		return 0x1c
	case "ctrl+]":
		return 0x1d
	case "ctrl+^":
		return 0x1e
	case "ctrl+_":
		return 0x1f
	}
	return 0
}

// isControlString returns true if s looks like a recognized control name
// (starts with a modifier prefix or is a known named key). We use it to skip
// the "msg.Text passthrough" for control combos.
func isControlString(s string) bool {
	if _, ok := simpleKeys[s]; ok {
		return true
	}
	for _, p := range []string{"ctrl+", "alt+", "shift+"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
