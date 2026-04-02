package homeassistant

import (
	"testing"

	"github.com/nugget/thane-ai-agent/internal/database"
	"github.com/nugget/thane-ai-agent/internal/router"

	_ "github.com/mattn/go-sqlite3"
)

func newTestWakeStore(t *testing.T) *WakeStore {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewWakeStore(db)
	if err != nil {
		t.Fatalf("new wake store: %v", err)
	}
	return store
}

func TestWakeStore_CreateAndGet(t *testing.T) {
	store := newTestWakeStore(t)

	localOnly := true
	w := &WakeSubscription{
		Topic:   "thane/test/wake/motion",
		Name:    "Garage motion at night",
		KBRef:   "routines/security_protocol.md",
		Context: "Check the cameras and notify Dan",
		Seed: router.LoopSeed{
			Source:          "wake",
			Mission:         "anticipation",
			LocalOnly:       &localOnly,
			QualityFloor:    7,
			ContextEntities: []string{"binary_sensor.garage_motion", "light.garage"},
			KBRefs:          []string{"routines/security_protocol.md"},
		},
		Enabled: true,
	}

	if err := store.Create(w); err != nil {
		t.Fatalf("create: %v", err)
	}
	if w.ID == "" {
		t.Fatal("ID should be auto-generated")
	}

	got, err := store.Get(w.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if got.Topic != "thane/test/wake/motion" {
		t.Errorf("topic = %q, want %q", got.Topic, "thane/test/wake/motion")
	}
	if got.Name != "Garage motion at night" {
		t.Errorf("name = %q, want %q", got.Name, "Garage motion at night")
	}
	if got.KBRef != "routines/security_protocol.md" {
		t.Errorf("kb_ref = %q, want %q", got.KBRef, "routines/security_protocol.md")
	}
	if got.Seed.Mission != "anticipation" {
		t.Errorf("seed.Mission = %q, want %q", got.Seed.Mission, "anticipation")
	}
	if got.Seed.LocalOnly == nil || !*got.Seed.LocalOnly {
		t.Error("seed.LocalOnly should be true")
	}
	if got.Seed.QualityFloor != 7 {
		t.Errorf("seed.QualityFloor = %d, want 7", got.Seed.QualityFloor)
	}
	if len(got.Seed.ContextEntities) != 2 {
		t.Errorf("seed.ContextEntities len = %d, want 2", len(got.Seed.ContextEntities))
	}
	if got.FireCount != 0 {
		t.Errorf("fire_count = %d, want 0", got.FireCount)
	}
}

func TestWakeStore_Active(t *testing.T) {
	store := newTestWakeStore(t)

	for _, name := range []string{"sub1", "sub2", "sub3"} {
		if err := store.Create(&WakeSubscription{
			Topic:   "thane/test/" + name,
			Name:    name,
			Enabled: true,
		}); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	active, err := store.Active()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(active) != 3 {
		t.Errorf("active count = %d, want 3", len(active))
	}
}

func TestWakeStore_Delete(t *testing.T) {
	store := newTestWakeStore(t)

	w := &WakeSubscription{Topic: "thane/test/delete", Name: "to delete", Enabled: true}
	if err := store.Create(w); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := store.Delete(w.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	got, err := store.Get(w.ID)
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if got != nil {
		t.Error("expected nil after soft delete")
	}

	// Double delete should error.
	if err := store.Delete(w.ID); err == nil {
		t.Error("expected error on double delete")
	}
}

func TestWakeStore_RecordFire(t *testing.T) {
	store := newTestWakeStore(t)

	w := &WakeSubscription{Topic: "thane/test/fire", Name: "fire test", Enabled: true}
	if err := store.Create(w); err != nil {
		t.Fatalf("create: %v", err)
	}

	for range 3 {
		if err := store.RecordFire(w.ID); err != nil {
			t.Fatalf("record fire: %v", err)
		}
	}

	got, err := store.Get(w.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.FireCount != 3 {
		t.Errorf("fire_count = %d, want 3", got.FireCount)
	}
	if got.LastFiredAt == nil {
		t.Error("last_fired_at should be set")
	}
}

func TestWakeStore_Update(t *testing.T) {
	store := newTestWakeStore(t)

	w := &WakeSubscription{
		Topic:   "thane/test/original",
		Name:    "original",
		KBRef:   "old.md",
		Enabled: true,
	}
	if err := store.Create(w); err != nil {
		t.Fatalf("create: %v", err)
	}

	w.Topic = "thane/test/updated"
	w.Name = "updated"
	w.KBRef = "new.md"
	w.Seed.Mission = "automation"

	if err := store.Update(w); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := store.Get(w.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Topic != "thane/test/updated" {
		t.Errorf("topic = %q, want %q", got.Topic, "thane/test/updated")
	}
	if got.Name != "updated" {
		t.Errorf("name = %q, want %q", got.Name, "updated")
	}
	if got.KBRef != "new.md" {
		t.Errorf("kb_ref = %q, want %q", got.KBRef, "new.md")
	}
	if got.Seed.Mission != "automation" {
		t.Errorf("seed.Mission = %q, want %q", got.Seed.Mission, "automation")
	}
}

func TestWakeStore_ActiveByTopic(t *testing.T) {
	store := newTestWakeStore(t)

	// Two subs on same topic, one on a different topic.
	if err := store.Create(&WakeSubscription{Topic: "thane/test/motion", Name: "sub1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&WakeSubscription{Topic: "thane/test/motion", Name: "sub2", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&WakeSubscription{Topic: "thane/test/other", Name: "sub3", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	subs, err := store.ActiveByTopic("thane/test/motion")
	if err != nil {
		t.Fatalf("active by topic: %v", err)
	}
	if len(subs) != 2 {
		t.Errorf("count = %d, want 2", len(subs))
	}
}
