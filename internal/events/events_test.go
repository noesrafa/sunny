package events

import (
	"sync"
	"testing"
	"time"
)

func drain(ch <-chan Event, n int, timeout time.Duration) []Event {
	var got []Event
	deadline := time.After(timeout)
	for len(got) < n {
		select {
		case ev, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, ev)
		case <-deadline:
			return got
		}
	}
	return got
}

func TestSubscribePublish(t *testing.T) {
	h := New(nil)
	ch, cancel := h.Subscribe()
	defer cancel()

	want := Event{Type: AgentCreated, AgentID: "alpha"}
	h.Publish(want)
	got := drain(ch, 1, 100*time.Millisecond)
	if len(got) != 1 || got[0] != want {
		t.Errorf("got %+v, want [%+v]", got, want)
	}
}

func TestMultipleSubscribers(t *testing.T) {
	h := New(nil)
	ch1, c1 := h.Subscribe()
	ch2, c2 := h.Subscribe()
	defer c1()
	defer c2()

	h.Publish(Event{Type: AgentCreated, AgentID: "alpha"})
	h.Publish(Event{Type: AgentDeleted, AgentID: "beta"})

	for i, ch := range []<-chan Event{ch1, ch2} {
		got := drain(ch, 2, 100*time.Millisecond)
		if len(got) != 2 {
			t.Errorf("sub %d got %d events, want 2", i, len(got))
		}
	}
}

func TestCancel_RemovesSubscription(t *testing.T) {
	h := New(nil)
	_, cancel := h.Subscribe()
	if h.SubCount() != 1 {
		t.Fatalf("SubCount before cancel = %d", h.SubCount())
	}
	cancel()
	if h.SubCount() != 0 {
		t.Errorf("SubCount after cancel = %d, want 0", h.SubCount())
	}
	// Cancel again should be a no-op (not panic, not double-close).
	cancel()
}

func TestPublish_NonBlockingOnSlowSubscriber(t *testing.T) {
	h := New(nil)
	_, cancel := h.Subscribe() // never read
	defer cancel()

	// Push 200 events. Buffer is 64; once full we should drop, not
	// block. If this test hangs, Publish blocked.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			h.Publish(Event{Type: AgentUpdated, AgentID: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on slow subscriber")
	}
}

func TestClose_StopsFurtherPublish(t *testing.T) {
	h := New(nil)
	ch, cancel := h.Subscribe()
	defer cancel()
	h.Close()
	if _, ok := <-ch; ok {
		t.Errorf("channel should be closed after Hub.Close")
	}
	// After close, Publish must not panic and must not deliver.
	h.Publish(Event{Type: AgentCreated})
}

func TestClose_AfterClose_SubscribeReturnsClosed(t *testing.T) {
	h := New(nil)
	h.Close()
	ch, _ := h.Subscribe()
	if _, ok := <-ch; ok {
		t.Errorf("Subscribe after Close should return closed channel")
	}
}

func TestConcurrentPublish(t *testing.T) {
	h := New(nil)
	ch, cancel := h.Subscribe()
	defer cancel()

	const goroutines = 8
	const each = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				h.Publish(Event{Type: AgentUpdated, AgentID: "x"})
			}
		}()
	}
	wg.Wait()

	// Drain whatever fit in the 64-slot buffer. We don't assert the
	// exact count (drops are expected with goroutines * each = 400
	// events vs 64-slot buffer), only that we got *some* and nothing
	// hung.
	got := drain(ch, 64, 200*time.Millisecond)
	if len(got) == 0 {
		t.Errorf("got 0 events under concurrent publish, want >= 1")
	}
}
