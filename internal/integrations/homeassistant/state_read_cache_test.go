package homeassistant

import (
	"context"
	"fmt"
	"sync"
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

// gatedStateReader blocks GetState until the gate opens, counting calls.
type gatedStateReader struct {
	mu    sync.Mutex
	calls int
	gate  chan struct{}
	state State
}

func (g *gatedStateReader) GetState(_ context.Context, entityID string) (*State, error) {
	g.mu.Lock()
	g.calls++
	g.mu.Unlock()
	<-g.gate
	s := g.state
	s.EntityID = entityID
	return &s, nil
}

func (g *gatedStateReader) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func (g *gatedStateReader) GetStates(context.Context) ([]State, error) { return nil, nil }
func (g *gatedStateReader) GetStateHistory(context.Context, string, time.Time, time.Time) ([]State, error) {
	return nil, nil
}
func (g *gatedStateReader) GetWeatherForecasts(context.Context, string, string) ([]map[string]any, error) {
	return nil, nil
}

// testClock is a lock-guarded clock for tests whose fetch goroutines
// read it concurrently with the test advancing it.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestStateReadCacheCoalescesConcurrentMisses: at a TTL boundary every
// loop misses together; exactly one fetch must serve them all, or the
// cache recreates the fan-out it exists to remove.
func TestStateReadCacheCoalescesConcurrentMisses(t *testing.T) {
	t.Parallel()

	gated := &gatedStateReader{gate: make(chan struct{}), state: State{State: "22.5"}}
	c := NewStateReadCache(gated)

	const readers = 8
	var wg sync.WaitGroup
	errs := make([]error, readers)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = c.GetState(context.Background(), "sensor.shared")
		}(i)
	}

	waitFor(t, "the leader to reach the reader", func() bool { return gated.callCount() >= 1 })
	close(gated.gate)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("reader %d: %v", i, err)
		}
	}
	if got := gated.callCount(); got != 1 {
		t.Fatalf("inner calls = %d, want 1 (concurrent misses must coalesce)", got)
	}
}

// TestStateReadCacheWaiterCancellationDoesNotPoisonFetch: an abandoning
// caller gets its ctx error, while the shared fetch completes on its
// detached bound and repopulates for everyone else.
func TestStateReadCacheWaiterCancellationDoesNotPoisonFetch(t *testing.T) {
	t.Parallel()

	gated := &gatedStateReader{gate: make(chan struct{}), state: State{State: "locked"}}
	c := NewStateReadCache(gated)

	leaderDone := make(chan error, 1)
	go func() {
		_, err := c.GetState(context.Background(), "lock.front")
		leaderDone <- err
	}()
	waitFor(t, "the leader to reach the reader", func() bool { return gated.callCount() >= 1 })

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.GetState(canceled, "lock.front"); err == nil {
		t.Fatal("canceled waiter must return its ctx error, not block")
	}

	close(gated.gate)
	if err := <-leaderDone; err != nil {
		t.Fatalf("leader: %v", err)
	}
	got, err := c.GetState(context.Background(), "lock.front")
	if err != nil || got.State != "locked" {
		t.Fatalf("cache not populated after waiter cancellation: %v %v", got, err)
	}
	if gated.callCount() != 1 {
		t.Fatalf("inner calls = %d, want 1", gated.callCount())
	}
}

// TestStateReadCacheEventDuringFetchWins: a state_changed event that
// lands while a REST fetch is in flight is newer truth; the late fetch
// completion must not roll the cache back over it.
func TestStateReadCacheEventDuringFetchWins(t *testing.T) {
	t.Parallel()

	gated := &gatedStateReader{gate: make(chan struct{}), state: State{State: "rest-stale"}}
	c := NewStateReadCache(gated)
	clk := &testClock{t: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)}
	c.now = clk.Now

	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		_, _ = c.GetState(context.Background(), "binary_sensor.door")
	}()
	waitFor(t, "the leader to reach the reader", func() bool { return gated.callCount() >= 1 })

	clk.Set(clk.Now().Add(time.Second))
	c.HandleStateChange(StateChangedData{NewState: &State{EntityID: "binary_sensor.door", State: "open"}})
	close(gated.gate)
	<-leaderDone

	got, err := c.GetState(context.Background(), "binary_sensor.door")
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if got.State != "open" {
		t.Fatalf("cache = %q, want the mid-flight event value %q", got.State, "open")
	}
}

// TestStateReadCacheCapEvictionSeedsFromMap: an insert stamped older
// than every existing entry must still make room for itself — the
// eviction candidate seeds from the map, not the incoming timestamp.
func TestStateReadCacheCapEvictionSeedsFromMap(t *testing.T) {
	t.Parallel()

	c := NewStateReadCache(&fakeStateReader{})
	clk := &testClock{t: time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC)}
	c.now = clk.Now

	for i := 0; i < stateReadCacheMaxEntries; i++ {
		c.HandleStateChange(StateChangedData{NewState: &State{
			EntityID: fmt.Sprintf("sensor.future_%d", i), State: "x",
		}})
	}

	// The straggler is stamped an hour earlier than everything cached.
	clk.Set(clk.Now().Add(-time.Hour))
	c.HandleStateChange(StateChangedData{NewState: &State{EntityID: "sensor.straggler", State: "y"}})

	c.mu.RLock()
	_, present := c.entries["sensor.straggler"]
	size := len(c.entries)
	c.mu.RUnlock()
	if !present {
		t.Fatal("old-stamped insert was refused instead of evicting the stalest entry")
	}
	if size > stateReadCacheMaxEntries {
		t.Fatalf("cache size %d exceeds cap %d", size, stateReadCacheMaxEntries)
	}
}
