package opencode

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/noesrafa/sunny/internal/provider"
)

// agentDirEnv lets tests / advanced users redirect the opencode agent
// directory. Default is ~/.config/opencode/agent (or
// $XDG_CONFIG_HOME/opencode/agent if set), matching what opencode
// itself uses.
const agentDirEnv = "SUNNY_OPENCODE_AGENT_DIR"

// agentNamePrefix scopes sunny-managed agent files so they don't
// collide with anything the user authored by hand via `opencode agent
// create`.
const agentNamePrefix = "sunny-"

// syncAgentFile writes a markdown agent file at
// ~/.config/opencode/agent/sunny-<slug>.md whose body is the engine's
// flattened system prompt. opencode then loads it via `--agent
// sunny-<slug>` on the next `opencode run`.
//
// The file is rewritten only when the prompt content changes, so
// repeated turns against an unchanged agent are cheap (one stat).
//
// Returns the agent name to pass on the command line.
func syncAgentFile(slug string, sys []provider.SystemBlock) (string, error) {
	if slug == "" {
		return "", fmt.Errorf("opencode: agent slug required")
	}
	dir, err := agentDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("opencode: mkdir agent dir: %w", err)
	}

	body := flattenSystem(sys)
	content := buildAgentMarkdown(slug, body)
	name := agentNamePrefix + slug
	path := filepath.Join(dir, name+".md")

	// Skip the write when the file matches what we'd produce. Avoids
	// gratuitous mtime churn when the user is mid-conversation.
	if existing, err := os.ReadFile(path); err == nil && fingerprint(existing) == fingerprint([]byte(content)) {
		return name, nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("opencode: write agent file: %w", err)
	}
	return name, nil
}

func agentDir() (string, error) {
	if v := strings.TrimSpace(os.Getenv(agentDirEnv)); v != "" {
		return v, nil
	}
	if v := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); v != "" {
		return filepath.Join(v, "opencode", "agent"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("opencode: home dir: %w", err)
	}
	return filepath.Join(home, ".config", "opencode", "agent"), nil
}

// buildAgentMarkdown serializes a sunny agent into opencode's
// frontmatter+body format. mode=primary makes it directly invokable
// from `--agent`; we don't set permissions because sunny passes
// --dangerously-skip-permissions on every spawn anyway.
func buildAgentMarkdown(slug, body string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "description: sunny agent %s (managed by sunny — do not edit by hand)\n", slug)
	b.WriteString("mode: primary\n")
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(body))
	b.WriteString("\n")
	return b.String()
}

// flattenSystem joins the engine's SystemBlocks into one string. Cache-
// breakpoint hints are dropped — opencode delegates caching to the
// underlying provider, so a flat string is the right shape.
func flattenSystem(blocks []provider.SystemBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	var b strings.Builder
	for i, blk := range blocks {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(strings.TrimSpace(blk.Text))
	}
	return b.String()
}

func fingerprint(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
