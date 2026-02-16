package anticipation

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupTestStore(t *testing.T) *Store {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func TestCreateAndGet(t *testing.T) {
	store := setupTestStore(t)

	afterTime := time.Now().Add(time.Hour)
	a := &Anticipation{
		Description: "Dan's flight arriving",
		Context:     "Check flight status for AA1234. Offer pickup if needed.",
		Trigger: Trigger{
			AfterTime:  &afterTime,
			Zone:       "airport",
			ZoneAction: "enter",
		},
	}

	if err := store.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}

	if a.ID == "" {
		t.Error("expected ID to be set")
	}

	got, err := store.Get(a.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.Description != a.Description {
		t.Errorf("description = %q, want %q", got.Description, a.Description)
	}
	if got.Context != a.Context {
		t.Errorf("context = %q, want %q", got.Context, a.Context)
	}
	if got.Trigger.Zone != "airport" {
		t.Errorf("trigger.zone = %q, want 'airport'", got.Trigger.Zone)
	}
}

func TestActive(t *testing.T) {
	store := setupTestStore(t)

	// Create two active anticipations
	store.Create(&Anticipation{
		Description: "First",
		Context:     "Context 1",
		Trigger:     Trigger{Zone: "home"},
	})
	store.Create(&Anticipation{
		Description: "Second",
		Context:     "Context 2",
		Trigger:     Trigger{Zone: "work"},
	})

	active, err := store.Active()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(active) != 2 {
		t.Errorf("active count = %d, want 2", len(active))
	}
}

func TestResolve(t *testing.T) {
	store := setupTestStore(t)

	a := &Anticipation{
		Description: "Test",
		Context:     "Test context",
		Trigger:     Trigger{Zone: "home"},
	}
	store.Create(a)

	if err := store.Resolve(a.ID); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Should no longer be in active list
	active, _ := store.Active()
	if len(active) != 0 {
		t.Errorf("expected 0 active after resolve, got %d", len(active))
	}

	// But should still be retrievable
	got, _ := store.Get(a.ID)
	if got == nil {
		t.Fatal("expected to still get resolved anticipation")
	}
	if got.ResolvedAt == nil {
		t.Error("expected resolved_at to be set")
	}
}

func TestDelete(t *testing.T) {
	store := setupTestStore(t)

	a := &Anticipation{
		Description: "Test",
		Context:     "Test context",
		Trigger:     Trigger{Zone: "home"},
	}
	store.Create(a)

	if err := store.Delete(a.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Should not be in active list
	active, _ := store.Active()
	if len(active) != 0 {
		t.Errorf("expected 0 active after delete, got %d", len(active))
	}

	// Soft delete means Get returns nil
	got, _ := store.Get(a.ID)
	if got != nil {
		t.Error("expected nil after soft delete")
	}
}

func TestExpiration(t *testing.T) {
	store := setupTestStore(t)

	// Create expired anticipation
	past := time.Now().Add(-time.Hour)
	a := &Anticipation{
		Description: "Expired",
		Context:     "Should not match",
		Trigger:     Trigger{Zone: "home"},
		ExpiresAt:   &past,
	}
	store.Create(a)

	// Should not be in active list
	active, _ := store.Active()
	if len(active) != 0 {
		t.Errorf("expected 0 active (expired), got %d", len(active))
	}
}

func TestMatch_TimeOnly(t *testing.T) {
	store := setupTestStore(t)

	past := time.Now().Add(-time.Hour)
	a := &Anticipation{
		Description: "Past time",
		Context:     "Should match",
		Trigger:     Trigger{AfterTime: &past},
	}
	store.Create(a)

	matched, err := store.Match(WakeContext{Time: time.Now()})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matched) != 1 {
		t.Errorf("expected 1 match, got %d", len(matched))
	}
}

func TestMatch_Zone(t *testing.T) {
	store := setupTestStore(t)

	store.Create(&Anticipation{
		Description: "Airport arrival",
		Context:     "Check flight",
		Trigger:     Trigger{Zone: "airport", ZoneAction: "enter"},
	})

	// Should match
	matched, _ := store.Match(WakeContext{
		Time:       time.Now(),
		Zone:       "airport",
		ZoneAction: "enter",
	})
	if len(matched) != 1 {
		t.Errorf("expected 1 match for airport enter, got %d", len(matched))
	}

	// Wrong action should not match
	matched, _ = store.Match(WakeContext{
		Time:       time.Now(),
		Zone:       "airport",
		ZoneAction: "leave",
	})
	if len(matched) != 0 {
		t.Errorf("expected 0 matches for airport leave, got %d", len(matched))
	}
}

func TestMatch_Entity(t *testing.T) {
	store := setupTestStore(t)

	store.Create(&Anticipation{
		Description: "Dan comes home",
		Context:     "Welcome him",
		Trigger:     Trigger{EntityID: "person.dan", EntityState: "home"},
	})

	// Should match
	matched, _ := store.Match(WakeContext{
		Time:        time.Now(),
		EntityID:    "person.dan",
		EntityState: "home",
	})
	if len(matched) != 1 {
		t.Errorf("expected 1 match, got %d", len(matched))
	}

	// Wrong state should not match
	matched, _ = store.Match(WakeContext{
		Time:        time.Now(),
		EntityID:    "person.dan",
		EntityState: "away",
	})
	if len(matched) != 0 {
		t.Errorf("expected 0 matches for wrong state, got %d", len(matched))
	}
}

func TestMatch_Combined(t *testing.T) {
	store := setupTestStore(t)

	// Anticipation with time AND zone
	past := time.Now().Add(-time.Hour)
	store.Create(&Anticipation{
		Description: "Dan at airport after 2pm",
		Context:     "Flight time",
		Trigger: Trigger{
			AfterTime:  &past,
			Zone:       "airport",
			ZoneAction: "enter",
		},
	})

	// Should match when both conditions met
	matched, _ := store.Match(WakeContext{
		Time:       time.Now(),
		Zone:       "airport",
		ZoneAction: "enter",
	})
	if len(matched) != 1 {
		t.Errorf("expected 1 match, got %d", len(matched))
	}

	// Should not match if time not reached
	future := time.Now().Add(time.Hour)
	store.Create(&Anticipation{
		Description: "Future event",
		Context:     "Not yet",
		Trigger:     Trigger{AfterTime: &future, Zone: "home"},
	})

	matched, _ = store.Match(WakeContext{
		Time: time.Now(),
		Zone: "home",
	})
	// Only the first one should match, not the future one
	if len(matched) != 0 {
		t.Errorf("expected 0 matches (future time), got %d", len(matched))
	}
}

func TestFormatMatchedContext(t *testing.T) {
	matched := []*Anticipation{
		{
			Description: "Dan's flight",
			Context:     "Check AA1234 status",
			CreatedAt:   time.Date(2026, 2, 9, 14, 0, 0, 0, time.UTC),
		},
	}

	output := FormatMatchedContext(matched)

	if output == "" {
		t.Error("expected non-empty output")
	}
	if !contains(output, "Dan's flight") {
		t.Error("expected description in output")
	}
	if !contains(output, "Check AA1234 status") {
		t.Error("expected context in output")
	}
}

func TestContextEntities_RoundTrip(t *testing.T) {
	store := setupTestStore(t)

	entities := []string{"sensor.temperature", "light.kitchen", "binary_sensor.front_door"}
	a := &Anticipation{
		Description:     "Dan arrives home",
		Context:         "Check door and lights",
		ContextEntities: entities,
		Trigger:         Trigger{EntityID: "person.dan", EntityState: "home"},
	}

	if err := store.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := store.Get(a.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if len(got.ContextEntities) != len(entities) {
		t.Fatalf("context_entities length = %d, want %d", len(got.ContextEntities), len(entities))
	}
	for i, want := range entities {
		if got.ContextEntities[i] != want {
			t.Errorf("context_entities[%d] = %q, want %q", i, got.ContextEntities[i], want)
		}
	}
}

func TestContextEntities_Empty(t *testing.T) {
	store := setupTestStore(t)

	a := &Anticipation{
		Description: "No entities",
		Context:     "Plain anticipation",
		Trigger:     Trigger{Zone: "home"},
	}

	if err := store.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := store.Get(a.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if len(got.ContextEntities) != 0 {
		t.Errorf("context_entities = %v, want empty", got.ContextEntities)
	}
}

func TestContextEntities_Active(t *testing.T) {
	store := setupTestStore(t)

	entities := []string{"sensor.temp"}
	store.Create(&Anticipation{
		Description:     "With entities",
		Context:         "Check temp",
		ContextEntities: entities,
		Trigger:         Trigger{EntityID: "person.dan", EntityState: "home"},
	})

	active, err := store.Active()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("active count = %d, want 1", len(active))
	}
	if len(active[0].ContextEntities) != 1 || active[0].ContextEntities[0] != "sensor.temp" {
		t.Errorf("active context_entities = %v, want [sensor.temp]", active[0].ContextEntities)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
