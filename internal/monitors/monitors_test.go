package monitors

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noesrafa/sunny/internal/events"
)

func TestEvaluateWhen(t *testing.T) {
	item := Item{Fields: map[string]any{"text": "Review this MR https://github.com/x/y/pull/3"}}
	cases := []struct {
		name  string
		cond  map[string]any
		match bool
	}{
		{"empty matches", map[string]any{}, true},
		{"text_matches hit", map[string]any{"text_matches": "github\\.com/.+/pull/\\d+"}, true},
		{"text_matches miss", map[string]any{"text_matches": "deploy"}, false},
		{"all both hit", map[string]any{"all": []any{
			map[string]any{"text_matches": "Review"},
			map[string]any{"text_matches": "MR"},
		}}, true},
		{"all one miss", map[string]any{"all": []any{
			map[string]any{"text_matches": "Review"},
			map[string]any{"text_matches": "nope"},
		}}, false},
		{"any hit", map[string]any{"any": []any{
			map[string]any{"text_matches": "deploy"},
			map[string]any{"text_matches": "MR"},
		}}, true},
		{"any miss", map[string]any{"any": []any{
			map[string]any{"text_matches": "deploy"},
			map[string]any{"text_matches": "nope"},
		}}, false},
		{"unknown key", map[string]any{"foo": "bar"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := EvaluateWhen(c.cond, item)
			if got != c.match {
				t.Errorf("got %v, want %v", got, c.match)
			}
		})
	}
}

