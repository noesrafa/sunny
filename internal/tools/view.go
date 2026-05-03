package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// viewTool reads a file's contents with optional line offset/limit
// and renders the output with line numbers — the format crush uses,
// matching what coding agents are conditioned to expect.
type viewTool struct{}

const (
	viewMaxLines    = 2000
	viewMaxBytes    = 4 << 20 // 4 MiB hard cap; anything bigger needs the user to read manually
	viewLineNumPad  = 6
)

func (viewTool) Name() string { return "view" }

func (viewTool) Description() string {
	return "Read a file's contents with line numbers. Use offset and limit for large files. Paths must stay inside the session's working directory."
}

func (viewTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":   {"type": "string", "description": "Path to the file (absolute, or relative to the session cwd)."},
    "offset": {"type": "integer", "minimum": 0, "description": "0-based line offset to start reading from."},
    "limit":  {"type": "integer", "minimum": 1, "description": "Maximum number of lines to return (default 2000, hard cap 2000)."}
  },
  "required": ["path"]
}`)
}

type viewParams struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

func (viewTool) Run(ctx context.Context, raw json.RawMessage, cwd string) (string, error) {
	var p viewParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("view: bad params: %w", err)
	}
	if p.Path == "" {
		return "", fmt.Errorf("view: path is required")
	}
	abs, err := resolveInside(cwd, p.Path)
	if err != nil {
		return "", fmt.Errorf("view: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("view: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("view: %s is a directory (use ls instead)", p.Path)
	}
	if info.Size() > viewMaxBytes {
		return "", fmt.Errorf("view: %s is %d bytes — over the %d cap; use offset/limit or open it manually", p.Path, info.Size(), viewMaxBytes)
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("view: %w", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")

	limit := p.Limit
	if limit <= 0 || limit > viewMaxLines {
		limit = viewMaxLines
	}
	start := p.Offset
	if start > len(lines) {
		start = len(lines)
	}
	end := start + limit
	if end > len(lines) {
		end = len(lines)
	}

	var b strings.Builder
	for i := start; i < end; i++ {
		// 1-based numbering matches editors and grep output.
		fmt.Fprintf(&b, "%*d  %s\n", viewLineNumPad, i+1, lines[i])
	}
	if end < len(lines) {
		fmt.Fprintf(&b, "\n[truncated — %d more lines; pass offset=%d to continue]\n", len(lines)-end, end)
	}
	return b.String(), nil
}
