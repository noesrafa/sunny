package runs

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/noesrafa/sunny/internal/events"
)

// Status is the runtime state of a run, NOT persisted. A run that
// the daemon has never touched reports as Stopped.
type Status string

const (
	StatusStopped Status = "stopped"
	StatusRunning Status = "running"
	StatusExited  Status = "exited" // exit code 0
	StatusFailed  Status = "failed" // exit code != 0 (or signaled)
)

// State is the in-memory live state of a run.
type State struct {
	PID       int        `json:"pid,omitempty"`
	Status    Status     `json:"status"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	ExitedAt  *time.Time `json:"exited_at,omitempty"`
	ExitCode  *int       `json:"exit_code,omitempty"`
}

// Runtime supervises live run processes. One Runtime per daemon.
// Constructed with the same Store the HTTP layer uses; reads
// definitions through it (no separate cache to keep in sync).
type Runtime struct {
	store   *Store
	hub     *events.Hub
	log     *slog.Logger
	logsDir string

	mu   sync.Mutex
	live map[string]*managed
}

// managed is a single live (or recently-exited) run. An entry stays
// in Runtime.live across restarts of the same id — Start replaces
// the *managed in place when the previous run has finished.
type managed struct {
	runID string
	cmd   *exec.Cmd
	sink  *logSink
	pgid  int
	done  chan struct{}

	mu             sync.RWMutex
	state          State
	stopRequested  bool // set by Stop before signaling so waitFor knows the
	                   // non-zero exit was user-initiated, not a crash
}

// New constructs a Runtime. logsDir is where per-run log files are
// written, typically <root>/runs/.
func New(store *Store, hub *events.Hub, log *slog.Logger, logsDir string) *Runtime {
	return &Runtime{
		store:   store,
		hub:     hub,
		log:     log,
		logsDir: logsDir,
		live:    map[string]*managed{},
	}
}

// State returns the current runtime state for id. Runs not yet
// tracked (never started) report as Stopped.
func (rt *Runtime) State(id string) State {
	rt.mu.Lock()
	m, ok := rt.live[id]
	rt.mu.Unlock()
	if !ok {
		return State{Status: StatusStopped}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

// Start spawns the run via `sh -c <command>` in a fresh process
// group. Returns an error if the run is already running, or if the
// definition is missing.
func (rt *Runtime) Start(id string) error {
	def, err := rt.store.Get(id)
	if err != nil {
		return err
	}

	rt.mu.Lock()
	if existing, ok := rt.live[id]; ok {
		existing.mu.RLock()
		st := existing.state.Status
		existing.mu.RUnlock()
		if st == StatusRunning {
			rt.mu.Unlock()
			return fmt.Errorf("run %s already running", id)
		}
	}

	sink, err := newLogSink(filepath.Join(rt.logsDir, id+".log"))
	if err != nil {
		rt.mu.Unlock()
		return fmt.Errorf("open log: %w", err)
	}
	cmd := exec.Command("sh", "-c", def.Command)
	cmd.Dir = def.Cwd
	configureProcessGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		sink.Close()
		rt.mu.Unlock()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		sink.Close()
		rt.mu.Unlock()
		return err
	}
	if err := cmd.Start(); err != nil {
		sink.Close()
		rt.mu.Unlock()
		return fmt.Errorf("spawn: %w", err)
	}

	now := time.Now().UTC()
	m := &managed{
		runID: id,
		cmd:   cmd,
		sink:  sink,
		// Setpgid above made the child its own group leader, so PID
		// == PGID for kill(-pgid, …).
		pgid: cmd.Process.Pid,
		done: make(chan struct{}),
		state: State{
			PID:       cmd.Process.Pid,
			Status:    StatusRunning,
			StartedAt: &now,
		},
	}
	rt.live[id] = m
	rt.mu.Unlock()

	go pumpLog(stdout, "out", sink)
	go pumpLog(stderr, "err", sink)
	go rt.waitFor(m)

	rt.publish(events.RunStarted, id)
	return nil
}

// waitFor blocks until the child exits, then finalizes m's state,
// closes the log sink, fires run.exited, and closes m.done so any
// caller blocked on Stop / Restart can proceed.
func (rt *Runtime) waitFor(m *managed) {
	err := m.cmd.Wait()

	now := time.Now().UTC()
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			code = -1
		}
	}

	m.mu.Lock()
	m.state.PID = 0
	m.state.ExitedAt = &now
	m.state.ExitCode = &code
	switch {
	case m.stopRequested:
		// User asked to stop; non-zero from SIGTERM is expected and
		// should not surface as failure.
		m.state.Status = StatusStopped
	case code == 0:
		m.state.Status = StatusExited
	default:
		m.state.Status = StatusFailed
	}
	m.mu.Unlock()

	m.sink.Close()
	rt.publish(events.RunExited, m.runID)
	close(m.done)
}

// Stop sends SIGTERM to the run's process group, waits up to 5s
// for the wait goroutine to finalize, then sends SIGKILL if still
// alive. Idempotent: no-op for runs that aren't running.
func (rt *Runtime) Stop(id string) error {
	rt.mu.Lock()
	m, ok := rt.live[id]
	rt.mu.Unlock()
	if !ok {
		return nil
	}
	m.mu.Lock()
	st := m.state.Status
	if st != StatusRunning {
		m.mu.Unlock()
		return nil
	}
	m.stopRequested = true
	m.mu.Unlock()

	if err := killGroup(m.pgid, syscall.SIGTERM); err != nil {
		rt.log.Warn("run stop SIGTERM", "id", id, "err", err)
	}
	select {
	case <-m.done:
		rt.publish(events.RunStopped, id)
		return nil
	case <-time.After(5 * time.Second):
	}
	if err := killGroup(m.pgid, syscall.SIGKILL); err != nil {
		rt.publish(events.RunStopped, id)
		return fmt.Errorf("SIGKILL: %w", err)
	}
	<-m.done
	rt.publish(events.RunStopped, id)
	return nil
}

// Restart stops then starts. Stop blocks until the previous run
// has finalized via m.done, so the subsequent Start sees a clean
// "not running" slot.
func (rt *Runtime) Restart(id string) error {
	if err := rt.Stop(id); err != nil {
		return err
	}
	return rt.Start(id)
}

// StopAll terminates every currently-running run. Used by the
// daemon's shutdown hook so children don't outlive the daemon.
// Stops happen in parallel but each respects the per-run 5s grace.
func (rt *Runtime) StopAll(ctx context.Context) {
	rt.mu.Lock()
	ids := make([]string, 0, len(rt.live))
	for id := range rt.live {
		ids = append(ids, id)
	}
	rt.mu.Unlock()

	var wg sync.WaitGroup
	for _, id := range ids {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := rt.Stop(id); err != nil {
				rt.log.Warn("StopAll", "id", id, "err", err)
			}
		}()
	}
	doneCh := make(chan struct{})
	go func() { wg.Wait(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-ctx.Done():
		rt.log.Warn("StopAll deadline exceeded; some runs may still be alive")
	}
}

// Forget drops the in-memory entry for id. Used by the HTTP delete
// handler after a successful Stop so a re-creation of the same id
// (collision-resistant, but possible) doesn't see stale state.
func (rt *Runtime) Forget(id string) {
	rt.mu.Lock()
	delete(rt.live, id)
	rt.mu.Unlock()
}

// TailLog returns the last n lines of the run's on-disk log. n <= 0
// returns the whole file. Missing log → nil slice, no error.
func (rt *Runtime) TailLog(id string, n int) ([]LogLine, error) {
	return TailLog(filepath.Join(rt.logsDir, id+".log"), n)
}

// WatchLog subscribes to the live log of id. Returns nil chan and
// a no-op release if the run isn't currently running. Always defer
// the release.
func (rt *Runtime) WatchLog(id string) (<-chan LogLine, func()) {
	rt.mu.Lock()
	m, ok := rt.live[id]
	rt.mu.Unlock()
	if !ok {
		return nil, func() {}
	}
	m.mu.RLock()
	st := m.state.Status
	m.mu.RUnlock()
	if st != StatusRunning {
		return nil, func() {}
	}
	return m.sink.Subscribe()
}

func (rt *Runtime) publish(t events.Type, id string) {
	if rt.hub == nil {
		return
	}
	rt.hub.Publish(events.Event{Type: t, RunID: id})
}