func TestSubstitute(t *testing.T) {
	item := Item{Fields: map[string]any{"text": "hi", "url": "https://x"}}
	vars := map[string]any{
		"dispatch":  "approved",
		"otherthing": map[string]any{"score": 7},
	}
	cases := []struct {
		in, want string
	}{
		{"plain", "plain"},
		{"${item.text}", "hi"},
		{"see ${item.url}", "see https://x"},
		{"${dispatch.result}", "approved"},
		{"${otherthing.score}", "7"},
		{"${item.missing}", "${item.missing}"},
		{"${unknown.field}", "${unknown.field}"},
		{"${dispatch.result} on ${item.url}", "approved on https://x"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := Substitute(c.in, item, vars)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestStateMarkSeenCap(t *testing.T) {
	s := &State{}
	for i := 0; i < maxSeenIDs+50; i++ {
		s.MarkSeen("id-" + strings.Repeat("x", i%4))
	}
	if len(s.Seen) > maxSeenIDs {
		t.Errorf("Seen exceeded cap: %d", len(s.Seen))
	}
}

func TestStateLoadSaveRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	in := &State{
		Cursor: "abc",
		Seen:   []string{"a", "b"},
		Vars:   map[string]any{"counter": float64(3)},
	}
	if err := SaveState(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Cursor != "abc" {
		t.Errorf("cursor: got %v", out.Cursor)
	}
	if len(out.Seen) != 2 || out.Seen[0] != "a" {
		t.Errorf("seen: %v", out.Seen)
	}
	if out.Vars["counter"] != float64(3) {
		t.Errorf("vars: %v", out.Vars)
	}
}

func TestStateLoadMissingFile(t *testing.T) {
	s, err := LoadState(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s == nil || s.Vars == nil {
		t.Fatalf("expected initialized state, got %+v", s)
	}
}

func TestSaveEnabledFlipsField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.yaml")
	if err := os.WriteFile(path, []byte("name: foo\nenabled: true\nsource:\n  type: shell\n  command: \"echo []\"\nrules: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveEnabled(path, false); err != nil {
		t.Fatal(err)
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Enabled {
		t.Errorf("expected disabled after toggle")
	}
}

// fakeAction records every Run call and returns a fixed result.
type fakeAction struct {
	calls atomic.Int32
	last  atomic.Value // map[string]any
	res   any
	err   error
}

func (a *fakeAction) Type() string { return "fake" }

func (a *fakeAction) Run(ctx context.Context, cfg map[string]any, item Item, vars map[string]any) (any, error) {
	a.calls.Add(1)
	a.last.Store(cfg)
	return a.res, a.err
}

// TestSchedulerEndToEnd writes a YAML, runs the scheduler against
// a shell source that prints two items, and verifies the action
// fires exactly once per item (dedup) and history records both.
func TestSchedulerEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns sh")
	}
	root := t.TempDir()
	monitorsDir := filepath.Join(root, "monitors")
	if err := os.MkdirAll(monitorsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	yaml := `name: test
enabled: true
interval: 1s
source:
  type: shell
  command: 'printf "[{\"id\":\"a\",\"text\":\"hello\"},{\"id\":\"b\",\"text\":\"world\"}]"'
rules:
  - name: react
    when:
      text_matches: "."
    then:
      - fake:
          payload: "${item.text}"
`
	if err := os.WriteFile(filepath.Join(monitorsDir, "test.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry()
	reg.RegisterSource(ShellSource{})
	fa := &fakeAction{res: "ok"}
	reg.RegisterAction(fa)

	hub := events.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer hub.Close()
	sch := New(monitorsDir, reg, hub, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sch.Start(ctx)

	// Wait up to 3s for the action to have fired exactly twice (once per item).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fa.calls.Load() >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if fa.calls.Load() != 2 {
		t.Fatalf("expected 2 action calls, got %d", fa.calls.Load())
	}

	// Now sleep one more interval — items are deduped, so calls
	// must NOT increase.
	time.Sleep(1500 * time.Millisecond)
	if fa.calls.Load() != 2 {
		t.Fatalf("dedup failed: got %d calls", fa.calls.Load())
	}

	// History should have two entries.
	cancel()
	sch.Stop()
	entries, err := TailHistory(filepath.Join(monitorsDir, ".history", "test.jsonl"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(entries))
	}
	if entries[0].Actions[0].Type != "fake" {
		t.Errorf("history action type: %s", entries[0].Actions[0].Type)
	}

	// Substitution: the action's cfg.payload should be the item's text.
	cfg, _ := fa.last.Load().(map[string]any)
	payload, _ := cfg["payload"].(string)
	if payload != "world" {
		t.Errorf("substitution failed: got %q, want world", payload)
	}
}

// TestSchedulerToggle verifies the HTTP toggle path: flip enabled
// in the YAML and observe the worker stop firing.
func TestSchedulerToggle(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns sh")
	}
	root := t.TempDir()
	monitorsDir := filepath.Join(root, "monitors")
	_ = os.MkdirAll(monitorsDir, 0o755)
	path := filepath.Join(monitorsDir, "toggle.yaml")
	yaml := `name: toggle
enabled: true
interval: 1s
source:
  type: shell
  command: 'printf "[{\"id\":\"x\",\"text\":\"t\"}]"'
rules:
  - name: r
    when: {text_matches: "."}
    then:
      - fake: {}
`
	_ = os.WriteFile(path, []byte(yaml), 0o644)

	reg := NewRegistry()
	reg.RegisterSource(ShellSource{})
	fa := &fakeAction{res: "ok"}
	reg.RegisterAction(fa)

	hub := events.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer hub.Close()
	sch := New(monitorsDir, reg, hub, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sch.Start(ctx)

	// First fire
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && fa.calls.Load() < 1 {
		time.Sleep(50 * time.Millisecond)
	}
	if fa.calls.Load() < 1 {
		t.Fatal("worker did not fire before toggle")
	}

	// Toggle off — worker should stop.
	if err := sch.Toggle("toggle", false); err != nil {
		t.Fatal(err)
	}
	before := fa.calls.Load()
	time.Sleep(2 * time.Second)
	if got := fa.calls.Load(); got != before {
		t.Errorf("worker still firing after toggle off: %d → %d", before, got)
	}
	cancel()
	sch.Stop()
}

func TestLoadFallsBackToFilenameWhenNameMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "filename-name.yaml")
	if err := os.WriteFile(path, []byte("enabled: true\nsource:\n  type: shell\n  command: 'echo []'\nrules: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "filename-name" {
		t.Errorf("name fallback failed: got %q", m.Name)
	}
}

// silence unused import warnings if a future refactor drops them
var _ = errors.New
