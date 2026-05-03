package runs

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Status enumerates the lifecycle states of a Run.
type Status int

const (
	StatusStopped Status = iota
	StatusRunning
	StatusCrashed // exited with non-zero
)

func (s Status) String() string {
	switch s {
	case StatusStopped:
		return "stopped"
	case StatusRunning:
		return "running"
	case StatusCrashed:
		return "crashed"
	}
	return "?"
}

// Run is a registered shell command (e.g. "bun run dev") with optional cwd
// and runtime state attached. Persisted fields (Name, Command, Cwd) survive
// process restarts; runtime fields (Cmd, Status, …) reset to zero on load.
type Run struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Command string `json:"command"`
	Cwd     string `json:"cwd,omitempty"`

	// Runtime — not persisted.
	Status    Status     `json:"-"`
	StartedAt time.Time  `json:"-"`
	ExitCode  int        `json:"-"`
	LastErr   error      `json:"-"`
	Logs      *LogBuffer `json:"-"`

	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan struct{} // closed by wait() when the process exits
}

func (r *Run) initLogs() {
	if r.Logs == nil {
		r.Logs = NewLogBuffer(DefaultLogBufferLines)
	}
}

// Running reports whether the underlying process is currently alive.
func (r *Run) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cmd != nil && r.cmd.Process != nil && r.Status == StatusRunning
}

// PID returns the OS PID, or 0 if not running.
func (r *Run) PID() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil && r.cmd.Process != nil {
		return r.cmd.Process.Pid
	}
	return 0
}

// Uptime returns how long the run has been alive (0 if not running).
func (r *Run) Uptime() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Status != StatusRunning || r.StartedAt.IsZero() {
		return 0
	}
	return time.Since(r.StartedAt)
}

// Start spawns the command via `sh -c` and captures stdout+stderr into the
// LogBuffer. Returns an error if the run is already running or the spawn
// fails.
func (r *Run) Start() error {
	r.mu.Lock()
	if r.cmd != nil && r.Status == StatusRunning {
		r.mu.Unlock()
		return errors.New("already running")
	}

	r.initLogs()
	r.Logs.Append(fmt.Sprintf("──── started %s · %s ────",
		time.Now().Format("15:04:05"), r.Command))

	cwd := r.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	cmd := exec.Command("sh", "-c", r.Command)
	cmd.Dir = cwd
	// New process group so we can kill the whole tree (children, grandchildren).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		r.mu.Unlock()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		r.mu.Unlock()
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		r.LastErr = err
		r.Status = StatusCrashed
		r.mu.Unlock()
		return fmt.Errorf("start: %w", err)
	}

	r.cmd = cmd
	r.done = make(chan struct{})
	r.Status = StatusRunning
	r.StartedAt = time.Now()
	r.ExitCode = 0
	r.LastErr = nil
	r.mu.Unlock()

	go r.captureLines(stdout)
	go r.captureLines(stderr)

	go r.wait()
	return nil
}

func (r *Run) captureLines(rd io.ReadCloser) {
	defer rd.Close()
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	for sc.Scan() {
		ts := time.Now().Format("15:04:05")
		r.Logs.Append(ts + " " + sc.Text())
	}
}

func (r *Run) wait() {
	r.mu.Lock()
	cmd := r.cmd
	done := r.done
	r.mu.Unlock()
	if cmd == nil {
		if done != nil {
			close(done)
		}
		return
	}
	err := cmd.Wait()

	r.mu.Lock()
	r.Logs.Append(fmt.Sprintf("──── exited %s ────", time.Now().Format("15:04:05")))
	if err != nil {
		r.LastErr = err
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			r.ExitCode = ee.ExitCode()
		}
		// Don't flag as crashed if we initiated a clean stop (signal-killed).
		if r.Status == StatusRunning {
			r.Status = StatusCrashed
		}
	} else {
		r.ExitCode = 0
		r.Status = StatusStopped
	}
	r.cmd = nil
	r.mu.Unlock()
	close(done)
}

// Stop sends SIGTERM to the process group. Falls back to SIGKILL after a
// short grace period if the process refuses to exit. Returns when wait()
// has observed the exit and updated state — so callers know runtime fields
// (ExitCode, Status) reflect the kill by the time Stop returns.
func (r *Run) Stop() error {
	r.mu.Lock()
	cmd := r.cmd
	if cmd == nil || cmd.Process == nil {
		r.mu.Unlock()
		return errors.New("not running")
	}
	r.Status = StatusStopped // mark intent so wait() doesn't flip to Crashed
	pid := cmd.Process.Pid
	done := r.done
	r.mu.Unlock()

	// Negative PID targets the whole process group.
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}

	// Grace: 2s for graceful exit, then SIGKILL. We observe the existing
	// wait() goroutine via the done channel rather than calling
	// cmd.Process.Wait() ourselves — concurrent Wait calls on the same
	// Process produce "wait already called" errors and burn cycles.
	if done == nil {
		return nil
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = syscall.Kill(pid, syscall.SIGKILL)
		<-done
	}
	return nil
}

// Restart stops the run (if running) and starts it again.
func (r *Run) Restart() error {
	if r.Running() {
		if err := r.Stop(); err != nil {
			return err
		}
		// Brief settle time so the new process doesn't fight the old one
		// for the port/socket.
		time.Sleep(150 * time.Millisecond)
	}
	return r.Start()
}

// LogFilePath returns where stdout+stderr is mirrored to disk for
// out-of-TUI tailing.
func LogFilePath(id string) string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".sunnytui", "runs")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, id+".log")
}

// SanitizeName lowercases + strips weirdness so users can use the name as a
// stable id.
func SanitizeName(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		case r == ' ':
			out = append(out, '-')
		}
	}
	return string(out)
}
