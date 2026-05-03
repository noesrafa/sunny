package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/noesrafa/sunny/internal/secrets"
)

// hasKey reports whether the given secret is reachable via the store
// or the env-var fallback. Tolerates a nil store.
func hasKey(store *secrets.Store, provider, field, env string) bool {
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		return true
	}
	if store == nil {
		return false
	}
	return strings.TrimSpace(store.Get(provider, field)) != ""
}

// briefVersion runs `<bin> <flag>` with a short timeout and returns
// the first token in the output that contains a digit (covers
// "1.14.33", "claude 1.2.3 (build …)", "opencode v0.5"). Empty
// return means the binary errored or didn't respond — callers
// should treat it as "couldn't get a version, but binary is there."
func briefVersion(bin, flag string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, flag).CombinedOutput()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for _, tok := range strings.Fields(line) {
			if hasDigit(tok) {
				return strings.TrimPrefix(tok, "v")
			}
		}
		return line
	}
	return ""
}

func hasDigit(s string) bool {
	for _, c := range s {
		if c >= '0' && c <= '9' {
			return true
		}
	}
	return false
}

// opencodeCredentialCount runs `opencode auth list` and counts the
// entries. opencode's output is a TUI-formatted human string today;
// we tolerate JSON in case a future version returns structured data.
// Returns -1 on probe failure (binary errored).
func opencodeCredentialCount(bin string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "auth", "list").CombinedOutput()
	if err != nil {
		return -1
	}
	var parsed []struct{}
	if err := json.Unmarshal(out, &parsed); err == nil {
		return len(parsed)
	}
	// Fallback: scan for the "N credentials" / "N credential" line.
	text := stripANSI(string(out))
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasSuffix(line, "credentials") && !strings.HasSuffix(line, "credential") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(parts[len(parts)-2], "%d", &n); err == nil {
			return n
		}
	}
	return -1
}

// vDetail formats a "v<ver> on PATH, <suffix>" string, gracefully
// dropping the version when we couldn't extract one.
func vDetail(ver, suffix string) string {
	if ver == "" {
		return suffix
	}
	return fmt.Sprintf("v%s on PATH, %s", ver, suffix)
}

// stripANSI removes ESC[…m escape sequences. Cheap state machine —
// good enough for parsing CLI output, not for binary data.
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	in := false
	for _, r := range s {
		if r == 0x1b {
			in = true
			continue
		}
		if in {
			if r == 'm' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				in = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// countAgentsAndConvs walks ~/.sunny/agents/<slug>/conversations
// shallowly. Errors short-circuit to (0,0); the caller's status flag
// covers that case.
func countAgentsAndConvs(root string) (agents, convs int) {
	agentsDir := filepath.Join(root, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		agents++
		convDir := filepath.Join(agentsDir, e.Name(), "conversations")
		convEntries, err := os.ReadDir(convDir)
		if err != nil {
			continue
		}
		for _, c := range convEntries {
			if c.IsDir() {
				convs++
			}
		}
	}
	return agents, convs
}

// humanDuration prints "37s", "5m12s", "1h2m" — short forms. time's
// default String() ("1h2m13.4s") is too noisy for a status line.
func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh%dm", h, m)
}
