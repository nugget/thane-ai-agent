package statewindow

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixedClock returns a nowFunc that returns a fixed time.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// advancingClock returns a nowFunc that advances by step on each call,
// starting from start.
func advancingClock(start time.Time, step time.Duration) func() time.Time {
	current := start
	var mu sync.Mutex
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t := current
		current = current.Add(step)
		return t
	}
}

func TestProvider_EmptyBuffer(t *testing.T) {
	p := NewProvider(10, 30*time.Minute, time.UTC, nil)

	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestProvider_SingleEntry(t *testing.T) {
	now := time.Date(2025, 6, 15, 14, 30, 0, 0, time.UTC)
	p := NewProvider(10, 30*time.Minute, time.UTC, nil)
	p.nowFunc = fixedClock(now)

	p.HandleStateChange("binary_sensor.front_door", "off", "on")

	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(got, "### Recent State Changes") {
		t.Error("missing header")
	}
	if !strings.Contains(got, "binary_sensor.front_door") {
		t.Error("missing entity ID")
	}
	if !strings.Contains(got, "off → on") {
		t.Error("missing state transition")
	}
	if !strings.Contains(got, "2025-06-15T14:30:00Z") {
		t.Errorf("missing ISO 8601 timestamp, got:\n%s", got)
	}
}

func TestProvider_MultipleEntries_NewestFirst(t *testing.T) {
	base := time.Date(2025, 6, 15, 14, 0, 0, 0, time.UTC)
	p := NewProvider(10, 30*time.Minute, time.UTC, nil)
	clock := advancingClock(base, time.Minute)
	p.nowFunc = clock

	p.HandleStateChange("sensor.temp", "20", "21")
	p.HandleStateChange("light.living_room", "on", "off")
	p.HandleStateChange("person.nugget", "not_home", "home")

	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}

	// Verify newest first: person.nugget should appear before sensor.temp.
	nuggetIdx := strings.Index(got, "person.nugget")
	tempIdx := strings.Index(got, "sensor.temp")
	if nuggetIdx < 0 || tempIdx < 0 {
		t.Fatalf("missing expected entries:\n%s", got)
	}
	if nuggetIdx > tempIdx {
		t.Errorf("entries not in newest-first order:\n%s", got)
	}
}

func TestProvider_CircularEviction(t *testing.T) {
	now := time.Date(2025, 6, 15, 14, 0, 0, 0, time.UTC)
	p := NewProvider(3, time.Hour, time.UTC, nil)
	clock := advancingClock(now, time.Minute)
	p.nowFunc = clock

	// Fill buffer with 5 entries; only last 3 should survive.
	p.HandleStateChange("entity.a", "0", "1")
	p.HandleStateChange("entity.b", "0", "1")
	p.HandleStateChange("entity.c", "0", "1")
	p.HandleStateChange("entity.d", "0", "1")
	p.HandleStateChange("entity.e", "0", "1")

	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}

	// entity.a and entity.b should be evicted.
	if strings.Contains(got, "entity.a") {
		t.Error("entity.a should have been evicted")
	}
	if strings.Contains(got, "entity.b") {
		t.Error("entity.b should have been evicted")
	}
	// entity.c, d, e should remain.
	for _, id := range []string{"entity.c", "entity.d", "entity.e"} {
		if !strings.Contains(got, id) {
			t.Errorf("expected %s to be present", id)
		}
	}
}

func TestProvider_AgeEviction(t *testing.T) {
	now := time.Date(2025, 6, 15, 14, 0, 0, 0, time.UTC)
	p := NewProvider(10, 10*time.Minute, time.UTC, nil)

	// Insert an entry 15 minutes ago.
	p.nowFunc = fixedClock(now.Add(-15 * time.Minute))
	p.HandleStateChange("sensor.old", "0", "1")

	// Insert a recent entry.
	p.nowFunc = fixedClock(now.Add(-2 * time.Minute))
	p.HandleStateChange("sensor.recent", "0", "1")

	// Read at "now" — old entry should be filtered out.
	p.nowFunc = fixedClock(now)
	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(got, "sensor.old") {
		t.Error("sensor.old should be filtered by age")
	}
	if !strings.Contains(got, "sensor.recent") {
		t.Error("sensor.recent should be present")
	}
}

func TestProvider_AllExpired(t *testing.T) {
	now := time.Date(2025, 6, 15, 14, 0, 0, 0, time.UTC)
	p := NewProvider(10, 5*time.Minute, time.UTC, nil)

	// Insert entries 10 minutes ago.
	p.nowFunc = fixedClock(now.Add(-10 * time.Minute))
	p.HandleStateChange("sensor.a", "0", "1")
	p.HandleStateChange("sensor.b", "0", "1")

	// Read at "now" — all entries expired.
	p.nowFunc = fixedClock(now)
	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty string when all entries expired, got %q", got)
	}
}

func TestProvider_ISO8601_Timezone(t *testing.T) {
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skip("timezone America/Chicago not available")
	}

	now := time.Date(2025, 6, 15, 14, 30, 0, 0, time.UTC)
	p := NewProvider(10, 30*time.Minute, loc, nil)
	p.nowFunc = fixedClock(now)

	p.HandleStateChange("sensor.test", "a", "b")

	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}

	// 14:30 UTC = 09:30 CDT (UTC-5)
	if !strings.Contains(got, "2025-06-15T09:30:00-05:00") {
		t.Errorf("expected Chicago timezone timestamp, got:\n%s", got)
	}
}

func TestProvider_HandleStateChange_Concurrent(t *testing.T) {
	p := NewProvider(100, time.Hour, time.UTC, nil)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.HandleStateChange("sensor.concurrent", "0", "1")
		}()
	}
	wg.Wait()

	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sensor.concurrent") {
		t.Error("expected entries after concurrent writes")
	}
}

func TestProvider_NilDefaults(t *testing.T) {
	// Verify constructor handles zero/nil values gracefully.
	p := NewProvider(0, 0, nil, nil)
	if len(p.entries) != 50 {
		t.Errorf("expected default maxEntries=50, got %d", len(p.entries))
	}
	if p.maxAge != 30*time.Minute {
		t.Errorf("expected default maxAge=30m, got %v", p.maxAge)
	}
	if p.loc != time.Local {
		t.Error("expected time.Local as default location")
	}
}
