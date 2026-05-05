package monitors

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
)

// ShellSource runs an arbitrary shell command and parses its stdout
// as a JSON array of objects. Each object becomes one Item; its
// `id` field (when present) drives deduplication, otherwise we hash
// the object so identical payloads collapse.
//
// This is the v1 "escape hatch" source — anything an agent can
// scrape with `curl`, `gh api`, `osascript`, `ssh ... sqlite3 …`
// becomes a monitor by writing a one-liner that prints JSON.
type ShellSource struct{}

func (ShellSource) Type() string { return "shell" }

func (ShellSource) Fetch(ctx context.Context, cfg map[string]any, state map[string]any) ([]Item, map[string]any, error) {
	cmd, _ := cfg["command"].(string)
	if cmd == "" {
		return nil, state, fmt.Errorf("shell source: `command` field required")
	}

	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	out, err := c.Output()
	if err != nil {
		return nil, state, fmt.Errorf("shell run: %w", err)
	}
	if len(out) == 0 {
		return nil, state, nil
	}

	var raw []map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, state, fmt.Errorf("shell output must be a JSON array of objects: %w", err)
	}
	items := make([]Item, 0, len(raw))
	for _, m := range raw {
		id, _ := m["id"].(string)
		if id == "" {
			id = hashItem(m)
		}
		items = append(items, Item{ID: id, Fields: m})
	}
	return items, state, nil
}

// hashItem returns a stable short hex digest of the item's JSON
// encoding. Used as a fallback dedup key when the source doesn't
// provide an explicit id.
func hashItem(m map[string]any) string {
	j, _ := json.Marshal(m)
	h := sha256.Sum256(j)
	return hex.EncodeToString(h[:8])
}
