package monitors

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HistoryEntry is one rule firing's record. Append-only to
// monitors/.history/<name>.jsonl so users can see what happened
// after the fact (which item matched, which actions ran, what
// dispatch returned).
type HistoryEntry struct {
	Ts      time.Time       `json:"ts"`
	Rule    string          `json:"rule"`
	Item    map[string]any  `json:"item"`
	Actions []HistoryAction `json:"actions"`
}

type HistoryAction struct {
	Type   string `json:"type"`
	Result any    `json:"result,omitempty"`
	Err    string `json:"err,omitempty"`
}

// AppendHistory adds one entry as a single line of JSON. Errors
// from create/write fail the call but don't roll back: the worker
// continues processing other items even if history writes fail.
func AppendHistory(path string, entry HistoryEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	return enc.Encode(entry)
}

// TailHistory reads the last n entries from the .jsonl file. Out-of-
// order or malformed lines are skipped silently — the caller gets
// well-formed entries only. Missing file → nil slice, no error.
func TailHistory(path string, n int) ([]HistoryEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]HistoryEntry, 0, len(lines))
	for _, raw := range lines {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var e HistoryEntry
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}
