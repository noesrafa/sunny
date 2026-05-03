// Package state persists/restores the TUI's between-runs UI state in a
// single ~/.sunny/state.json. Holds:
//
//   - Open chat sessions (title, cwd, model, effort, draft, conv_id
//     pointing at the server-side conversation that owns the journal)
//   - Open terminal panes (title, command, cwd)
//   - Which tab was active (kind + index)
//   - Selected theme
//
// The TUI is the source of truth for *layout* (which sessions are
// open in tabs, which is active). The daemon is the source of truth
// for the *content* of those sessions (transcripts persisted under
// agents/<slug>/conversations/<id>/).
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const Version = 4 // bumped when remote_id became conv_id (server-side conversations)

type SavedSession struct {
	Title  string `json:"title"`
	Cwd    string `json:"cwd"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	Draft  string `json:"draft,omitempty"`
	// ConvID points at ~/.sunny/agents/<slug>/conversations/<id>/ which
	// owns the persistent transcript. Empty for sessions that haven't
	// sent any message yet.
	ConvID string `json:"conv_id,omitempty"`

	// Items is the JSON-encoded transcript for this session
	// (session.MarshalItems / UnmarshalItems). Stored as raw bytes so the
	// state package stays decoupled from the session item types.
	//
	// This is a UI-side cache so the chat re-renders immediately on TUI
	// restart, before any GET /conversations/{id} round-trip. The journal
	// on disk is canonical; if it disagrees, the journal wins on reload.
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
	return filepath.Join(home, ".sunny", "state.json"), nil
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
	// Backfill from legacy ~/.sunny/panes.json if v1 state doesn't have
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

// loadLegacyPanes reads the old standalone ~/.sunny/panes.json so users
// who had panes from a previous version don't lose them on upgrade.
func loadLegacyPanes() ([]SavedPane, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(home, ".sunny", "panes.json"))
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
