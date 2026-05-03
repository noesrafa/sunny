package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ErrAlreadyRunning is returned by Acquire when another sunnytui process
// already holds the lock. The error message includes the live PID.
var ErrAlreadyRunning = errors.New("sunnytui already running")

// Lock is a held single-instance lock. Call Release on shutdown to free it.
type Lock struct{ path string }

// lockPath returns ~/.sunnytui/sunny.lock.
func lockPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".sunnytui", "sunny.lock"), nil
}

// Acquire takes the global single-instance lock. Returns ErrAlreadyRunning
// (wrapped with the live PID) when another sunnytui process is already
// running. If the lock file exists but its PID is dead, the file is treated
// as stale and overwritten.
//
// We use O_CREATE|O_EXCL for the happy path so two simultaneous starts can't
// both succeed. The stale-takeover path is best-effort: a tiny window exists
// where two processes both see a stale PID and race; in practice this only
// matters if two starts happen within milliseconds of each other AND the
// previous instance just crashed, which is rare enough to ignore.
func Acquire() (*Lock, error) {
	p, err := lockPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}

	if err := writeLock(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY); err == nil {
		return &Lock{path: p}, nil
	} else if !os.IsExist(err) {
		return nil, err
	}

	// Lock file already exists — check whether its owner is still alive.
	raw, rerr := os.ReadFile(p)
	if rerr != nil {
		return nil, rerr
	}
	pid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if perr != nil || pid <= 0 || !pidAlive(pid) {
		// Stale lock — take it over.
		if err := writeLock(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY); err != nil {
			return nil, err
		}
		return &Lock{path: p}, nil
	}
	return nil, fmt.Errorf("%w (pid %d)", ErrAlreadyRunning, pid)
}

// Release deletes the lock file. Safe to call on a nil receiver.
func (l *Lock) Release() {
	if l == nil {
		return
	}
	_ = os.Remove(l.path)
}

func writeLock(p string, flags int) error {
	f, err := os.OpenFile(p, flags, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%d\n", os.Getpid())
	return err
}

// pidAlive reports whether a process with the given pid is currently running.
// Uses signal 0 (kill -0) which is the canonical "does this PID exist" probe
// on Unix; it never actually delivers a signal.
func pidAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return true
}

// LockPath returns the on-disk path of the lock file. Useful for log
// messages and the "already running" hint.
func LockPath() string {
	p, _ := lockPath()
	return p
}
