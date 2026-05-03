package runs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// Manager owns the registered Runs, persistence, and lookup.
type Manager struct {
	mu   sync.Mutex
	runs []*Run
	path string
}

var idSeq atomic.Int64

func nextID() string {
	return fmt.Sprintf("r%d", idSeq.Add(1))
}

func defaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".sunnytui", "runs.json")
}

// Load reads the persisted runs.json. Missing file returns an empty manager;
// invalid JSON returns an error.
func Load() (*Manager, error) {
	p := defaultPath()
	m := &Manager{path: p}
	if p == "" {
		return m, nil
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, err
	}
	var loaded []*Run
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return nil, fmt.Errorf("decode runs.json: %w", err)
	}
	// Reassign IDs on load so the runtime always has unique, consecutive
	// IDs regardless of what the persisted file looked like — earlier
	// sunnytui versions reset idSeq across processes, which silently
	// minted duplicate "r1"s and made every run resolve to the first one
	// (e.g. opening logs always tailed run #1's buffer).
	for i, r := range loaded {
		r.ID = fmt.Sprintf("r%d", i+1)
		r.Status = StatusStopped
		m.runs = append(m.runs, r)
	}
	// Advance idSeq past the highest assigned ID so later Adds don't
	// collide with anything we just loaded.
	target := int64(len(m.runs))
	for idSeq.Load() < target {
		idSeq.Add(1)
	}
	return m, nil
}

// Save writes the runs.json (only persisted fields — runtime state is dropped).
func (m *Manager) Save() error {
	if m.path == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m.runs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.path, data, 0o644)
}

// All returns the current run slice (live pointers).
func (m *Manager) All() []*Run {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Run, len(m.runs))
	copy(out, m.runs)
	return out
}

func (m *Manager) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.runs)
}

func (m *Manager) Get(id string) *Run {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.runs {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func (m *Manager) Index(i int) *Run {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i < 0 || i >= len(m.runs) {
		return nil
	}
	return m.runs[i]
}

// Add registers a new run. Returns the new Run.
func (m *Manager) Add(name, command, cwd string) *Run {
	r := &Run{
		ID:      nextID(),
		Name:    name,
		Command: command,
		Cwd:     cwd,
		Status:  StatusStopped,
	}
	m.mu.Lock()
	m.runs = append(m.runs, r)
	m.mu.Unlock()
	return r
}

// Remove stops the run if running and removes it from the list.
func (m *Manager) Remove(id string) {
	m.mu.Lock()
	idx := -1
	for i, r := range m.runs {
		if r.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		m.mu.Unlock()
		return
	}
	target := m.runs[idx]
	m.runs = append(m.runs[:idx], m.runs[idx+1:]...)
	m.mu.Unlock()
	_ = target.Stop()
}

// StopAll stops every running entry. Used at shutdown.
func (m *Manager) StopAll() {
	m.mu.Lock()
	all := append([]*Run(nil), m.runs...)
	m.mu.Unlock()
	for _, r := range all {
		if r.Running() {
			_ = r.Stop()
		}
	}
}
