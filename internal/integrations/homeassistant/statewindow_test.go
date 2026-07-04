package homeassistant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
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
	p := NewStateWindowProvider(10, 30*time.Minute, nil, nil)

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestProvider_SingleEntry(t *testing.T) {
	now := time.Date(2025, 6, 15, 14, 30, 0, 0, time.UTC)
	p := NewStateWindowProvider(10, 30*time.Minute, nil, nil)
	p.nowFunc = fixedClock(now)

	p.HandleStateChange("binary_sensor.front_door", "off", "on", "")

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(got, "### Recent State Changes") {
		t.Error("missing header")
	}
	if !strings.Contains(got, `"entity":"binary_sensor.front_door"`) {
		t.Error("missing entity ID in JSON")
	}
	if !strings.Contains(got, `"from":"off"`) {
		t.Error("missing from state in JSON")
	}
	if !strings.Contains(got, `"to":"on"`) {
		t.Error("missing to state in JSON")
	}
	if !strings.Contains(got, `"ago":"-0s"`) {
		t.Errorf("missing delta timestamp in JSON, got:\n%s", got)
	}
}

func TestProvider_MultipleEntries_NewestFirst(t *testing.T) {
	base := time.Date(2025, 6, 15, 14, 0, 0, 0, time.UTC)
	p := NewStateWindowProvider(10, 30*time.Minute, nil, nil)
	clock := advancingClock(base, time.Minute)
	p.nowFunc = clock

	p.HandleStateChange("sensor.temp", "20", "21", "")
	p.HandleStateChange("light.living_room", "on", "off", "")
	p.HandleStateChange("person.nugget", "not_home", "home", "")

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
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
	p := NewStateWindowProvider(3, time.Hour, nil, nil)
	clock := advancingClock(now, time.Minute)
	p.nowFunc = clock

	// Fill buffer with 5 entries; only last 3 should survive.
	p.HandleStateChange("entity.a", "0", "1", "")
	p.HandleStateChange("entity.b", "0", "1", "")
	p.HandleStateChange("entity.c", "0", "1", "")
	p.HandleStateChange("entity.d", "0", "1", "")
	p.HandleStateChange("entity.e", "0", "1", "")

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
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
	p := NewStateWindowProvider(10, 10*time.Minute, nil, nil)

	// Insert an entry 15 minutes ago.
	p.nowFunc = fixedClock(now.Add(-15 * time.Minute))
	p.HandleStateChange("sensor.old", "0", "1", "")

	// Insert a recent entry.
	p.nowFunc = fixedClock(now.Add(-2 * time.Minute))
	p.HandleStateChange("sensor.recent", "0", "1", "")

	// Read at "now" — old entry should be filtered out.
	p.nowFunc = fixedClock(now)
	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
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
	p := NewStateWindowProvider(10, 5*time.Minute, nil, nil)

	// Insert entries 10 minutes ago.
	p.nowFunc = fixedClock(now.Add(-10 * time.Minute))
	p.HandleStateChange("sensor.a", "0", "1", "")
	p.HandleStateChange("sensor.b", "0", "1", "")

	// Read at "now" — all entries expired.
	p.nowFunc = fixedClock(now)
	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty string when all entries expired, got %q", got)
	}
}

func TestProvider_DeltaFormat(t *testing.T) {
	now := time.Date(2025, 6, 15, 14, 30, 0, 0, time.UTC)
	p := NewStateWindowProvider(10, 30*time.Minute, nil, nil)

	// Insert an entry 5 minutes ago.
	p.nowFunc = fixedClock(now.Add(-5 * time.Minute))
	p.HandleStateChange("sensor.test", "a", "b", "")

	// Read at "now" — should show 300 second delta.
	p.nowFunc = fixedClock(now)
	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(got, `"ago":"-300s"`) {
		t.Errorf("expected delta ago:-300s in JSON, got:\n%s", got)
	}
}

