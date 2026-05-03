package runs

import "sync"

// DefaultLogBufferLines is the per-run scrollback ceiling. Long-running
// dev servers (next dev, vitest --watch, tailwind --watch) trash 500
// quickly, and the user wants meaningful history when something blew
// up minutes ago.
const DefaultLogBufferLines = 5000

// LogBuffer is a ring buffer of recent log lines for a single Run. Safe for
// concurrent appends from the stdout/stderr capture goroutines while the UI
// reads via Snapshot.
type LogBuffer struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func NewLogBuffer(max int) *LogBuffer {
	if max <= 0 {
		max = DefaultLogBufferLines
	}
	return &LogBuffer{max: max, lines: make([]string, 0, max)}
}

func (b *LogBuffer) Append(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, line)
	if len(b.lines) > b.max {
		drop := len(b.lines) - b.max
		b.lines = b.lines[drop:]
	}
}

// Snapshot returns a copy of the current lines, safe for the caller to use
// without holding the lock.
func (b *LogBuffer) Snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}

func (b *LogBuffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = b.lines[:0]
}

func (b *LogBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.lines)
}
