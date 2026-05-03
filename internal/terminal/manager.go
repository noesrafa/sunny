package terminal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// SavedPane is the persisted form of a Pane (no runtime state).
type SavedPane struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Cwd     string `json:"cwd,omitempty"`
}

// Manager owns the active list of panes and which one is focused.
type Manager struct {
	mu     sync.Mutex
	Panes  []*Pane
	Active int
}

func NewManager() *Manager { return &Manager{} }

func (m *Manager) Add(p *Pane) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Panes = append(m.Panes, p)
	m.Active = len(m.Panes) - 1
}

func (m *Manager) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Panes)
}

func (m *Manager) Current() *Pane {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.Panes) == 0 {
		return nil
	}
	if m.Active < 0 || m.Active >= len(m.Panes) {
		m.Active = 0
	}
	return m.Panes[m.Active]
}

func (m *Manager) ByID(id string) *Pane {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.Panes {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (m *Manager) Index(i int) *Pane {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i < 0 || i >= len(m.Panes) {
		return nil
	}
	return m.Panes[i]
}

// SetActive selects the pane at index i (clamped).
func (m *Manager) SetActive(i int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.Panes) == 0 {
		m.Active = 0
		return
	}
	if i < 0 {
		i = 0
	}
	if i >= len(m.Panes) {
		i = len(m.Panes) - 1
	}
	m.Active = i
}

// Close removes the pane and kills the child.
func (m *Manager) Close(id string) {
	m.mu.Lock()
	idx := -1
	for i, p := range m.Panes {
		if p.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		m.mu.Unlock()
		return
	}
	target := m.Panes[idx]
	m.Panes = append(m.Panes[:idx], m.Panes[idx+1:]...)
	if m.Active >= len(m.Panes) {
		m.Active = len(m.Panes) - 1
	}
	if m.Active < 0 {
		m.Active = 0
	}
	m.mu.Unlock()
	_ = target.Close()
}

// CloseAll kills every pane (used at shutdown).
func (m *Manager) CloseAll() {
	m.mu.Lock()
	all := append([]*Pane(nil), m.Panes...)
	m.Panes = nil
	m.Active = 0
	m.mu.Unlock()
	for _, p := range all {
		_ = p.Close()
	}
}

func panesPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".sunnytui", "panes.json")
}

// LoadSaved returns the persisted (Name, Command, Cwd) tuples. Caller
// re-spawns them via Spawn — manager doesn't auto-revive because spawn
// requires terminal dims.
func LoadSaved() ([]SavedPane, error) {
	p := panesPath()
	if p == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var saved []SavedPane
	if err := json.Unmarshal(raw, &saved); err != nil {
		return nil, err
	}
	return saved, nil
}

// Save writes the current pane registrations to ~/.sunnytui/panes.json.
func (m *Manager) Save() error {
	p := panesPath()
	if p == "" {
		return nil
	}
	m.mu.Lock()
	saved := make([]SavedPane, 0, len(m.Panes))
	for _, pn := range m.Panes {
		saved = append(saved, SavedPane{
			Name:    pn.Title,
			Command: pn.Command,
			Cwd:     pn.Cwd,
		})
	}
	m.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
