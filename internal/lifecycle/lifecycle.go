// Package lifecycle manages the on-disk state of a running sunny daemon —
// where the pid/log files live, reading and writing the state file, and
// checking whether a recorded PID is still alive.
package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type Paths struct {
	Root  string
	Run   string
	State string
	Log   string
}

func PathsFor(root string) Paths {
	run := filepath.Join(root, "run")
	return Paths{
		Root:  root,
		Run:   run,
		State: filepath.Join(run, "state.json"),
		Log:   filepath.Join(run, "sunny.log"),
	}
}

type State struct {
	PID       int       `json:"pid"`
	Addr      string    `json:"addr"`
	StartedAt time.Time `json:"started_at"`
	Binary    string    `json:"binary"`
}

func (p Paths) LoadState() (*State, error) {
	data, err := os.ReadFile(p.State)
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p.State, err)
	}
	return &s, nil
}

func (p Paths) SaveState(s *State) error {
	if err := os.MkdirAll(p.Run, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.State, data, 0o644)
}

func (p Paths) ClearState() error {
	err := os.Remove(p.State)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// IsAlive returns true if a process with the given PID exists. Uses signal 0,
// the standard POSIX "is the process there?" probe.
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
