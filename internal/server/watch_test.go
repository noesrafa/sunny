package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noesrafa/sunny/internal/conv"
	"github.com/noesrafa/sunny/internal/conversation"
	"github.com/noesrafa/sunny/internal/engine"
	"github.com/noesrafa/sunny/internal/store"
)

// newTestServer wires up the minimum needed to exercise the watch
// endpoint: a real conversation store + sink, an empty agent store
// pre-loaded with the slug we'll watch, and a noop engine pointer.
// Auth is disabled (Token: "") so tests don't have to plumb a bearer.
func newTestServer(t *testing.T) (*httptest.Server, *conv.Sink, string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "agents", "test"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Prompt file so the store can resolve the agent.
	if err := os.WriteFile(filepath.Join(root, "agents", "test", "agent.yaml"),
		[]byte("name: test\nmodel: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	convs := conversation.NewStore(root)
	meta, err := convs.Create("test", "t", "", "")
	if err != nil {
		t.Fatal(err)
	}
	sink := conv.NewSink(convs, nil)

	var enginePtr atomic.Pointer[engine.Engine]
	h := New(Options{
		Store:         st,
		Conversations: convs,
		Sink:          sink,
		Engine:        &enginePtr,
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		Token:         "", // disable auth
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, sink, "test", meta.ID
}

// watchClient consumes an SSE stream into a slice of journal events
// until ctx is cancelled or the stream ends. Returns whatever was
// observed at that point.
func watchClient(ctx context.Context, t *testing.T, url string) <-chan conversation.Event {
	t.Helper()
	out := make(chan conversation.Event, 64)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("watch: status %d", resp.StatusCode)
	}
	go func() {
		defer close(out)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<14), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue // heartbeat or framing
			}
			payload := strings.TrimPrefix(line, "data: ")
			var ev conversation.Event
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				continue
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func TestWatchReplaysJournalFromSinceZero(t *testing.T) {
	srv, sink, slug, id := newTestServer(t)

	// Pre-fill the journal before any watcher connects.
	for i := 0; i < 3; i++ {
		if _, err := sink.Append(slug, id, "user", map[string]string{"text": "x"}); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	url := srv.URL + "/agents/" + slug + "/conversations/" + id + "/watch?since=0"
	ch := watchClient(ctx, t, url)

	got := drain(ch, 3, time.Second)
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	for i, ev := range got {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event %d seq = %d, want %d", i, ev.Seq, i+1)
		}
	}
}

func TestWatchSinceSkipsEarlierEvents(t *testing.T) {
	srv, sink, slug, id := newTestServer(t)
	for i := 0; i < 5; i++ {
		sink.Append(slug, id, "user", map[string]string{})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	url := srv.URL + "/agents/" + slug + "/conversations/" + id + "/watch?since=3"
	ch := watchClient(ctx, t, url)
	got := drain(ch, 2, time.Second)
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2 (seqs 4,5)", len(got))
	}
	if got[0].Seq != 4 || got[1].Seq != 5 {
		t.Fatalf("got seqs %d,%d; want 4,5", got[0].Seq, got[1].Seq)
	}
}

func TestWatchTailsLiveEvents(t *testing.T) {
	srv, sink, slug, id := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	url := srv.URL + "/agents/" + slug + "/conversations/" + id + "/watch"
	ch := watchClient(ctx, t, url)

	// Give the watcher a moment to subscribe before we publish.
	time.Sleep(100 * time.Millisecond)
	for i := 0; i < 3; i++ {
		sink.Append(slug, id, "text_delta", map[string]string{"text": "tick"})
	}
	got := drain(ch, 3, time.Second)
	if len(got) != 3 {
		t.Fatalf("got %d live events, want 3", len(got))
	}
}

func TestWatchReplayThenLiveNoDuplicates(t *testing.T) {
	srv, sink, slug, id := newTestServer(t)

	// Two events before the watcher connects.
	sink.Append(slug, id, "user", map[string]string{})
	sink.Append(slug, id, "text_delta", map[string]string{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	url := srv.URL + "/agents/" + slug + "/conversations/" + id + "/watch?since=0"
	ch := watchClient(ctx, t, url)

	// Two more after — should arrive via live tail.
	time.Sleep(100 * time.Millisecond)
	sink.Append(slug, id, "text_delta", map[string]string{})
	sink.Append(slug, id, "done", map[string]string{})

	got := drain(ch, 4, 2*time.Second)
	if len(got) != 4 {
		t.Fatalf("got %d events, want 4", len(got))
	}
	// Seqs must be strictly monotonic — that proves no dup made it
	// through the journal/live boundary.
	for i := 1; i < len(got); i++ {
		if got[i].Seq <= got[i-1].Seq {
			t.Fatalf("seq regression at %d: %d after %d", i, got[i].Seq, got[i-1].Seq)
		}
	}
}

func drain(ch <-chan conversation.Event, n int, timeout time.Duration) []conversation.Event {
	out := []conversation.Event{}
	deadline := time.After(timeout)
	for len(out) < n {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			return out
		}
	}
	return out
}
