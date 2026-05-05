package monitors

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// State is per-monitor persistent state, kept under
// monitors/.state/<name>.json. Cursor is opaque source-specific
// (last-seen rowid, ISO timestamp, …); Seen is the bounded dedup
// set keyed by Item.ID; Vars is free-form for sources/actions that
// want to remember things across ticks.
type State struct {
	Cursor   any            `json:"cursor,omitempty"`
	Seen     []string       `json:"seen,omitempty"`
	Vars     map[string]any `json:"vars,omitempty"`
	LastFire time.Time      `json:"last_fire,omitempty"`
}

// maxSeenIDs caps the dedup ring. 1000 is plenty for chat-style
// sources at human pace; bursts of more than 1000 unique items in
// one tick is the kind of fire-hose situation a monitor wouldn't
// handle well anyway.
const maxSeenIDs = 1000

// MarkSeen appends id to the dedup ring, trimming oldest if over
// the cap.
func (s *State) MarkSeen(id string) {
	s.Seen = append(s.Seen, id)
	if len(s.Seen) > maxSeenIDs {
		s.Seen = s.Seen[len(s.Seen)-maxSeenIDs:]
	}
}

// IsSeen reports whether id was already processed in a previous tick.
// O(n) — fine at maxSeenIDs.
func (s *State) IsSeen(id string) bool {
	for _, x := range s.Seen {
		if x == id {
			return true
		}
	}
	return false
}

// LoadState reads path and decodes a State. Missing file → empty
// State (not an error) so a fresh monitor doesn't need bootstrap.
func LoadState(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &State{Vars: map[string]any{}}, nil
		}
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return &State{Vars: map[string]any{}}, nil
	}
	if s.Vars == nil {
		s.Vars = map[string]any{}
	}
	return &s, nil
}

// SaveState atomically writes the state. Uses tmp+rename so a crash
// mid-write doesn't leave a half-truncated json that fails to
// decode on next boot.
func SaveState(path string, s *State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
