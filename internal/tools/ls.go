package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// lsTool walks a directory tree under cwd, returning a tree-shaped
// rendering similar to `tree`. Hidden entries (`.git`, `.DS_Store`,
// dotfiles) are skipped by default; vendored noise (`node_modules`,
// `vendor`, etc.) too. Bounded depth keeps output usable for the
// model.
type lsTool struct{}

const (
	lsDefaultDepth = 3
	lsMaxDepth     = 10
	lsMaxEntries   = 500 // hard cap to avoid swamping the model with a big repo
)

// lsSkip is the default ignore list. Crush uses a richer ignore
// engine; for v1 of this tool we hardcode the obvious noise. A
// future PR can wire .gitignore.
var lsSkip = map[string]bool{
	".git":         true,
	".svn":         true,
	".hg":          true,
	"node_modules": true,
	"vendor":       true,
	".venv":        true,
	"__pycache__":  true,
	".DS_Store":    true,
	"dist":         true,
	"build":        true,
	".next":        true,
	".cache":       true,
}

func (lsTool) Name() string { return "ls" }

func (lsTool) Description() string {
	return "List a directory tree. Skips hidden entries and common vendored dirs (node_modules, .git, etc.). Bounded depth and total entries; pass deeper paths or higher depth for more."
}

func (lsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":  {"type": "string", "description": "Directory to list (default: cwd). Must stay inside the session cwd."},
    "depth": {"type": "integer", "minimum": 1, "maximum": 10, "description": "Tree depth (default 3, max 10)."}
  }
}`)
}

type lsParams struct {
	Path  string `json:"path,omitempty"`
	Depth int    `json:"depth,omitempty"`
}

func (lsTool) Run(ctx context.Context, raw json.RawMessage, cwd string) (string, error) {
	var p lsParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return "", fmt.Errorf("ls: bad params: %w", err)
		}
	}
	if p.Path == "" {
		p.Path = "."
	}
	depth := p.Depth
	if depth <= 0 {
		depth = lsDefaultDepth
	}
	if depth > lsMaxDepth {
		depth = lsMaxDepth
	}

	root, err := resolveInside(cwd, p.Path)
	if err != nil {
		return "", fmt.Errorf("ls: %w", err)
	}
	info, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(p.Path + "\n")

	var count int
	var truncated bool
	err = filepath.WalkDir(info, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // best-effort: skip unreadable dirs, don't blow the whole walk
		}
		if path == info {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || lsSkip[name] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(info, path)
		if err != nil {
			return nil
		}
		level := strings.Count(rel, string(filepath.Separator))
		if level >= depth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if count >= lsMaxEntries {
			truncated = true
			return filepath.SkipAll
		}
		count++
		indent := strings.Repeat("  ", level)
		marker := ""
		if d.IsDir() {
			marker = "/"
		}
		fmt.Fprintf(&b, "%s%s%s\n", indent, name, marker)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("ls: walk: %w", err)
	}
	if truncated {
		fmt.Fprintf(&b, "\n[truncated — over %d entries; narrow the path or lower depth]\n", lsMaxEntries)
	}
	return b.String(), nil
}
