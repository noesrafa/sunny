package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestAcquire walks through the three lock outcomes — fresh acquire, "already
// running" while the holder is alive, stale takeover when the recorded PID is
// gone — without spawning a real subprocess.
func TestAcquire(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	lockFile := filepath.Join(tmp, ".sunny", "sunny.lock")

	// 1. Fresh: no file, Acquire succeeds and writes our PID.
	l, err := Acquire()
	if err != nil {
		t.Fatalf("fresh Acquire: %v", err)
	}
	raw, err := os.ReadFile(lockFile)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if got := string(raw); got == "" {
		t.Fatalf("lock file empty")
	}

	// 2. Live holder: a second Acquire while our process still owns the lock
	// must return ErrAlreadyRunning.
	if _, err := Acquire(); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("expected ErrAlreadyRunning, got %v", err)
	}

	l.Release()

	// 3. Stale takeover: write a clearly-dead PID, then Acquire should
	// overwrite the file rather than refuse.
	if err := os.WriteFile(lockFile, []byte("999999\n"), 0o644); err != nil {
		t.Fatalf("seed stale lock: %v", err)
	}
	l2, err := Acquire()
	if err != nil {
		t.Fatalf("stale takeover: %v", err)
	}
	raw, _ = os.ReadFile(lockFile)
	if string(raw) == "999999\n" {
		t.Fatalf("stale lock not overwritten: %q", string(raw))
	}
	l2.Release()

	// 4. Release deletes the file — a subsequent Acquire is fresh again.
	if _, err := os.Stat(lockFile); !os.IsNotExist(err) {
		t.Fatalf("Release should remove lock, stat err=%v", err)
	}
}

// TestReleaseNilSafe documents that Release on a nil receiver is a no-op,
// so callers can use `defer lock.Release()` immediately after Acquire even
// when the error path is taken.
func TestReleaseNilSafe(t *testing.T) {
	var l *Lock
	l.Release()
}
