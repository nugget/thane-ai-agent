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

func TestExpiration_TimezoneNormalization(t *testing.T) {
	store := setupTestStore(t)

	// Simulate the real-world bug: create an anticipation with ExpiresAt
	// in a non-UTC timezone (e.g., CST = UTC-6). Before the fix, SQLite's
	// string comparison would see "16:07" < "22:07" (CURRENT_TIMESTAMP in
	// UTC) and incorrectly mark the anticipation as expired even though
	// the offset-adjusted time is still in the future.
	loc := time.FixedZone("CST", -6*3600)
	future := time.Now().In(loc).Add(2 * time.Hour)
	a := &Anticipation{
		Description: "Future expiry in non-UTC timezone",
		Context:     "Should still be active",
		Trigger:     Trigger{Zone: "home"},
		ExpiresAt:   &future,
	}
	if err := store.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Verify ExpiresAt was normalized to UTC by Create.
	got, err := store.Get(a.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ExpiresAt == nil {
		t.Fatal("expected non-nil ExpiresAt")
	}
	if got.ExpiresAt.Location() != time.UTC {
		t.Errorf("ExpiresAt location = %v, want UTC", got.ExpiresAt.Location())
	}

	// The anticipation is 2 hours in the future — must appear in Active().
	active, err := store.Active()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(active) != 1 {
		t.Errorf("expected 1 active anticipation, got %d", len(active))
	}
}

func TestCreate_NormalizesTimestampsToUTC(t *testing.T) {
	store := setupTestStore(t)

	loc := time.FixedZone("EST", -5*3600)
	localNow := time.Now().In(loc)
	future := localNow.Add(time.Hour)

	a := &Anticipation{
		Description: "UTC normalization check",
		Context:     "Test",
		Trigger:     Trigger{Zone: "work"},
		CreatedAt:   localNow,
		ExpiresAt:   &future,
	}
	if err := store.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Verify the struct was mutated to UTC.
	if a.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt location = %v, want UTC", a.CreatedAt.Location())
	}
	if a.ExpiresAt.Location() != time.UTC {
		t.Errorf("ExpiresAt location = %v, want UTC", a.ExpiresAt.Location())
	}

	// Round-trip through the database should also be UTC.
	got, err := store.Get(a.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ExpiresAt == nil {
		t.Fatal("expected non-nil ExpiresAt")
	}
	if got.ExpiresAt.Location() != time.UTC {
		t.Errorf("retrieved ExpiresAt location = %v, want UTC", got.ExpiresAt.Location())
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

func TestRecurring_RoundTrip(t *testing.T) {
	store := setupTestStore(t)

	tests := []struct {
		name      string
		recurring bool
	}{
		{"recurring true", true},
		{"recurring false", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Anticipation{
				Description: "Test recurring " + tt.name,
				Context:     "Test context",
				Recurring:   tt.recurring,
				Trigger:     Trigger{EntityID: "person.dan", EntityState: "home"},
			}
			if err := store.Create(a); err != nil {
				t.Fatalf("create: %v", err)
			}

			got, err := store.Get(a.ID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got.Recurring != tt.recurring {
				t.Errorf("recurring = %v, want %v", got.Recurring, tt.recurring)
			}
		})
	}
}

func TestRecurring_Active(t *testing.T) {
	store := setupTestStore(t)

	store.Create(&Anticipation{
		Description: "One-shot",
		Context:     "Once",
		Recurring:   false,
		Trigger:     Trigger{EntityID: "sensor.a"},
	})
	store.Create(&Anticipation{
		Description: "Recurring",
		Context:     "Forever",
		Recurring:   true,
		Trigger:     Trigger{EntityID: "sensor.b"},
	})

	active, err := store.Active()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("active count = %d, want 2", len(active))
	}

	// First should be one-shot (created first).
	if active[0].Recurring {
		t.Error("first anticipation should not be recurring")
	}
	if !active[1].Recurring {
		t.Error("second anticipation should be recurring")
	}
}

func TestCooldownSeconds_RoundTrip(t *testing.T) {
	store := setupTestStore(t)

	tests := []struct {
		name     string
		cooldown int
	}{
		{"zero (global default)", 0},
		{"30 seconds", 30},
		{"300 seconds (5m)", 300},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Anticipation{
				Description:     "Cooldown test " + tt.name,
				Context:         "Test context",
				CooldownSeconds: tt.cooldown,
				Recurring:       true,
				Trigger:         Trigger{EntityID: "sensor.test"},
			}
			if err := store.Create(a); err != nil {
				t.Fatalf("create: %v", err)
			}

			got, err := store.Get(a.ID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got.CooldownSeconds != tt.cooldown {
				t.Errorf("cooldown_seconds = %d, want %d", got.CooldownSeconds, tt.cooldown)
			}
		})
	}
}

func TestCooldownSeconds_Active(t *testing.T) {
	store := setupTestStore(t)

	store.Create(&Anticipation{
		Description:     "With cooldown",
		Context:         "Check",
		CooldownSeconds: 60,
		Recurring:       true,
		Trigger:         Trigger{EntityID: "sensor.a"},
	})

	active, err := store.Active()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("active count = %d, want 1", len(active))
	}
	if active[0].CooldownSeconds != 60 {
		t.Errorf("active cooldown_seconds = %d, want 60", active[0].CooldownSeconds)
	}
}

