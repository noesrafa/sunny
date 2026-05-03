package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// grepTool searches file contents by regex. Prefers `rg` if it's on
// PATH (multi-thread, gitignore-aware, fast); falls back to a pure-Go
// walker over `regexp` line-by-line for the no-rg case.
type grepTool struct{}

const grepMaxMatches = 200

func (grepTool) Name() string { return "grep" }

func (grepTool) Description() string {
	return "Search file contents by regular expression. Returns path:line:match for each hit. Uses ripgrep when available, falls back to a Go-native walker. Skips hidden + vendored dirs."
}

func (grepTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Regular expression to search for (re2 / Go syntax)."},
    "path":    {"type": "string", "description": "Directory to search under (default: cwd). Must stay inside the session cwd."},
    "include": {"type": "string", "description": "Optional glob filter on filenames (e.g. *.go). Applied to basenames."}
  },
  "required": ["pattern"]
}`)
}

type grepParams struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
	Include string `json:"include,omitempty"`
}

func (grepTool) Run(ctx context.Context, raw json.RawMessage, cwd string) (string, error) {
	var p grepParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("grep: bad params: %w", err)
	}
	if p.Pattern == "" {
		return "", fmt.Errorf("grep: pattern is required")
	}
	startRel := p.Path
	if startRel == "" {
		startRel = "."
	}
	root, err := resolveInside(cwd, startRel)
	if err != nil {
		return "", fmt.Errorf("grep: %w", err)
	}

	if _, err := exec.LookPath("rg"); err == nil {
		return grepRipgrep(ctx, p, root)
	}
	return grepGo(ctx, p, root)
}

// grepRipgrep shells out to ripgrep, which honours .gitignore + is
// orders of magnitude faster than the Go walker on any non-trivial
// repo. Output is line-buffered, capped at grepMaxMatches.
func grepRipgrep(ctx context.Context, p grepParams, root string) (string, error) {
	args := []string{"--line-number", "--no-heading", "--color=never", "--max-count", "50"}
	if p.Include != "" {
		args = append(args, "--glob", p.Include)
	}
	args = append(args, p.Pattern, ".")
	cmd := exec.CommandContext(ctx, "rg", args...)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		// rg exits 1 when there are no matches; that's not an error
		// for us, just an empty result.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "(no matches)\n", nil
		}
		return "", fmt.Errorf("grep (rg): %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return capMatches(stdout.String()), nil
}

// grepGo is the no-ripgrep fallback. Walks the tree honouring
// lsSkip + dot-prefix filter, runs the regex line-by-line. Bails
// early on the match cap.
func grepGo(ctx context.Context, p grepParams, root string) (string, error) {
	re, err := regexp.Compile(p.Pattern)
	if err != nil {
		return "", fmt.Errorf("grep: %w", err)
	}

	var b strings.Builder
	var matches int
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
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
		if p.Include != "" {
			ok, _ := filepath.Match(p.Include, name)
			if !ok {
				return nil
			}
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 1<<16), 1<<20)
		lineno := 0
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for scanner.Scan() {
			lineno++
			line := scanner.Text()
			if re.MatchString(line) {
				fmt.Fprintf(&b, "%s:%d:%s\n", rel, lineno, line)
				matches++
				if matches >= grepMaxMatches {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, context.Canceled) {
		return "", fmt.Errorf("grep: walk: %w", walkErr)
	}
	if matches == 0 {
		return "(no matches)\n", nil
	}
	out := b.String()
	if matches >= grepMaxMatches {
		out += fmt.Sprintf("\n[truncated — over %d matches; narrow the pattern]\n", grepMaxMatches)
	}
	return out, nil
}

// capMatches truncates ripgrep output to grepMaxMatches lines so the
// model doesn't choke on huge result sets.
func capMatches(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) <= grepMaxMatches {
		return out
	}
	kept := strings.Join(lines[:grepMaxMatches], "\n")
	return kept + fmt.Sprintf("\n\n[truncated — %d more matches; narrow the pattern]\n", len(lines)-grepMaxMatches)
}
