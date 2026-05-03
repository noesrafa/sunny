package runs

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
)

func TestLogBuffer_AppendAndSnapshot(t *testing.T) {
	b := NewLogBuffer(10)
	b.Append("a")
	b.Append("b")
	b.Append("c")
	got := b.Snapshot()
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0] != "a" || got[2] != "c" {
		t.Errorf("ordering wrong: %v", got)
	}
}

func TestLogBuffer_RingTrimsOldest(t *testing.T) {
	b := NewLogBuffer(3)
	for i := 0; i < 5; i++ {
		b.Append(strconv.Itoa(i))
	}
	got := b.Snapshot()
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0] != "2" || got[2] != "4" {
		t.Errorf("ring trim wrong, got %v, want [2 3 4]", got)
	}
}

func TestLogBuffer_DefaultMax(t *testing.T) {
	b := NewLogBuffer(0)
	for i := 0; i < DefaultLogBufferLines+100; i++ {
		b.Append(strconv.Itoa(i))
	}
	if l := b.Len(); l != DefaultLogBufferLines {
		t.Errorf("len = %d, want %d (default max)", l, DefaultLogBufferLines)
	}
}

func TestLogBuffer_Clear(t *testing.T) {
	b := NewLogBuffer(10)
	b.Append("x")
	b.Append("y")
	b.Clear()
	if b.Len() != 0 {
		t.Errorf("Len after Clear = %d, want 0", b.Len())
	}
	// Buffer still usable after Clear.
	b.Append("z")
	if got := b.Snapshot(); len(got) != 1 || got[0] != "z" {
		t.Errorf("buffer not reusable after Clear: %v", got)
	}
}

func TestLogBuffer_SnapshotIsCopy(t *testing.T) {
	b := NewLogBuffer(10)
	b.Append("a")
	snap := b.Snapshot()
	snap[0] = "tampered"
	if got := b.Snapshot(); got[0] != "a" {
		t.Errorf("snapshot mutation leaked: %v", got)
	}
}

func TestLogBuffer_ConcurrentAppend(t *testing.T) {
	b := NewLogBuffer(10000)
	var wg sync.WaitGroup
	const writers = 8
	const each = 500
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				b.Append(fmt.Sprintf("w%d-%d", id, i))
			}
		}(w)
	}
	// Reader running concurrently should never panic.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				_ = b.Snapshot()
			}
		}
	}()
	wg.Wait()
	close(stop)
	if b.Len() != writers*each {
		t.Errorf("len = %d, want %d", b.Len(), writers*each)
	}
}