func TestMarkFired_SetsLastFiredAt(t *testing.T) {
	store := setupTestStore(t)

	a := &Anticipation{
		Description: "Fire test",
		Context:     "Test",
		Trigger:     Trigger{EntityID: "sensor.a"},
	}
	if err := store.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Before firing, LastFiredAt should be nil.
	got, _ := store.Get(a.ID)
	if got.LastFiredAt != nil {
		t.Error("expected nil LastFiredAt before MarkFired")
	}

	if err := store.MarkFired(a.ID); err != nil {
		t.Fatalf("mark fired: %v", err)
	}

	got, _ = store.Get(a.ID)
	if got.LastFiredAt == nil {
		t.Fatal("expected non-nil LastFiredAt after MarkFired")
	}
	if time.Since(*got.LastFiredAt) > 5*time.Second {
		t.Errorf("LastFiredAt too old: %v", got.LastFiredAt)
	}
}

func TestOnCooldown_NeverFired(t *testing.T) {
	store := setupTestStore(t)

	a := &Anticipation{
		Description: "Never fired",
		Context:     "Test",
		Trigger:     Trigger{EntityID: "sensor.a"},
	}
	store.Create(a)

	onCooldown, err := store.OnCooldown(a.ID, 5*time.Minute)
	if err != nil {
		t.Fatalf("OnCooldown: %v", err)
	}
	if onCooldown {
		t.Error("expected not on cooldown when never fired")
	}
}

func TestOnCooldown_RecentlyFired(t *testing.T) {
	store := setupTestStore(t)

	a := &Anticipation{
		Description: "Recently fired",
		Context:     "Test",
		Trigger:     Trigger{EntityID: "sensor.a"},
	}
	store.Create(a)
	store.MarkFired(a.ID)

	// Just fired — should be on cooldown with a 5-minute window.
	onCooldown, err := store.OnCooldown(a.ID, 5*time.Minute)
	if err != nil {
		t.Fatalf("OnCooldown: %v", err)
	}
	if !onCooldown {
		t.Error("expected on cooldown immediately after MarkFired")
	}
}

func TestOnCooldown_PerAnticipationOverridesGlobal(t *testing.T) {
	store := setupTestStore(t)

	a := &Anticipation{
		Description:     "Per-anticipation cooldown",
		Context:         "Test",
		CooldownSeconds: 3600, // 1 hour
		Recurring:       true,
		Trigger:         Trigger{EntityID: "sensor.a"},
	}
	store.Create(a)
	store.MarkFired(a.ID)

	// Global default is 1 second (would have expired), but per-anticipation
	// is 1 hour — should still be on cooldown.
	onCooldown, err := store.OnCooldown(a.ID, 1*time.Second)
	if err != nil {
		t.Fatalf("OnCooldown: %v", err)
	}
	if !onCooldown {
		t.Error("expected on cooldown — per-anticipation 1h should override 1s global")
	}
}

