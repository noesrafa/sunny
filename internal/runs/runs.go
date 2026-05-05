// Package runs supervises long-lived background processes (dev
// servers, watchers, etc.) per daemon. Definitions live in
// runs.json under the daemon root; runtime state (pid, status,
// exit code) is in-memory and dies with the daemon process.
//
// Design parallels internal/tabs: file-backed Store + atomic-rename
// persistence + single mutex. The supervisor (Runtime) is its own
// concern — it owns the live processes and their log writers, but
// reads definitions through the Store.
package runs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrNotFound is returned when an id does not exist in the store.
var ErrNotFound = errors.New("run not found")

// Run is the persisted definition of a background service. Runtime
// state (pid, status, exit code) is NOT stored here — that lives in
// the Runtime supervisor and dies with the daemon.
type Run struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Cwd       string    `json:"cwd"`
	Command   string    `json:"command"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Patch is the body of an Update — nil pointers leave fields
// untouched. Same shape as the HTTP PATCH body.
type Patch struct {
	Name    *string
	Cwd     *string
	Command *string
}

// Store is the file-backed run registry. Construct once at boot via
// Load(root); pass the same instance to the HTTP layer and to the
// Runtime supervisor.
type Store struct {
	path string

	mu   sync.Mutex
	runs []*Run
}

const (
	currentVersion = 1
	fileName       = "runs.json"
)

type fileShape struct {
	Version int    `json:"version"`
	Runs    []*Run `json:"runs"`
}

// Load reads runs.json from root. Missing file or unknown version →
// empty store, no error. A corrupt file is tolerated by starting
// fresh; the next save overwrites it.
func Load(root string) (*Store, error) {
	s := &Store{path: filepath.Join(root, fileName)}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read runs.json: %w", err)
	}
	var f fileShape
	if err := json.Unmarshal(raw, &f); err != nil {
		return s, nil
	}
	if f.Version != currentVersion {
		return s, nil
	}
	s.runs = f.Runs
	return s, nil
}

// List returns a snapshot of all runs in created_at order. The
// caller must not mutate the returned slice.
func (s *Store) List() []*Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Run, len(s.runs))
	copy(out, s.runs)
	return out
}

// Get returns a copy of the run with the given id, or ErrNotFound.
func (s *Store) Get(id string) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runs {
		if r.ID == id {
			cp := *r
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

// Create scaffolds a new run definition and persists. Returns the
// stored copy (id + timestamps assigned).
func (s *Store) Create(name, cwd, command string) (*Run, error) {
	if err := validate(name, cwd, command); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	stored := &Run{
		ID:        id,
		Name:      name,
		Cwd:       cwd,
		Command:   command,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.runs = append(s.runs, stored)
	if err := s.saveLocked(); err != nil {
		s.runs = s.runs[:len(s.runs)-1]
		return nil, err
	}
	cp := *stored
	return &cp, nil
}

// Update applies p to the run with the given id and persists. Nil
// fields in p leave the corresponding value untouched. Returns the
// post-update copy.
func (s *Store) Update(id string, p Patch) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runs {
		if r.ID != id {
			continue
		}
		next := *r
		if p.Name != nil {
			next.Name = *p.Name
		}
		if p.Cwd != nil {
			next.Cwd = *p.Cwd
		}
		if p.Command != nil {
			next.Command = *p.Command
		}
		if err := validate(next.Name, next.Cwd, next.Command); err != nil {
			return nil, err
		}
		next.UpdatedAt = time.Now().UTC()
		backup := *r
		*r = next
		if err := s.saveLocked(); err != nil {
			*r = backup
			return nil, err
		}
		cp := next
		return &cp, nil
	}
	return nil, ErrNotFound
}

// Remove drops the run with the given id. Idempotent for missing
// ids — returns ErrNotFound but the store stays consistent.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.runs {
		if r.ID == id {
			s.runs = append(s.runs[:i], s.runs[i+1:]...)
			return s.saveLocked()
		}
	}
	return ErrNotFound
}

// saveLocked atomically writes the current state to disk. Caller
// must hold s.mu.
func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	sort.Slice(s.runs, func(i, j int) bool { return s.runs[i].CreatedAt.Before(s.runs[j].CreatedAt) })
	data, err := json.MarshalIndent(fileShape{Version: currentVersion, Runs: s.runs}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func validate(name, cwd, command string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("name: required")
	}
	if strings.TrimSpace(cwd) == "" {
		return fmt.Errorf("cwd: required")
	}
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("command: required")
	}
	return nil
}

// newID returns a sortable, collision-resistant run id of the shape
// run_<unix_ms>_<8hex>. Same shape as conv/tab IDs.
func newID() (string, error) {
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return fmt.Sprintf("run_%013d_%s", time.Now().UnixMilli(), hex.EncodeToString(rnd[:])), nil
}
