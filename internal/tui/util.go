package tui

import (
	"os"
	"strings"
)

func homedir() string {
	h, _ := os.UserHomeDir()
	return h
}

// defaultShell returns the user's preferred shell, falling back to /bin/bash
// if $SHELL is unset.
func defaultShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/bash"
}

// padRight returns s padded with spaces to width w, with at least one
// trailing space if s is already as wide or wider.
func padRight(s string, w int) string {
	if len(s) >= w {
		return s + " "
	}
	return s + strings.Repeat(" ", w-len(s))
}