func TestProvider_HandleStateChange_Concurrent(t *testing.T) {
	p := NewStateWindowProvider(100, time.Hour, nil, nil)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.HandleStateChange("sensor.concurrent", "0", "1", "")
		}()
	}
	wg.Wait()

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sensor.concurrent") {
		t.Error("expected entries after concurrent writes")
	}
}

func TestProvider_SameStateSuppressed(t *testing.T) {
	now := time.Date(2025, 6, 15, 14, 30, 0, 0, time.UTC)
	p := NewStateWindowProvider(10, 30*time.Minute, nil, nil)
	p.nowFunc = fixedClock(now)

	// Same→same should be filtered.
	p.HandleStateChange("person.nugget", "home", "home", "")
	p.HandleStateChange("sensor.temp", "20", "20", "")

	// Real transition should be recorded.
	p.HandleStateChange("light.office", "off", "on", "")

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: ""})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(got, "person.nugget") {
		t.Error("same→same transition should be filtered")
	}
	if strings.Contains(got, "sensor.temp") {
		t.Error("same→same transition should be filtered")
	}
	if !strings.Contains(got, "light.office") {
		t.Error("real transition should be present")
	}
}

func TestProvider_NilDefaults(t *testing.T) {
	// Verify constructor handles zero/nil values gracefully.
	p := NewStateWindowProvider(0, 0, nil, nil)
	if len(p.entries) != 50 {
		t.Errorf("expected default maxEntries=50, got %d", len(p.entries))
	}
	if p.maxAge != 30*time.Minute {
		t.Errorf("expected default maxAge=30m, got %v", p.maxAge)
	}
	if p.translate != nil {
		t.Error("expected nil translate to stay nil (raw passthrough)")
	}
}

// With a semantic translator wired, transitions render class-aware
// labels; without one, raw states pass through. The real vocabulary is
// asserted end-to-end in statewindow_semantic_test.go (external package,
// since contextfmt imports this one).
func TestStateWindow_SemanticTranslatorApplied(t *testing.T) {
	p := NewStateWindowProvider(10, 30*time.Minute, func(domain, deviceClass, state string) string {
		if domain == "binary_sensor" && deviceClass == "garage_door" {
			switch state {
			case "on":
				return "open"
			case "off":
				return "closed"
			}
		}
		return state
	}, nil)

	p.HandleStateChange("binary_sensor.garage", "off", "on", "garage_door")
	p.HandleStateChange("sensor.temp", "20", "21", "temperature")

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if !strings.Contains(got, `"from":"closed"`) || !strings.Contains(got, `"to":"open"`) {
		t.Errorf("garage transition not translated:\n%s", got)
	}
	if !strings.Contains(got, `"from":"20"`) || !strings.Contains(got, `"to":"21"`) {
		t.Errorf("numeric transition should pass through:\n%s", got)
	}
}

