package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
)

// globTool finds files by glob pattern. Supports `**` for recursive
// wildcards (filepath.Match doesn't, hence the small custom matcher
// below — kept inline so we don't take a doublestar dep for ~30
// lines of logic).
type globTool struct{}

const globMaxResults = 200

func (globTool) Name() string { return "glob" }

func (globTool) Description() string {
	return "Find files matching a glob pattern. Supports `**` for recursive matching (e.g. `**/*.go`, `internal/**/*_test.go`). Skips common vendored dirs."
}

func (globTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Glob pattern. Use ** to recurse."},
    "path":    {"type": "string", "description": "Directory to start from (default: cwd). Must stay inside the session cwd."}
  },
  "required": ["pattern"]
}`)
}

type globParams struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

func (globTool) Run(ctx context.Context, raw json.RawMessage, cwd string) (string, error) {
	var p globParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("glob: bad params: %w", err)
	}
	if p.Pattern == "" {
		return "", fmt.Errorf("glob: pattern is required")
	}
	startRel := p.Path
	if startRel == "" {
		startRel = "."
	}
	root, err := resolveInside(cwd, startRel)
	if err != nil {
		return "", fmt.Errorf("glob: %w", err)
	}

	pattern := filepath.ToSlash(p.Pattern)
	var matches []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if p == root {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || lsSkip[name] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if matchDoublestar(pattern, rel) {
			matches = append(matches, rel)
			if len(matches) >= globMaxResults {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("glob: walk: %w", err)
	}
	if len(matches) == 0 {
		return "(no matches)\n", nil
	}
	var b strings.Builder
	for _, m := range matches {
		b.WriteString(m)
		b.WriteByte('\n')
	}
	if len(matches) >= globMaxResults {
		fmt.Fprintf(&b, "\n[truncated — over %d matches; narrow the pattern]\n", globMaxResults)
	}
	return b.String(), nil
}

// matchDoublestar implements `**` aware glob matching for forward-
// slash paths. Algorithm: split both pattern and path by `/`, then
// recurse. `**` consumes zero or more path segments, anything else
// matches one segment via path.Match. Doublestar libraries are 10×
// the size; for sunny's needs this is plenty.
func matchDoublestar(pattern, target string) bool {
	pp := strings.Split(pattern, "/")
	tt := strings.Split(target, "/")
	return matchSegments(pp, tt)
}

func matchSegments(pp, tt []string) bool {
	for len(pp) > 0 && len(tt) > 0 {
		if pp[0] == "**" {
			// Recursive case: try matching remainder against
			// every suffix of tt (including empty).
			rest := pp[1:]
			if len(rest) == 0 {
				return true // trailing ** swallows everything
			}
			for i := 0; i <= len(tt); i++ {
				if matchSegments(rest, tt[i:]) {
					return true
				}
			}
			return false
		}
		ok, err := path.Match(pp[0], tt[0])
		if err != nil || !ok {
			return false
		}
		pp = pp[1:]
		tt = tt[1:]
	}
	// Trailing `**` after consuming all path segments still matches.
	for _, s := range pp {
		if s != "**" {
			return false
		}
	}
	return len(tt) == 0
}
