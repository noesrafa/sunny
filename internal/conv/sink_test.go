package conv

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/noesrafa/sunny/internal/conversation"
)

// newTestSink builds a Sink rooted at a temp dir with one agent and
// one fresh conversation already created. Returns the sink + slug +
// conv id.
func newTestSink(t *testing.T) (*Sink, string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "agents", "test"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := conversation.NewStore(root)
	meta, err := store.Create("test", "t", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return NewSink(store, nil), "test", meta.ID
}

func TestSinkAssignsMonotonicSeq(t *testing.T) {
	s, slug, id := newTestSink(t)
	for i := 1; i <= 5; i++ {
		ev, err := s.Append(slug, id, "text_delta", map[string]string{"text": "hi"})
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		if got, want := ev.Seq, int64(i); got != want {
			t.Fatalf("seq = %d, want %d", got, want)
		}
	}
}

func TestSinkSubscribeReceivesLiveEvents(t *testing.T) {
	s, slug, id := newTestSink(t)
	ch, current, cancel := s.Subscribe(slug, id)
	defer cancel()
	if current != 0 {
		t.Fatalf("current at subscribe = %d, want 0 (no events yet)", current)
	}
	want, err := s.Append(slug, id, "user", map[string]string{"text": "ping"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-ch:
		if got.Seq != want.Seq || got.Kind != "user" {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber didn't receive event")
	}
}

func TestSinkSubscribeReportsHeadSeqAtJoin(t *testing.T) {
	s, slug, id := newTestSink(t)
	for i := 0; i < 3; i++ {
		if _, err := s.Append(slug, id, "user", map[string]string{}); err != nil {
			t.Fatal(err)
		}
	}
	_, current, cancel := s.Subscribe(slug, id)
	defer cancel()
	if current != 3 {
		t.Fatalf("current at subscribe = %d, want 3", current)
	}
}

func TestSinkPrimesCounterFromJournal(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "agents", "test"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := conversation.NewStore(root)
	meta, err := store.Create("test", "t", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Sink #1 writes 4 events.
	s1 := NewSink(store, nil)
	for i := 0; i < 4; i++ {
		if _, err := s1.Append("test", meta.ID, "user", map[string]string{}); err != nil {
			t.Fatal(err)
		}
	}

	// Sink #2 sees them on first touch via the journal scan, so the
	// next event it writes is seq=5, not seq=1 (which would shadow).
	s2 := NewSink(store, nil)
	ev, err := s2.Append("test", meta.ID, "text_delta", map[string]string{"text": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Seq != 5 {
		t.Fatalf("after restart, next seq = %d, want 5", ev.Seq)
	}
}

func TestSinkConcurrentAppendsSeqsAreUnique(t *testing.T) {
	s, slug, id := newTestSink(t)
	const N = 50
	var wg sync.WaitGroup
	seen := make(chan int64, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ev, err := s.Append(slug, id, "user", map[string]string{})
			if err != nil {
				t.Errorf("append: %v", err)
				return
			}
			seen <- ev.Seq
		}()
	}
	wg.Wait()
	close(seen)
	bag := map[int64]bool{}
	for sq := range seen {
		if bag[sq] {
			t.Fatalf("duplicate seq %d", sq)
		}
		bag[sq] = true
	}
	if len(bag) != N {
		t.Fatalf("got %d unique seqs, want %d", len(bag), N)
	}
}
