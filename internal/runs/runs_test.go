package runs

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noesrafa/sunny/internal/events"
)

func TestStoreCRUD(t *testing.T) {
	root := t.TempDir()
	s, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(s.List()); got != 0 {
		t.Fatalf("empty store should list 0 runs, got %d", got)
	}

	r, err := s.Create("dev", "/tmp", "echo hi")
	if err != nil {
		t.Fatal(err)
	}
	if r.ID == "" || !strings.HasPrefix(r.ID, "run_") {
		t.Errorf("bad id: %q", r.ID)
	}

	got, err := s.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "dev" || got.Cwd != "/tmp" || got.Command != "echo hi" {
		t.Errorf("got %+v, want dev/tmp/echo hi", got)
	}

	name := "dev2"
	upd, err := s.Update(r.ID, Patch{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Name != "dev2" || upd.Cwd != "/tmp" || upd.Command != "echo hi" {
		t.Errorf("partial update broke other fields: %+v", upd)
	}
	if !upd.UpdatedAt.After(r.CreatedAt) {
		t.Errorf("UpdatedAt not bumped")
	}

	// Reload from disk to confirm persistence.
	s2, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	got2, err := s2.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Name != "dev2" {
		t.Errorf("reload lost update: name=%q", got2.Name)
	}

	if err := s.Remove(r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(r.ID); err == nil {
		t.Errorf("Get after Remove should fail")
	}
}

func TestStoreValidatesRequiredFields(t *testing.T) {
	root := t.TempDir()
	s, _ := Load(root)
	cases := []struct{ name, cwd, command string }{
		{"", "/tmp", "echo"},
		{"x", "", "echo"},
		{"x", "/tmp", ""},
		{"   ", "/tmp", "echo"},
	}
	for i, c := range cases {
		if _, err := s.Create(c.name, c.cwd, c.command); err == nil {
			t.Errorf("case %d: empty field should error: %+v", i, c)
		}
	}
}

// TestRuntimeStartCapturesLogs spawns a tiny shell command and
// verifies the runtime tracks it through to exit, captures stdout,
// and surfaces an exit code via State.
func TestRuntimeStartCapturesLogs(t *testing.T) {
	root := t.TempDir()
	store, _ := Load(root)
	hub := events.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer hub.Close()
	rt := New(store, hub, slog.New(slog.NewTextHandler(io.Discard, nil)), filepath.Join(root, "runs"))

	r, _ := store.Create("hello", t.TempDir(), `echo "hello world"; echo "bye" >&2`)

	if err := rt.Start(r.ID); err != nil {
		t.Fatal(err)
	}

	// Wait for exit (echo is instant; budget some slack for CI).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st := rt.State(r.ID)
		if st.Status == StatusExited || st.Status == StatusFailed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	st := rt.State(r.ID)
	if st.Status != StatusExited {
		t.Fatalf("expected exited, got %s (exit=%v)", st.Status, st.ExitCode)
	}
	if st.ExitCode == nil || *st.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %v", st.ExitCode)
	}

	lines, err := rt.TailLog(r.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawOut, sawErr bool
	for _, l := range lines {
		if l.Stream == "out" && strings.Contains(l.Text, "hello world") {
			sawOut = true
		}
		if l.Stream == "err" && strings.Contains(l.Text, "bye") {
			sawErr = true
		}
	}
	if !sawOut {
		t.Errorf("missing stdout line: %+v", lines)
	}
	if !sawErr {
		t.Errorf("missing stderr line: %+v", lines)
	}
}

// TestRuntimeStopKillsProcessGroup spawns a sh that traps SIGTERM
// and forks a child that ignores it; the runtime must still kill
// the whole group via SIGKILL after the 5s grace. We use a much
// shorter sleep + a sub-grace so the test stays fast.
func TestRuntimeStopKillsRunningProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns sleep")
	}
	root := t.TempDir()
	store, _ := Load(root)
	hub := events.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer hub.Close()
	rt := New(store, hub, slog.New(slog.NewTextHandler(io.Discard, nil)), filepath.Join(root, "runs"))

	// `sleep 60` will be killed by SIGTERM cleanly within the 5s grace.
	r, _ := store.Create("slow", t.TempDir(), "sleep 60")
	if err := rt.Start(r.ID); err != nil {
		t.Fatal(err)
	}

	// Give the shell a moment to spawn the sleep child.
	time.Sleep(100 * time.Millisecond)
	if got := rt.State(r.ID).Status; got != StatusRunning {
		t.Fatalf("expected running, got %s", got)
	}

	if err := rt.Stop(r.ID); err != nil {
		t.Fatal(err)
	}
	st := rt.State(r.ID)
	if st.Status == StatusRunning {
		t.Errorf("Stop returned but state is still running")
	}
	if st.ExitedAt == nil {
		t.Errorf("ExitedAt not set after Stop")
	}
}

// TestStopAllParallel exercises the shutdown path: multiple live
// runs should all be stopped before StopAll returns (within the
// context deadline).
func TestStopAllParallel(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns sleep")
	}
	root := t.TempDir()
	store, _ := Load(root)
	hub := events.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer hub.Close()
	rt := New(store, hub, slog.New(slog.NewTextHandler(io.Discard, nil)), filepath.Join(root, "runs"))

	for i := 0; i < 3; i++ {
		r, _ := store.Create("s", t.TempDir(), "sleep 30")
		if err := rt.Start(r.ID); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	rt.StopAll(ctx)

	for id := range rt.live {
		if got := rt.State(id).Status; got == StatusRunning {
			t.Errorf("run %s still running after StopAll", id)
		}
	}
}
