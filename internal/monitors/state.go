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
	// Firings is the timestamp ring of recent rule firings. Used to
	// enforce per-monitor rate limits (RateLimit.PerMinute /
	// PerHour). Capped to maxFiringHistory entries; older entries
	// roll off as new ones arrive.
	Firings []time.Time `json:"firings,omitempty"`
}

// maxFiringHistory caps the Firings ring. At default 1/min and 10/hr
// limits, we'd never legitimately accumulate more than 10. Cap at 200
// to give plenty of headroom when users bump the per-hour cap.
const maxFiringHistory = 200

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

// CountFiringsSince returns how many rule firings happened in the
// last `window` duration. Caller uses this to decide whether the
// monitor's RateLimit allows another firing right now.
func (s *State) CountFiringsSince(window time.Duration) int {
	cutoff := time.Now().Add(-window)
	n := 0
	for _, t := range s.Firings {
		if t.After(cutoff) {
			n++
		}
	}
	return n
}

// MarkFiring records a rule firing at "now" and trims the ring if
// it grew past maxFiringHistory. Call this AFTER deciding to fire
// (i.e. after the rate-limit check passed), not before — otherwise
// the act of checking would itself consume budget.
func (s *State) MarkFiring() {
	s.Firings = append(s.Firings, time.Now().UTC())
	if len(s.Firings) > maxFiringHistory {
		s.Firings = s.Firings[len(s.Firings)-maxFiringHistory:]
	}
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
