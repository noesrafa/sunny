package monitors

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/noesrafa/sunny/internal/events"
)

// Scheduler is the daemon-side supervisor for every monitor on
// disk. Construct one at boot, RegisterSource/Action against its
// Registry, then call Start with the root daemon context.
//
// Lifecycle:
//
//   - Start spawns a watchdog that scans monitorsDir every
//     watchdogInterval. New YAML files spawn a worker; deleted ones
//     reap theirs; toggled-off ones get cancelled.
//   - Each worker runs its own ticker at the monitor's interval.
//     Each tick re-loads the YAML by mtime so rule edits propagate
//     without a daemon restart.
//   - Stop cancels the parent ctx and waits for every worker to
//     finish its current tick (bounded by the actions' own context-
//     awareness).
type Scheduler struct {
	root     string
	registry *Registry
	hub      *events.Hub
	log      *slog.Logger

	mu      sync.Mutex
	workers map[string]*worker
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// worker is one running monitor. Workers live in the workers map
// while their YAML file exists and is `enabled: true`.
type worker struct {
	name   string
	cancel context.CancelFunc

	mu       sync.Mutex
	running  bool
	lastFire time.Time
	lastErr  string
}

// watchdogInterval bounds the latency between an agent writing a
// new monitor file and the scheduler picking it up. 5s is fast
// enough for human-pace edits without burning CPU on directory
// scans.
const watchdogInterval = 5 * time.Second

// New constructs a Scheduler tied to monitorsDir
// (typically ~/.sunny/monitors). The Registry must be populated
// (RegisterSource / RegisterAction) before Start; sources/actions
// added after Start work too but won't apply to in-flight ticks.
func New(monitorsDir string, reg *Registry, hub *events.Hub, log *slog.Logger) *Scheduler {
	return &Scheduler{
		root:     monitorsDir,
		registry: reg,
		hub:      hub,
		log:      log,
		workers:  map[string]*worker{},
	}
}

// Registry returns the registry so callers can register sources
// and actions after construction.
func (s *Scheduler) Registry() *Registry { return s.registry }

// Start launches the watchdog. Cancelling parentCtx (or calling
// Stop) tears every worker down.
func (s *Scheduler) Start(parentCtx context.Context) {
	ctx, cancel := context.WithCancel(parentCtx)
	s.ctx = ctx
	s.cancel = cancel
	s.wg.Add(1)
	go s.watchdog(ctx)
}

// Stop cancels every worker and waits for the goroutines to drain.
// Safe to call from a deferred shutdown handler; bounded by the
// 5-second watchdog grace and whatever timeout the in-flight
// actions honor on their context.
func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

func (s *Scheduler) watchdog(ctx context.Context) {
	defer s.wg.Done()
	s.scan(ctx)
	t := time.NewTicker(watchdogInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.cancelAll()
			return
		case <-t.C:
			s.scan(ctx)
		}
	}
}

func (s *Scheduler) cancelAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.workers {
		w.cancel()
	}
	s.workers = map[string]*worker{}
}

// scan reconciles the workers map with the YAML files on disk.
// Idempotent and cheap when nothing changed.
func (s *Scheduler) scan(ctx context.Context) {
	files, _ := filepath.Glob(filepath.Join(s.root, "*.yaml"))
	seen := make(map[string]bool, len(files))

	for _, path := range files {
		m, err := Load(path)
		if err != nil {
			s.log.Warn("monitor load", "path", path, "err", err)
			continue
		}
		seen[m.Name] = true
		s.ensureWorker(ctx, path, m)
	}

	s.mu.Lock()
	for name, w := range s.workers {
		if !seen[name] {
			w.cancel()
			delete(s.workers, name)
		}
	}
	s.mu.Unlock()
}

// ensureWorker spawns or cancels a worker so that its existence
// matches the monitor's `enabled` flag. Idempotent across scans.
func (s *Scheduler) ensureWorker(ctx context.Context, path string, m *Monitor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, exists := s.workers[m.Name]
	switch {
	case !exists && m.Enabled:
		wctx, cancel := context.WithCancel(ctx)
		w = &worker{name: m.Name, cancel: cancel}
		s.workers[m.Name] = w
		s.wg.Add(1)
		go s.runWorker(wctx, w, path)
	case exists && !m.Enabled:
		w.cancel()
		delete(s.workers, m.Name)
	}
	// Existing + enabled: leave alone. The worker re-loads the
	// YAML each tick so rule/interval changes apply mid-stream.
}

