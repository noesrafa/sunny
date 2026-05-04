package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noesrafa/sunny/internal/conv"
	"github.com/noesrafa/sunny/internal/conversation"
	"github.com/noesrafa/sunny/internal/engine"
	"github.com/noesrafa/sunny/internal/events"
	"github.com/noesrafa/sunny/internal/store"
	"github.com/noesrafa/sunny/internal/tabs"
)

// newStatsImpl builds a *server with one agent (`test`) and one
// conversation, plus a tabs store, hub, and sink. Returns the impl
// and the slug/convID so tests can poke registries directly without
// going through the wrapped handler chain.
func newStatsImpl(t *testing.T) (*server, string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "agents", "test"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agents", "test", "agent.yaml"),
		[]byte("name: test\nmodel: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	convs := conversation.NewStore(root)
	meta, err := convs.Create("test", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	sink := conv.NewSink(convs, nil)
	tabsStore, err := tabs.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	hub := events.New(nil)
	t.Cleanup(hub.Close)

	var enginePtr atomic.Pointer[engine.Engine]
	enginePtr.Store(engine.New(nil, "", nil))

	impl := &server{
		store:       st,
		conv:        convs,
		sink:        sink,
		tabs:        tabsStore,
		engine:      &enginePtr,
		hub:         hub,
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		activeTurns: newActiveTurnsRegistry(),
		startedAt:   time.Now().UTC(),
	}
	return impl, "test", meta.ID
}

func serveStats(t *testing.T, impl *server) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stats", impl.stats)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestStatsBasicShape(t *testing.T) {
	impl, slug, _ := newStatsImpl(t)
	url := serveStats(t, impl)
	got := fetchStats(t, url)

	if got.Counts.Agents != 1 {
		t.Errorf("agents = %d, want 1", got.Counts.Agents)
	}
	if got.Counts.Conversations != 1 {
		t.Errorf("conversations = %d, want 1", got.Counts.Conversations)
	}
	if got.Counts.ConvsPerAgent[slug] != 1 {
		t.Errorf("convs[%s] = %d, want 1", slug, got.Counts.ConvsPerAgent[slug])
	}
	if got.System.Platform != runtime.GOOS {
		t.Errorf("platform = %q, want %q", got.System.Platform, runtime.GOOS)
	}
	if got.System.NumCPU <= 0 {
		t.Errorf("num_cpu = %d, want >0", got.System.NumCPU)
	}
	if got.Process.Goroutines <= 0 {
		t.Errorf("goroutines = %d, want >0", got.Process.Goroutines)
	}
	if got.Daemon.UptimeSeconds < 0 {
		t.Errorf("uptime negative: %d", got.Daemon.UptimeSeconds)
	}
}

func TestStatsCountsTabs(t *testing.T) {
	impl, slug, convID := newStatsImpl(t)
	if _, err := impl.tabs.Add(&tabs.Tab{AgentSlug: slug, ConvID: convID, Title: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := impl.tabs.Add(&tabs.Tab{AgentSlug: slug, ConvID: convID, Title: "b"}); err != nil {
		t.Fatal(err)
	}
	got := fetchStats(t, serveStats(t, impl))
	if got.Counts.Tabs != 2 {
		t.Errorf("tabs = %d, want 2", got.Counts.Tabs)
	}
}

func TestStatsLiveTurnsAndWatchers(t *testing.T) {
	impl, slug, convID := newStatsImpl(t)

	_, busCancel := impl.hub.Subscribe()
	defer busCancel()
	_, _, watchCancel := impl.sink.Subscribe(slug, convID)
	defer watchCancel()
	_, release, ok := impl.activeTurns.claim(slug, convID)
	if !ok {
		t.Fatal("claim failed")
	}
	defer release()

	got := fetchStats(t, serveStats(t, impl))

	if len(got.Live.TurnsInFlight) != 1 {
		t.Fatalf("turns_in_flight = %d, want 1", len(got.Live.TurnsInFlight))
	}
	turn := got.Live.TurnsInFlight[0]
	if turn.Slug != slug || turn.ConvID != convID {
		t.Errorf("turn = %+v, want {%s, %s}", turn, slug, convID)
	}
	if got.Live.BusSubscribers != 1 {
		t.Errorf("bus_subscribers = %d, want 1", got.Live.BusSubscribers)
	}
	key := slug + "/" + convID
	if got.Live.Watchers[key] != 1 {
		t.Errorf("watchers[%s] = %d, want 1", key, got.Live.Watchers[key])
	}
}

func fetchStats(t *testing.T, url string) statsResponse {
	t.Helper()
	resp, err := http.Get(url + "/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d, body=%s", resp.StatusCode, body)
	}
	var got statsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return got
}
