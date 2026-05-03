// Package state persists/restores sunnytui's between-runs UI state in a
// single ~/.sunnytui/state.json. Holds:
//
//   - Open Claude sessions (title, cwd, model, effort, draft, claude
//     session_id used for `--resume`)
//   - Open terminal panes (title, command, cwd)
//   - Which tab was active (kind + index)
//
// Runs and favorites live in their own files (different concerns).
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const Version = 3 // bumped when Items (transcript persistence) was added

type SavedSession struct {
	Title    string `json:"title"`
	Cwd      string `json:"cwd"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Draft    string `json:"draft,omitempty"`
	RemoteID string `json:"remote_id,omitempty"` // claude session_id, used with --resume

	// Items is the JSON-encoded transcript for this session
	// (session.MarshalItems / UnmarshalItems). Stored as raw bytes so the
	// state package stays decoupled from the session item types.
	Items json.RawMessage `json:"items,omitempty"`

	// Cumulative cost + turn counter, persisted so the sidebar stays
	// meaningful immediately after restore (before any new event arrives).
	TotalCost float64 `json:"total_cost,omitempty"`
	Turns     int     `json:"turns,omitempty"`
}

type SavedPane struct {
	Title   string `json:"title"`
	Command string `json:"command"`
	Cwd     string `json:"cwd,omitempty"`
}

type State struct {
	Version    int            `json:"version"`
	Sessions   []SavedSession `json:"sessions"`
	Panes      []SavedPane    `json:"panes,omitempty"`
	ActiveKind string         `json:"active_kind,omitempty"` // "claude" | "pane"
	ActiveIdx  int            `json:"active_idx"`            // index within ActiveKind's manager

	// Theme is the persisted theme ID (see internal/tui themes.go for the
	// catalog). Empty means "use the default" — the TUI's ThemeByID falls
	// back to Themes[0] for unknown values, so it's safe to leave blank.
	Theme string `json:"theme,omitempty"`
}

func path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".sunnytui", "state.json"), nil
}

// Load returns the persisted state, or a zero-value State with no error if
// the file doesn't exist. Migrates from v1 (sessions-only) silently.
func Load() (*State, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return &State{Version: Version, ActiveKind: "claude"}, nil
		}
		return nil, err
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	// Backfill from legacy ~/.sunnytui/panes.json if v1 state doesn't have
	// panes yet. After first Save() the legacy file becomes redundant.
	if len(st.Panes) == 0 {
		if legacy, lerr := loadLegacyPanes(); lerr == nil && len(legacy) > 0 {
			st.Panes = legacy
		}
	}
	if st.ActiveKind == "" {
		st.ActiveKind = "claude"
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

func Path() string {
	p, _ := path()
	return p
}

// loadLegacyPanes reads the old standalone ~/.sunnytui/panes.json so users
// who had panes from a previous version don't lose them on upgrade.
func loadLegacyPanes() ([]SavedPane, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(home, ".sunnytui", "panes.json"))
	if err != nil {
		return nil, err
	}
	type legacy struct {
		Name    string `json:"name"`
		Command string `json:"command"`
		Cwd     string `json:"cwd"`
	}
	var ls []legacy
	if err := json.Unmarshal(raw, &ls); err != nil {
		return nil, err
	}
	out := make([]SavedPane, 0, len(ls))
	for _, l := range ls {
		out = append(out, SavedPane{Title: l.Name, Command: l.Command, Cwd: l.Cwd})
	}
	return out, nil
}
