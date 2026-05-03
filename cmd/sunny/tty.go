package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// Helpers shared between commands that need to talk to the user via
// stdin (`secrets`, `setup`). Keeping them in one file means the
// "is this a tty? read a line, confirm a prompt" logic lives in
// exactly one place.

// isTTY reports whether stdin is connected to a terminal. Used to
// decide between interactive prompts and pipe-friendly plain reads.
func isTTY() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

// readLine reads one trimmed line from stdin. Empty string on EOF /
// error — callers decide what that means.
func readLine() string {
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}

// confirm reads a y/n answer from stdin, defaulting to YES on empty
// input and on a non-tty stdin (the caller already showed what's
// about to happen). Returns true for y/yes (any case).
func confirm() bool {
	if !isTTY() {
		return true
	}
	switch strings.ToLower(readLine()) {
	case "", "y", "yes":
		return true
	}
	return false
}

// readSecretValue reads a secret value from stdin. When stdin is a
// pipe, reads everything; when it's a tty, prompts interactively
// (echo is NOT suppressed — recommend piping for sensitive values;
// we deliberately don't pull in a terminal-control dep just for this).
//
// `prompt` is the human label shown when interactive ("anthropic.api_key").
func readSecretValue(prompt string) (string, error) {
	if !isTTY() {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	fmt.Fprintf(os.Stderr, "value for %s (will echo — pipe for sensitive values): ", prompt)
	return readLine(), nil
}
