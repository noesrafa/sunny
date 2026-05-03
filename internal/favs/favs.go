// Package favs persists user-saved terminal command snippets ("favorites")
// so the new-pane dialog can offer 1-press spawning of common workflows
// (e.g. `claude --dangerously-skip-permissions`, `lazygit`, `bun run dev`).
//
// File: ~/.sunnytui/favs.json
package favs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Favorite struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Cwd     string `json:"cwd,omitempty"`
}

func path() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".sunnytui", "favs.json")
}

// Load reads the favorites file. Missing file returns ([], nil) so a fresh
// install just shows an empty list.
func Load() ([]Favorite, error) {
	p := path()
	if p == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return seedDefaults(), nil
		}
		return nil, err
	}
	var f []Favorite
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	return f, nil
}

// Save writes the slice atomically.
func Save(favs []Favorite) error {
	p := path()
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(favs, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Add appends a new favorite, dedup'ing by name. Returns the updated slice.
func Add(name, command, cwd string) ([]Favorite, error) {
	name = strings.TrimSpace(name)
	command = strings.TrimSpace(command)
	if name == "" || command == "" {
		return nil, errors.New("name and command required")
	}
	favs, _ := Load()
	for i := range favs {
		if favs[i].Name == name {
			favs[i].Command = command
			favs[i].Cwd = cwd
			return favs, Save(favs)
		}
	}
	favs = append(favs, Favorite{Name: name, Command: command, Cwd: cwd})
	return favs, Save(favs)
}

// Remove deletes by name; no-op if missing.
func Remove(name string) ([]Favorite, error) {
	favs, _ := Load()
	out := favs[:0]
	for _, f := range favs {
		if f.Name != name {
			out = append(out, f)
		}
	}
	return out, Save(out)
}

// Path returns the on-disk location for diagnostics.
func Path() string { return path() }

// seedDefaults provides starter favorites on a fresh install. The user can
// edit/delete them via the favorites dialog later.
var seedOnce sync.Once

func seedDefaults() []Favorite {
	defaults := []Favorite{
		{Name: "claude", Command: "claude"},
		{Name: "claude (yolo)", Command: "claude --dangerously-skip-permissions"},
		{Name: "shell", Command: ""}, // empty → uses $SHELL
	}
	seedOnce.Do(func() { _ = Save(defaults) })
	return defaults
}