// Toggle flips a monitor's enabled flag in its YAML and triggers
// an immediate rescan so the user sees the worker spawn / die
// without waiting for the next watchdog tick.
func (s *Scheduler) Toggle(name string, enabled bool) error {
	path := filepath.Join(s.root, name+".yaml")
	if err := SaveEnabled(path, enabled); err != nil {
		return err
	}
	m, err := Load(path)
	if err != nil {
		return err
	}
	if s.ctx != nil {
		s.ensureWorker(s.ctx, path, m)
	}
	if s.hub != nil {
		s.hub.Publish(events.Event{Type: events.MonitorToggled, MonitorName: name})
	}
	return nil
}

// MonitorView is the wire shape the HTTP layer returns: definition
// + runtime state.
type MonitorView struct {
	Name     string    `json:"name"`
	Enabled  bool      `json:"enabled"`
	Interval string    `json:"interval"`
	Source   string    `json:"source"`
	Running  bool      `json:"running"`
	LastFire time.Time `json:"last_fire,omitempty"`
	LastErr  string    `json:"last_err,omitempty"`
}

// List returns every monitor on disk plus its runtime state.
// Walks the directory each call (cheap, sorted).
func (s *Scheduler) List() []MonitorView {
	files, _ := filepath.Glob(filepath.Join(s.root, "*.yaml"))
	out := make([]MonitorView, 0, len(files))
	for _, path := range files {
		m, err := Load(path)
		if err != nil {
			continue
		}
		v := MonitorView{
			Name:     m.Name,
			Enabled:  m.Enabled,
			Interval: m.Interval,
			Source:   m.Source.Type,
		}
		s.mu.Lock()
		w, ok := s.workers[m.Name]
		s.mu.Unlock()
		if ok {
			w.mu.Lock()
			v.Running = true
			v.LastFire = w.lastFire
			v.LastErr = w.lastErr
			w.mu.Unlock()
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// HistoryDir returns where a monitor's .jsonl is stored. Used by
// the HTTP layer to tail history without re-deriving the path.
func (s *Scheduler) HistoryPath(name string) string {
	return filepath.Join(s.root, ".history", name+".jsonl")
}

// runWorker is one monitor's main loop. Re-reads the YAML each
// tick; exits cleanly when the file is removed or `enabled: false`
// (caller cancels via worker.cancel).
func (s *Scheduler) runWorker(ctx context.Context, w *worker, path string) {
	defer s.wg.Done()

	statePath := filepath.Join(s.root, ".state", w.name+".json")
	historyPath := s.HistoryPath(w.name)

	state, err := LoadState(statePath)
	if err != nil {
		s.log.Warn("monitor load state", "name", w.name, "err", err)
		state = &State{Vars: map[string]any{}}
	}

	m, err := Load(path)
	if err != nil {
		s.log.Error("monitor reload", "name", w.name, "err", err)
		return
	}
	currentInterval := m.IntervalDuration()
	mtime := fileMtime(path)

	// Run once on start so the user gets immediate feedback when
	// they enable a monitor instead of waiting `interval` seconds.
	s.tick(ctx, w, m, state, statePath, historyPath)

	t := time.NewTicker(currentInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Re-load if mtime changed.
			if cur := fileMtime(path); cur != mtime {
				newM, err := Load(path)
				if err == nil {
					m = newM
					mtime = cur
					if !m.Enabled {
						return
					}
					if newInt := m.IntervalDuration(); newInt != currentInterval {
						t.Stop()
						t = time.NewTicker(newInt)
						currentInterval = newInt
					}
				}
			}
			s.tick(ctx, w, m, state, statePath, historyPath)
		}
	}
}

// tick is one polling cycle. Refuses to overlap with itself; logs
// errors instead of bubbling so a bad source doesn't kill the
// worker — the next tick gets a clean shot.
func (s *Scheduler) tick(ctx context.Context, w *worker, m *Monitor, state *State, statePath, historyPath string) {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return
	}
	w.running = true
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.running = false
		w.mu.Unlock()
	}()

	src, ok := s.registry.Source(m.Source.Type)
	if !ok {
		s.setErr(w, "unknown source: "+m.Source.Type)
		return
	}
	items, newState, err := src.Fetch(ctx, m.Source.Config, state.Vars)
	if err != nil {
		s.setErr(w, err.Error())
		s.publishError(m.Name)
		return
	}
	state.Vars = newState
	s.setErr(w, "")

	for _, item := range items {
		if state.IsSeen(item.ID) {
			continue
		}
		s.processItem(ctx, m, item, state, historyPath)
		state.MarkSeen(item.ID)
	}
	state.LastFire = time.Now().UTC()
	w.mu.Lock()
	w.lastFire = state.LastFire
	w.mu.Unlock()
	if err := SaveState(statePath, state); err != nil {
		s.log.Warn("monitor save state", "name", m.Name, "err", err)
	}
}