// TestRecentTransitions covers the per-entity retention rings behind
// the shared window (#1210): newest-first ordering, class-aware
// translation, window filtering, limit clamping with an honest
// matched count, and isolation between entities.
func TestRecentTransitions(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	clock := now
	translate := func(domain, deviceClass, state string) string {
		if domain == "binary_sensor" && deviceClass == "garage_door" {
			switch state {
			case "on":
				return "open"
			case "off":
				return "closed"
			}
		}
		return state
	}
	p := NewStateWindowProvider(4, 30*time.Minute, translate, nil)
	p.nowFunc = func() time.Time { return clock }

	// Three garage transitions a minute apart, plus noise on another
	// entity that must not bleed in — and enough shared-window churn
	// (buffer cap 4) that the garage's oldest change is long gone from
	// the shared ring.
	for i := 0; i < 3; i++ {
		p.HandleStateChange("binary_sensor.garage_bay_3", "off", "on", "garage_door")
		clock = clock.Add(30 * time.Second)
		p.HandleStateChange("binary_sensor.garage_bay_3", "on", "off", "garage_door")
		clock = clock.Add(30 * time.Second)
		p.HandleStateChange("sensor.noise", "1", "2", "")
		p.HandleStateChange("sensor.noise", "2", "1", "")
	}

	transitions, matched := p.RecentTransitions("binary_sensor.garage_bay_3", 0, 0)
	if matched != 6 || len(transitions) != 6 {
		t.Fatalf("matched=%d len=%d, want 6/6 despite shared-window churn", matched, len(transitions))
	}
	// Newest first, class-aware labels.
	if transitions[0].From != "open" || transitions[0].To != "closed" {
		t.Errorf("newest = %s→%s, want open→closed", transitions[0].From, transitions[0].To)
	}
	if !transitions[0].At.After(transitions[5].At) {
		t.Error("transitions not newest-first")
	}

	// Limit clamps the slice but matched stays honest.
	limited, matchedLimited := p.RecentTransitions("binary_sensor.garage_bay_3", 2, 0)
	if len(limited) != 2 || matchedLimited != 6 {
		t.Errorf("limited len=%d matched=%d, want 2/6", len(limited), matchedLimited)
	}

	// Window filter: only changes in the last 61 seconds (the final
	// on/off pair plus margin).
	windowed, matchedWindowed := p.RecentTransitions("binary_sensor.garage_bay_3", 0, 61*time.Second)
	if matchedWindowed != 2 || len(windowed) != 2 {
		t.Errorf("windowed len=%d matched=%d, want 2/2", len(windowed), matchedWindowed)
	}

	// Unknown entity: empty, zero.
	if tr, m := p.RecentTransitions("sensor.unknown", 0, 0); len(tr) != 0 || m != 0 {
		t.Errorf("unknown entity = %v/%d, want empty", tr, m)
	}
}

// TestRecentTransitionsPerEntityBound verifies the ring cap: the
// oldest transitions fall off once an entity exceeds
// perEntityTransitionCap retained changes.
func TestRecentTransitionsPerEntityBound(t *testing.T) {
	clock := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	p := NewStateWindowProvider(4, 30*time.Minute, nil, nil)
	p.nowFunc = func() time.Time { return clock }

	for i := 0; i < perEntityTransitionCap+8; i++ {
		p.HandleStateChange("sensor.busy", fmt.Sprintf("%d", i), fmt.Sprintf("%d", i+1), "")
		clock = clock.Add(time.Second)
	}
	transitions, matched := p.RecentTransitions("sensor.busy", 0, 0)
	if matched != perEntityTransitionCap || len(transitions) != perEntityTransitionCap {
		t.Fatalf("matched=%d len=%d, want ring cap %d", matched, len(transitions), perEntityTransitionCap)
	}
	// Newest retained change is the last write.
	if transitions[0].To != fmt.Sprintf("%d", perEntityTransitionCap+8) {
		t.Errorf("newest.To = %s, want the final write", transitions[0].To)
	}
}

// TestRecentTransitionsEvictsLeastRecentlyUpdated verifies the
// tracked-entity backstop: crossing maxTrackedEntities evicts the
// entity whose ring was written longest ago.
func TestRecentTransitionsEvictsLeastRecentlyUpdated(t *testing.T) {
	clock := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	p := NewStateWindowProvider(4, 30*time.Minute, nil, nil)
	p.nowFunc = func() time.Time { return clock }

	p.HandleStateChange("sensor.stale", "a", "b", "")
	for i := 0; i < maxTrackedEntities; i++ {
		clock = clock.Add(time.Second)
		p.HandleStateChange(fmt.Sprintf("sensor.fresh_%d", i), "a", "b", "")
	}

	if _, matched := p.RecentTransitions("sensor.stale", 0, 0); matched != 0 {
		t.Errorf("stale entity survived eviction with %d retained changes", matched)
	}
	if _, matched := p.RecentTransitions(fmt.Sprintf("sensor.fresh_%d", maxTrackedEntities-1), 0, 0); matched != 1 {
		t.Errorf("freshest entity missing after eviction pass: %d", matched)
	}
}
