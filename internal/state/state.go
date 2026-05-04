// Package state persists the TUI's between-runs UI preferences in
// ~/.sunny/state.json.
//
// Scope (v8): only what's genuinely local to ONE TUI device — theme,
// drafts (the unsent textarea text per tab), and which tab was
// active per peer. The "open tabs" themselves moved to the daemon
// in v0.18 (~/.sunny/tabs.json on each daemon) so multiple TUIs on
// the same daemon see the same tab set in real time. Schema mismatches
// or missing files degrade to an Empty() prefs document — no
// migrations, the daemon owns the canonical state.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Version is the on-disk schema version. v8 introduced the
// daemon-owned tab model; sessions/tabs no longer live here.
const Version = 8

// PeerPrefs is everything we remember between runs about ONE
// federation peer, scoped to this TUI device.
//
//   - ActiveTabID: which tab the user had focused last time we
//     visited this peer. On boot we restore that tab as the
//     active one within the peer's session list.
//   - Drafts: tab_id → unsent textarea text. Drafts are device-
//     local on purpose — what you were typing on your laptop
//     shouldn't auto-appear on your phone mid-sentence.
type PeerPrefs struct {
	ActiveTabID string            `json:"active_tab_id,omitempty"`
	Drafts      map[string]string `json:"drafts,omitempty"`
}

// State is the root document. PeerState is keyed by peer name (the
// same names used by client.Federation).
type State struct {
	Version   int                   `json:"version"`
	Theme     string                `json:"theme,omitempty"`
	PeerState map[string]*PeerPrefs `json:"peer_state,omitempty"`
}

func path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".sunny", "state.json"), nil
}

// Empty returns a zero-value State with the current Version stamped.
// Use this as the bootstrap value when no file exists or the file
// is unreadable.
func Empty() *State {
	return &State{Version: Version, PeerState: map[string]*PeerPrefs{}}
}

// Load returns the persisted prefs, or Empty() if the file doesn't
// exist or has an incompatible version.
func Load() (*State, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return Empty(), nil
		}
		return nil, err
	}
	var probe struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || probe.Version != Version {
		return Empty(), nil
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return Empty(), nil
	}
	if st.PeerState == nil {
		st.PeerState = map[string]*PeerPrefs{}
	}
	return &st, nil
}

// Save writes atomically: temp file then rename.
func Save(st *State) error {
	if st == nil {
		return nil
	}
	st.Version = Version
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
