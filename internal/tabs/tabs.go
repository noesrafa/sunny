// Package tabs persists the daemon-side list of "open chat tabs".
//
// Why server-side: the canonical state of "what conversations are
// open right now" lives on the daemon, not on each TUI. Multiple
// TUIs connected to the same daemon — local + tailnet — see the
// same tabs in real time. Open a tab in one client, it appears in
// every other one within a second (via the events.Hub bus). Close a
// tab in one client, it disappears in the others. This is the
// Discord/Slack model: server-authoritative state, clients are
// stateless views.
//
// What stays per-TUI: drafts (the unsent textarea text), the
// currently focused tab index, theme. Those genuinely differ per
// device and don't make sense to sync.
//
// On-disk shape (~/.sunny/tabs.json):
//
//	{
//	  "version": 1,
//	  "tabs": [
//	    {"id": "tab_…", "agent_slug": "sunny", "conv_id": "conv_…", …}
//	  ]
//	}
//
// Single mutex protects the whole list — opens/closes are rare
// (human pace) so contention is non-existent.
package tabs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrNotFound is returned when a tab id doesn't exist in the store.
var ErrNotFound = errors.New("tab not found")

// Tab is one open chat tab in the daemon.
type Tab struct {
	ID        string    `json:"id"`
	AgentSlug string    `json:"agent_slug"`
	ConvID    string    `json:"conv_id"`
	Title     string    `json:"title,omitempty"`
	Cwd       string    `json:"cwd,omitempty"`
	OpenedAt  time.Time `json:"opened_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Store is the file-backed tab registry. Construct once at daemon
// boot via Load(root); pass the same instance to the HTTP layer.
type Store struct {
	path string

	mu   sync.Mutex
	tabs []*Tab
}

const (
	currentVersion = 1
	fileName       = "tabs.json"
)

type fileShape struct {
	Version int    `json:"version"`
	Tabs    []*Tab `json:"tabs"`
}

// Load reads ~/.sunny/tabs.json (or the equivalent under root).
// Missing file or unknown version → empty store, no error. Returns
// the populated Store ready to serve reads + writes.
func Load(root string) (*Store, error) {
	s := &Store{path: filepath.Join(root, fileName)}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read tabs.json: %w", err)
	}
	var f fileShape
	if err := json.Unmarshal(raw, &f); err != nil {
		// Corrupt file — start fresh rather than fail boot. The
		// next save overwrites the broken bytes.
		return s, nil
	}
	if f.Version != currentVersion {
		return s, nil
	}
	s.tabs = f.Tabs
	return s, nil
}

// List returns a snapshot of all tabs in opened_at order (the order
// they were added). Caller may not mutate the returned slice — make
// a copy if you need to.
func (s *Store) List() []*Tab {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Tab, len(s.tabs))
	copy(out, s.tabs)
	return out
}

// Get returns the tab with the given id, or ErrNotFound.
func (s *Store) Get(id string) (*Tab, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tabs {
		if t.ID == id {
			cp := *t
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

// Add appends a new tab and persists. Caller is responsible for
// filling AgentSlug, ConvID, Cwd, Title; ID + timestamps are
// assigned here. Returns the stored copy (with id + timestamps set).
func (s *Store) Add(t *Tab) (*Tab, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	stored := &Tab{
		ID:        id,
		AgentSlug: t.AgentSlug,
		ConvID:    t.ConvID,
		Title:     t.Title,
		Cwd:       t.Cwd,
		OpenedAt:  now,
		UpdatedAt: now,
	}
	s.tabs = append(s.tabs, stored)
	if err := s.saveLocked(); err != nil {
		// Roll back the append on save failure so memory and disk
		// agree.
		s.tabs = s.tabs[:len(s.tabs)-1]
		return nil, err
	}
	cp := *stored
	return &cp, nil
}

// Remove drops the tab with the given id. Idempotent — a missing id
// returns ErrNotFound but the store stays consistent either way.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.tabs {
		if t.ID == id {
			s.tabs = append(s.tabs[:i], s.tabs[i+1:]...)
			return s.saveLocked()
		}
	}
	return ErrNotFound
}

// Update applies fn to the tab in place, refreshes UpdatedAt, and
// persists. fn may modify any field except ID / OpenedAt — those are
// invariants. Returns the updated copy.
func (s *Store) Update(id string, fn func(*Tab)) (*Tab, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tabs {
		if t.ID == id {
			fn(t)
			t.UpdatedAt = time.Now().UTC()
			if err := s.saveLocked(); err != nil {
				return nil, err
			}
			cp := *t
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

// saveLocked atomically writes the current state to disk. Caller
// must hold s.mu.
func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(fileShape{Version: currentVersion, Tabs: s.tabs}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// newID returns a sortable, collision-resistant tab id of the shape
// tab_<unix_ms>_<8hex>. Same shape as conversation IDs so the two
// look related.
func newID() (string, error) {
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return fmt.Sprintf("tab_%013d_%s", time.Now().UnixMilli(), hex.EncodeToString(rnd[:])), nil
}