func (s *Scheduler) processItem(ctx context.Context, m *Monitor, item Item, state *State, historyPath string) {
	limits := m.RateLimit.Effective()
	for _, rule := range m.Rules {
		if !EvaluateWhen(rule.When, item) {
			continue
		}
		// Rate-limit guard: count recent firings BEFORE doing any
		// work. We log the skip so the user can spot when their
		// monitor is being silenced, and write a no-actions history
		// entry so the scheduler still publishes "fired" and the
		// item is still marked seen — without the seen mark, the
		// same item would keep coming back every tick.
		if perMin := state.CountFiringsSince(time.Minute); perMin >= limits.PerMinute {
			s.log.Warn("monitor rate-limited (per_minute)", "name", m.Name, "rule", rule.Name, "count", perMin, "limit", limits.PerMinute)
			s.appendRateLimitHistory(historyPath, m, rule, item, "per_minute", perMin, limits.PerMinute)
			continue
		}
		if perHr := state.CountFiringsSince(time.Hour); perHr >= limits.PerHour {
			s.log.Warn("monitor rate-limited (per_hour)", "name", m.Name, "rule", rule.Name, "count", perHr, "limit", limits.PerHour)
			s.appendRateLimitHistory(historyPath, m, rule, item, "per_hour", perHr, limits.PerHour)
			continue
		}
		state.MarkFiring()
		entry := HistoryEntry{
			Ts:   time.Now().UTC(),
			Rule: rule.Name,
			Item: item.Fields,
		}
		vars := map[string]any{}
		for _, ainv := range rule.Then {
			if len(ainv) == 0 {
				continue
			}
			// Each map in `then` has exactly one key — the action
			// type. We iterate to extract it (Go maps don't have
			// pop) but break after the first.
			for atype, acfg := range ainv {
				cfgMap, _ := acfg.(map[string]any)
				expanded := SubstituteMap(cfgMap, item, vars)
				ha := s.runAction(ctx, atype, expanded, item, vars)
				entry.Actions = append(entry.Actions, ha)
				if ha.Err == "" && ha.Result != nil {
					vars[atype] = ha.Result
				}
				break
			}
		}
		if err := AppendHistory(historyPath, entry); err != nil {
			s.log.Warn("monitor history append", "name", m.Name, "err", err)
		}
		s.publishFired(m.Name)
	}
}

// appendRateLimitHistory records a rule that matched but was
// suppressed by the rate limiter. Without this the user has no
// signal in the UI that the monitor decided NOT to fire — they'd
// just see fewer entries and wonder why. The entry carries a single
// synthetic "rate_limit" action whose Err describes which window
// tripped and what the threshold was.
func (s *Scheduler) appendRateLimitHistory(historyPath string, m *Monitor, rule Rule, item Item, window string, count, limit int) {
	entry := HistoryEntry{
		Ts:   time.Now().UTC(),
		Rule: rule.Name,
		Item: item.Fields,
		Actions: []HistoryAction{{
			Type: "rate_limit",
			Err:  fmt.Sprintf("skipped: %s cap exhausted (%d/%d)", window, count, limit),
		}},
	}
	if err := AppendHistory(historyPath, entry); err != nil {
		s.log.Warn("monitor history append", "name", m.Name, "err", err)
	}
}

func (s *Scheduler) runAction(ctx context.Context, atype string, cfg map[string]any, item Item, vars map[string]any) HistoryAction {
	act, ok := s.registry.Action(atype)
	if !ok {
		return HistoryAction{Type: atype, Err: "unknown action: " + atype}
	}
	res, err := act.Run(ctx, cfg, item, vars)
	if err != nil {
		return HistoryAction{Type: atype, Result: res, Err: err.Error()}
	}
	return HistoryAction{Type: atype, Result: res}
}

func (s *Scheduler) setErr(w *worker, msg string) {
	w.mu.Lock()
	w.lastErr = msg
	w.mu.Unlock()
}

func (s *Scheduler) publishFired(name string) {
	if s.hub == nil {
		return
	}
	s.hub.Publish(events.Event{Type: events.MonitorFired, MonitorName: name})
}

func (s *Scheduler) publishError(name string) {
	if s.hub == nil {
		return
	}
	s.hub.Publish(events.Event{Type: events.MonitorError, MonitorName: name})
}

func fileMtime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return time.Time{}
		}
		return time.Time{}
	}
	return info.ModTime()
}
