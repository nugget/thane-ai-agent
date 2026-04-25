package events

import (
	"sync"
	"testing"
	"time"
)

func TestNilBusPublish(t *testing.T) {
	var b *Bus
	// Must not panic.
	b.Publish(Event{Source: SourceAgent, Kind: KindRequestStart})
}

func TestNilBusSubscriberCount(t *testing.T) {
	var b *Bus
	if got := b.SubscriberCount(); got != 0 {
		t.Errorf("SubscriberCount() on nil bus = %d, want 0", got)
	}
}

func TestPublishSingleSubscriber(t *testing.T) {
	b := New()
	ch := b.Subscribe(8)
	defer b.Unsubscribe(ch)

	want := Event{
		Timestamp: time.Now(),
		Source:    SourceAgent,
		Kind:      KindRequestStart,
		Data:      map[string]any{"request_id": "r_abc"},
	}
	b.Publish(want)

	select {
	case got := <-ch:
		if got.Source != want.Source || got.Kind != want.Kind {
			t.Errorf("got event %v, want %v", got, want)
		}
		reqID, ok := got.Data["request_id"].(string)
		if !ok || reqID != "r_abc" {
			t.Errorf("got request_id %v, want %q", got.Data["request_id"], "r_abc")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestPublishMultipleSubscribers(t *testing.T) {
	b := New()
	const n = 5
	channels := make([]<-chan Event, n)
	for i := range n {
		channels[i] = b.Subscribe(8)
	}
	defer func() {
		for _, ch := range channels {
			b.Unsubscribe(ch)
		}
	}()

	evt := Event{Source: SourceSignal, Kind: KindMessageReceived}
	b.Publish(evt)

	for i, ch := range channels {
		select {
		case got := <-ch:
			if got.Source != evt.Source || got.Kind != evt.Kind {
				t.Errorf("subscriber %d: got %v, want %v", i, got, evt)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out", i)
		}
	}
}

func TestDropOnFull(t *testing.T) {
	b := New()
	// Buffer size 1 — second publish should be dropped.
	ch := b.Subscribe(1)
	defer b.Unsubscribe(ch)

	b.Publish(Event{Kind: "first"})
	b.Publish(Event{Kind: "second"})

	got := <-ch
	if got.Kind != "first" {
		t.Errorf("got kind %q, want %q", got.Kind, "first")
	}

	// Channel should be empty — the second event was dropped.
	select {
	case evt := <-ch:
		t.Errorf("expected empty channel, got event %v", evt)
	default:
		// Correct — channel is empty.
	}
}

func TestUnsubscribeClosesChannel(t *testing.T) {
	b := New()
	ch := b.Subscribe(8)

	b.Unsubscribe(ch)

	// Reading from a closed channel returns the zero value immediately.
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed after Unsubscribe")
	}
}

func TestDoubleUnsubscribe(t *testing.T) {
	b := New()
	ch := b.Subscribe(8)

	b.Unsubscribe(ch)
	// Must not panic.
	b.Unsubscribe(ch)
}

func TestSubscriberCount(t *testing.T) {
	b := New()

	if got := b.SubscriberCount(); got != 0 {
		t.Errorf("initial count = %d, want 0", got)
	}

	ch1 := b.Subscribe(4)
	ch2 := b.Subscribe(4)

	if got := b.SubscriberCount(); got != 2 {
		t.Errorf("after 2 subscribes = %d, want 2", got)
	}

	b.Unsubscribe(ch1)
	if got := b.SubscriberCount(); got != 1 {
		t.Errorf("after 1 unsubscribe = %d, want 1", got)
	}

	b.Unsubscribe(ch2)
	if got := b.SubscriberCount(); got != 0 {
		t.Errorf("after all unsubscribed = %d, want 0", got)
	}
}

func TestConcurrentPublishSubscribe(t *testing.T) {
	b := New()
	const publishers = 10
	const eventsPerPublisher = 100

	var wg sync.WaitGroup

	// Start a subscriber that drains events.
	ch := b.Subscribe(64)
	wg.Add(1)
	go func() {
		defer wg.Done()
		count := 0
		for range ch {
			count++
			// We don't assert exact count because drops are expected.
		}
	}()

	// Launch concurrent publishers.
	var pubWg sync.WaitGroup
	for i := range publishers {
		pubWg.Add(1)
		go func() {
			defer pubWg.Done()
			for j := range eventsPerPublisher {
				b.Publish(Event{
					Timestamp: time.Now(),
					Source:    SourceAgent,
					Kind:      KindToolCall,
					Data:      map[string]any{"publisher": i, "seq": j},
				})
			}
		}()
	}

	pubWg.Wait()
	b.Unsubscribe(ch) // Closes the channel, ending the draining goroutine.
	wg.Wait()
}

func TestPublishNoSubscribers(t *testing.T) {
	b := New()
	// Must not panic when publishing with no subscribers.
	b.Publish(Event{Source: SourceScheduler, Kind: KindTaskFired})
}

func TestPublishAfterUnsubscribe(t *testing.T) {
	b := New()
	ch := b.Subscribe(8)
	b.Unsubscribe(ch)

	// Publishing after the only subscriber is gone must not panic.
	b.Publish(Event{Source: SourceEmail, Kind: KindPollStart})
}
