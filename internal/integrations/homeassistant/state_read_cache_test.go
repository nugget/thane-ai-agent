package homeassistant

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// fakeStateReader counts calls and returns a scripted state.
type fakeStateReader struct {
	getStateCalls int
	state         *State
	err           error
}

func (f *fakeStateReader) GetState(_ context.Context, entityID string) (*State, error) {
	f.getStateCalls++
	if f.err != nil {
		return nil, f.err
	}
	s := *f.state
	s.EntityID = entityID
	return &s, nil
}

func (f *fakeStateReader) GetStates(context.Context) ([]State, error) { return nil, nil }
func (f *fakeStateReader) GetStateHistory(context.Context, string, time.Time, time.Time) ([]State, error) {
	return nil, nil
}
func (f *fakeStateReader) GetWeatherForecasts(context.Context, string, string) ([]map[string]any, error) {
	return nil, nil
}

func cacheUnderTest(f *fakeStateReader, at time.Time) (*StateReadCache, *time.Time) {
	c := NewStateReadCache(f)
	now := at
	c.now = func() time.Time { return now }
	return c, &now
}

// TestStateReadCacheReadThroughAndTTL: the first read fetches, reads
// within the TTL serve memory, and expiry falls through and
// repopulates.
func TestStateReadCacheReadThroughAndTTL(t *testing.T) {
	t.Parallel()

	fake := &fakeStateReader{state: &State{State: "22.5", Attributes: map[string]any{"unit": "°C"}}}
	c, now := cacheUnderTest(fake, time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC))

	ctx := context.Background()
	if _, err := c.GetState(ctx, "sensor.office_temp"); err != nil {
		t.Fatalf("GetState: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := c.GetState(ctx, "sensor.office_temp"); err != nil {
			t.Fatalf("cached GetState: %v", err)
		}
	}
	if fake.getStateCalls != 1 {
		t.Fatalf("inner calls = %d, want 1 (reads within TTL serve memory)", fake.getStateCalls)
	}

	*now = now.Add(stateReadCacheTTL + time.Second)
	if _, err := c.GetState(ctx, "sensor.office_temp"); err != nil {
		t.Fatalf("expired GetState: %v", err)
	}
	if fake.getStateCalls != 2 {
		t.Fatalf("inner calls = %d, want 2 (expiry falls through)", fake.getStateCalls)
	}
}

// TestStateReadCacheEventPrimesAndRefreshes: an event-fed entry serves
// without any REST call, and a newer event replaces it.
func TestStateReadCacheEventPrimesAndRefreshes(t *testing.T) {
	t.Parallel()

	fake := &fakeStateReader{state: &State{State: "unused"}}
	c, _ := cacheUnderTest(fake, time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC))

	c.HandleStateChange(StateChangedData{NewState: &State{
		EntityID: "binary_sensor.garage", State: "off",
	}})
	got, err := c.GetState(context.Background(), "binary_sensor.garage")
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if got.State != "off" || fake.getStateCalls != 0 {
		t.Fatalf("event-primed read: state=%q calls=%d, want off/0", got.State, fake.getStateCalls)
	}

	c.HandleStateChange(StateChangedData{NewState: &State{
		EntityID: "binary_sensor.garage", State: "on",
	}})
	got, _ = c.GetState(context.Background(), "binary_sensor.garage")
	if got.State != "on" {
		t.Fatalf("event refresh not served: state=%q, want on", got.State)
	}
}

// TestStateReadCacheClonesEventPayloads: mutating the shared event
// payload after delivery must not corrupt the stored entry.
func TestStateReadCacheClonesEventPayloads(t *testing.T) {
	t.Parallel()

	c, _ := cacheUnderTest(&fakeStateReader{}, time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC))
	shared := &State{EntityID: "light.kitchen", State: "on", Attributes: map[string]any{"brightness": 200}}
	c.HandleStateChange(StateChangedData{NewState: shared})

	shared.State = "corrupted"
	shared.Attributes["brightness"] = -1

	got, err := c.GetState(context.Background(), "light.kitchen")
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if got.State != "on" || got.Attributes["brightness"] != 200 {
		t.Fatalf("stored entry mutated through shared payload: %+v", got)
	}
}

// TestStateReadCacheErrorPassesThrough: a fetch error surfaces
// unchanged and never substitutes a stale entry.
func TestStateReadCacheErrorPassesThrough(t *testing.T) {
	t.Parallel()

	fake := &fakeStateReader{state: &State{State: "ok"}}
	c, now := cacheUnderTest(fake, time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC))

	ctx := context.Background()
	if _, err := c.GetState(ctx, "lock.front"); err != nil {
		t.Fatalf("prime: %v", err)
	}
	*now = now.Add(stateReadCacheTTL + time.Second)
	fake.err = fmt.Errorf("ha unreachable")
	if _, err := c.GetState(ctx, "lock.front"); err == nil {
		t.Fatal("expired read with failing inner must surface the error, not the stale entry")
	}
}

// TestStateReadCacheCapEvictsStalest: at capacity the stalest entry
// makes room; the cap never blocks a fresh store.
func TestStateReadCacheCapEvictsStalest(t *testing.T) {
	t.Parallel()

	c, now := cacheUnderTest(&fakeStateReader{}, time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC))
	for i := 0; i < stateReadCacheMaxEntries; i++ {
		c.HandleStateChange(StateChangedData{NewState: &State{
			EntityID: fmt.Sprintf("sensor.bulk_%d", i), State: "x",
		}})
		*now = now.Add(time.Millisecond)
	}
	c.HandleStateChange(StateChangedData{NewState: &State{EntityID: "sensor.newcomer", State: "y"}})

	c.mu.RLock()
	_, oldest := c.entries["sensor.bulk_0"]
	_, newcomer := c.entries["sensor.newcomer"]
	size := len(c.entries)
	c.mu.RUnlock()
	if oldest {
		t.Error("stalest entry survived the cap eviction")
	}
	if !newcomer {
		t.Error("fresh store blocked by the cap")
	}
	if size > stateReadCacheMaxEntries {
		t.Errorf("cache size %d exceeds cap %d", size, stateReadCacheMaxEntries)
	}
}