func TestOnCooldown_ZeroCooldownUsesGlobal(t *testing.T) {
	store := setupTestStore(t)

	a := &Anticipation{
		Description:     "Global fallback",
		Context:         "Test",
		CooldownSeconds: 0, // use global
		Trigger:         Trigger{EntityID: "sensor.a"},
	}
	store.Create(a)
	store.MarkFired(a.ID)

	// Global default is 1 hour — should be on cooldown.
	onCooldown, err := store.OnCooldown(a.ID, time.Hour)
	if err != nil {
		t.Fatalf("OnCooldown: %v", err)
	}
	if !onCooldown {
		t.Error("expected on cooldown with 0 cooldown_seconds and 1h global default")
	}
}

func TestOnCooldown_NonExistentID(t *testing.T) {
	store := setupTestStore(t)

	onCooldown, err := store.OnCooldown("nonexistent", 5*time.Minute)
	if err != nil {
		t.Fatalf("OnCooldown: %v", err)
	}
	if onCooldown {
		t.Error("expected not on cooldown for nonexistent ID")
	}
}

func TestLastFiredAt_Active(t *testing.T) {
	store := setupTestStore(t)

	a := &Anticipation{
		Description: "Track firing",
		Context:     "Test",
		Trigger:     Trigger{EntityID: "sensor.a"},
	}
	store.Create(a)
	store.MarkFired(a.ID)

	active, err := store.Active()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("active count = %d, want 1", len(active))
	}
	if active[0].LastFiredAt == nil {
		t.Error("expected LastFiredAt in Active() results")
	}
}

func TestRoutingHints_RoundTrip(t *testing.T) {
	store := setupTestStore(t)

	localOnly := false
	a := &Anticipation{
		Description:  "High-quality wake",
		Context:      "Need strong reasoning for this.",
		Model:        "claude-sonnet-4-20250514",
		LocalOnly:    &localOnly,
		QualityFloor: 8,
		Trigger:      Trigger{EntityID: "person.dan", EntityState: "home"},
	}

	if err := store.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := store.Get(a.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.Model != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q, want %q", got.Model, "claude-sonnet-4-20250514")
	}
	if got.LocalOnly == nil {
		t.Fatal("local_only = nil, want non-nil")
	}
	if *got.LocalOnly != false {
		t.Errorf("local_only = %v, want false", *got.LocalOnly)
	}
	if got.QualityFloor != 8 {
		t.Errorf("quality_floor = %d, want 8", got.QualityFloor)
	}
}

func TestRoutingHints_Defaults(t *testing.T) {
	store := setupTestStore(t)

	a := &Anticipation{
		Description: "Default routing",
		Context:     "Use default hints.",
		Trigger:     Trigger{EntityID: "sensor.temp"},
	}

	if err := store.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := store.Get(a.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.Model != "" {
		t.Errorf("model = %q, want empty", got.Model)
	}
	if got.LocalOnly != nil {
		t.Errorf("local_only = %v, want nil", got.LocalOnly)
	}
	if got.QualityFloor != 0 {
		t.Errorf("quality_floor = %d, want 0", got.QualityFloor)
	}
}

func TestRoutingHints_Active(t *testing.T) {
	store := setupTestStore(t)

	localOnly := true
	if err := store.Create(&Anticipation{
		Description:  "With routing hints",
		Context:      "Check",
		Model:        "test-model",
		LocalOnly:    &localOnly,
		QualityFloor: 7,
		Trigger:      Trigger{EntityID: "sensor.a"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	active, err := store.Active()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("active count = %d, want 1", len(active))
	}
	if active[0].Model != "test-model" {
		t.Errorf("active model = %q, want %q", active[0].Model, "test-model")
	}
	if active[0].LocalOnly == nil || !*active[0].LocalOnly {
		t.Errorf("active local_only = %v, want true", active[0].LocalOnly)
	}
	if active[0].QualityFloor != 7 {
		t.Errorf("active quality_floor = %d, want 7", active[0].QualityFloor)
	}
}

func TestRoutingHints_LocalOnlyExplicitTrue(t *testing.T) {
	store := setupTestStore(t)

	localOnly := true
	a := &Anticipation{
		Description: "Explicit local only",
		Context:     "Stay local.",
		LocalOnly:   &localOnly,
		Trigger:     Trigger{EntityID: "sensor.a"},
	}
	if err := store.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := store.Get(a.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.LocalOnly == nil {
		t.Fatal("local_only = nil, want non-nil")
	}
	if !*got.LocalOnly {
		t.Error("local_only = false, want true")
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
