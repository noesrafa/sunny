package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/noesrafa/sunny/internal/secrets"
)

// EnsureSecretsTemplate writes a commented stub at root/secrets.yaml
// so:
//
//   - The file exists with mode 0600 from day one (right perms even
//     before the user touches it).
//   - An AI agent running inside sunny can `view` the file, see the
//     shape, and edit it directly the same way it edits knowledge
//     files. The stub doubles as the spec.
//   - A curious user who `cat`s ~/.sunny/secrets.yaml gets a self-
//     describing template instead of "no such file".
//
// Idempotent: only writes when the file is absent, so an existing
// secrets.yaml with real keys is never touched. Returns true if a
// stub was written, false if the file already existed.
func EnsureSecretsTemplate(root string) (bool, error) {
	path := secrets.Path(root)
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	body := buildSecretsTemplate(secrets.Catalog())
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return false, fmt.Errorf("write secrets template: %w", err)
	}
	return true, nil
}

// buildSecretsTemplate assembles a commented YAML scaffold from the
// catalog. Every provider+field is shown as a commented sample line so
// the user (or an AI looking at the file) immediately sees what's
// supported. Uncommenting + filling a value is all it takes to enable.
func buildSecretsTemplate(entries []secrets.CatalogEntry) string {
	header := `# sunny secrets — API keys and per-provider config.
#
# Mode 0600 (your user only). The daemon re-reads this file on every
# turn, so rotating an existing key takes effect immediately. Adding a
# brand-new provider section requires sunny restart so the engine
# picks up the new driver.
#
# Edit this file directly — that's the supported flow. The HTTP API
# (PUT /secrets/{provider}) works too and triggers the engine reload
# automatically; pick whichever fits your workflow.
#
# Environment variables override file values when both are set
# (ANTHROPIC_API_KEY, OPENAI_API_KEY, OLLAMA_API_KEY, …) — useful for
# CI / docker without writing keys to disk.
#
# Uncomment the sections below and fill in your keys.

`
	body := header
	for _, e := range entries {
		body += fmt.Sprintf("# %s\n", e.Label)
		if e.HelpURL != "" {
			body += fmt.Sprintf("#   get a key: %s\n", e.HelpURL)
		}
		body += fmt.Sprintf("# %s:\n", e.Provider)
		for _, f := range e.Fields {
			suffix := ""
			if f.Hint != "" {
				suffix = "   # " + f.Hint
			}
			body += fmt.Sprintf("#   %s: \"\"%s\n", f.Key, suffix)
		}
		body += "\n"
	}
	return body
}
