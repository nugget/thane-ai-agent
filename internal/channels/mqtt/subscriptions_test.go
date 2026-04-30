package mqtt

import (
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"

	_ "github.com/mattn/go-sqlite3"
)

func TestMatchTopicFilter(t *testing.T) {
	tests := []struct {
		filter string
		topic  string
		want   bool
	}{
		// Exact matches.
		{"foo/bar", "foo/bar", true},
		{"foo/bar", "foo/baz", false},
		{"foo", "foo", true},
		{"foo", "bar", false},

		// Single-level wildcard (+).
		{"foo/+/bar", "foo/x/bar", true},
		{"foo/+/bar", "foo/y/bar", true},
		{"foo/+/bar", "foo/x/baz", false},
		{"foo/+/bar", "foo/bar", false}, // + must match exactly one level
		{"+/bar", "foo/bar", true},
		{"+/+", "foo/bar", true},
		{"+/+", "foo/bar/baz", false},

		// Multi-level wildcard (#).
		{"#", "foo", true},
		{"#", "foo/bar", true},
		{"#", "foo/bar/baz", true},
		{"foo/#", "foo", true}, // # matches zero or more remaining levels
		{"foo/#", "foo/bar", true},
		{"foo/#", "foo/bar/baz", true},
		{"foo/bar/#", "foo/bar/baz/qux", true},

		// Mixed wildcards.
		{"+/bar/#", "foo/bar/baz", true},
		{"+/bar/#", "foo/bar/baz/qux", true},
		{"+/bar/#", "foo/baz/qux", false},

		// Edge cases.
		{"", "", true},
		{"foo/bar", "foo", false},
		{"foo", "foo/bar", false},
	}

	for _, tt := range tests {
		t.Run(tt.filter+"→"+tt.topic, func(t *testing.T) {
			if got := matchTopicFilter(tt.filter, tt.topic); got != tt.want {
				t.Errorf("matchTopicFilter(%q, %q) = %v, want %v", tt.filter, tt.topic, got, tt.want)
			}
		})
	}
}

func newTestStore(t *testing.T) *SubscriptionStore {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s, err := NewSubscriptionStore(db, nil)
	if err != nil {
		t.Fatalf("new subscription store: %v", err)
	}
	return s
}

func TestSubscriptionStoreAddRemoveList(t *testing.T) {
	s := newTestStore(t)

	// Initially empty.
	if subs := s.List(); len(subs) != 0 {
		t.Fatalf("expected empty list, got %d", len(subs))
	}

	// Add a subscription.
	ws, err := s.Add("test/topic", router.LoopProfile{Mission: "automation"}, nil)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if ws.Topic != "test/topic" {
		t.Errorf("topic = %q, want %q", ws.Topic, "test/topic")
	}
	if ws.Source != "runtime" {
		t.Errorf("source = %q, want %q", ws.Source, "runtime")
	}

	// List shows it.
	subs := s.List()
	if len(subs) != 1 {
		t.Fatalf("list len = %d, want 1", len(subs))
	}
	if subs[0].ID != ws.ID {
		t.Errorf("list[0].ID = %q, want %q", subs[0].ID, ws.ID)
	}

	// Remove it.
	if err := s.Remove(ws.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if subs := s.List(); len(subs) != 0 {
		t.Fatalf("expected empty list after remove, got %d", len(subs))
	}
}

func TestSubscriptionStoreLoadConfig(t *testing.T) {
	s := newTestStore(t)

	profile := router.LoopProfile{QualityFloor: "7", Mission: "automation"}
	cfgSubs := []config.SubscriptionConfig{
		{Topic: "homeassistant/+/+/state"},        // no wake
		{Topic: "frigate/events", Wake: &profile}, // wake-enabled
	}

	if err := s.LoadConfig(cfgSubs); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	subs := s.List()
	if len(subs) != 1 {
		t.Fatalf("list len = %d, want 1 (only wake-enabled)", len(subs))
	}
	if subs[0].Source != "config" {
		t.Errorf("source = %q, want %q", subs[0].Source, "config")
	}
	if subs[0].Profile.QualityFloor != "7" {
		t.Errorf("profile.QualityFloor = %q, want %q", subs[0].Profile.QualityFloor, "7")
	}
}

func TestSubscriptionStoreConfigNotRemovable(t *testing.T) {
	s := newTestStore(t)

	profile := router.LoopProfile{Mission: "automation"}
	if err := s.LoadConfig([]config.SubscriptionConfig{
		{Topic: "test/topic", Wake: &profile},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	subs := s.List()
	if len(subs) != 1 {
		t.Fatalf("expected 1 sub, got %d", len(subs))
	}

	err := s.Remove(subs[0].ID)
	if err == nil {
		t.Fatal("expected error removing config subscription, got nil")
	}
}

func TestSubscriptionStoreMatches(t *testing.T) {
	s := newTestStore(t)

	profile := router.LoopProfile{Mission: "automation"}
	if err := s.LoadConfig([]config.SubscriptionConfig{
		{Topic: "frigate/+/events", Wake: &profile},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	// Matching topic.
	matches := s.Matches("frigate/camera1/events")
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].Topic != "frigate/+/events" {
		t.Errorf("matched topic = %q, want %q", matches[0].Topic, "frigate/+/events")
	}

	// Non-matching topic.
	matches = s.Matches("homeassistant/sensor/temperature/state")
	if len(matches) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(matches))
	}
}

func TestSubscriptionStoreMatchesFanOut(t *testing.T) {
	s := newTestStore(t)

	// Two subscriptions on the same topic with different profiles.
	profileA := router.LoopProfile{Mission: "automation", Instructions: "check temperature"}
	profileB := router.LoopProfile{Mission: "background", Instructions: "log to database"}
	if err := s.LoadConfig([]config.SubscriptionConfig{
		{Topic: "sensors/temperature", Wake: &profileA},
		{Topic: "sensors/temperature", Wake: &profileB},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	matches := s.Matches("sensors/temperature")
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches for fan-out, got %d", len(matches))
	}

	// Verify they have distinct IDs.
	if matches[0].ID == matches[1].ID {
		t.Errorf("fan-out subscriptions have duplicate IDs: %q", matches[0].ID)
	}

	// Verify distinct profiles carried through.
	missions := map[string]bool{
		matches[0].Profile.Mission: true,
		matches[1].Profile.Mission: true,
	}
	if !missions["automation"] || !missions["background"] {
		t.Errorf("expected both missions, got %v and %v",
			matches[0].Profile.Mission, matches[1].Profile.Mission)
	}
}

func TestSubscriptionStorePersistence(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer db.Close()

	// Create store and add a runtime subscription.
	s1, err := NewSubscriptionStore(db, nil)
	if err != nil {
		t.Fatalf("new store 1: %v", err)
	}

	_, err = s1.Add("persist/test", router.LoopProfile{Mission: "automation", QualityFloor: "5"}, nil)
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	// Create a new store from the same DB — simulates restart.
	s2, err := NewSubscriptionStore(db, nil)
	if err != nil {
		t.Fatalf("new store 2: %v", err)
	}

	subs := s2.List()
	if len(subs) != 1 {
		t.Fatalf("after reload, list len = %d, want 1", len(subs))
	}
	if subs[0].Topic != "persist/test" {
		t.Errorf("topic = %q, want %q", subs[0].Topic, "persist/test")
	}
	if subs[0].Profile.Mission != "automation" {
		t.Errorf("profile.Mission = %q, want %q", subs[0].Profile.Mission, "automation")
	}
}

// TestSubscriptionStoreLegacyInitialTagsMigration verifies that rows
// written by a pre-migration server (initial_tags embedded inside the
// LoopProfile seed_json blob, no initial_tags_json column populated)
// are hoisted onto WakeSubscription.InitialTags on first load AND
// written back to the new column so the migration is durable.
func TestSubscriptionStoreLegacyInitialTagsMigration(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer db.Close()

	if _, err := NewSubscriptionStore(db, nil); err != nil {
		t.Fatalf("new store (init schema): %v", err)
	}

	// Synthesize a pre-migration row: LoopProfile JSON includes the
	// removed initial_tags field, and initial_tags_json is empty.
	legacyJSON := `{"mission":"automation","initial_tags":["homeassistant","security"]}`
	if _, err := db.Exec(
		`INSERT INTO mqtt_wake_subscriptions (id, topic, seed_json, initial_tags_json, source, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"legacy-row", "legacy/topic", legacyJSON, "[]", "runtime", "2026-04-01T00:00:00Z",
	); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	// First load — hoists from seed_json, writes back to the column.
	s1, err := NewSubscriptionStore(db, nil)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	subs := s1.List()
	if len(subs) != 1 {
		t.Fatalf("first load len = %d, want 1", len(subs))
	}
	if got := subs[0].InitialTags; len(got) != 2 || got[0] != "homeassistant" || got[1] != "security" {
		t.Fatalf("first-load InitialTags = %v, want [homeassistant security]", got)
	}

	// Verify the write-back actually landed in the column.
	var stored string
	if err := db.QueryRow(`SELECT initial_tags_json FROM mqtt_wake_subscriptions WHERE id = ?`, "legacy-row").Scan(&stored); err != nil {
		t.Fatalf("select after migration: %v", err)
	}
	if stored != `["homeassistant","security"]` {
		t.Fatalf("initial_tags_json after migration = %q, want JSON array", stored)
	}

	// Second load — should read from the column directly. The legacy
	// extractor would still fire (seed_json is unchanged), but the
	// column wins and the result is identical.
	s2, err := NewSubscriptionStore(db, nil)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	subs = s2.List()
	if len(subs) != 1 {
		t.Fatalf("second load len = %d, want 1", len(subs))
	}
	if got := subs[0].InitialTags; len(got) != 2 || got[0] != "homeassistant" || got[1] != "security" {
		t.Fatalf("second-load InitialTags = %v, want [homeassistant security]", got)
	}
}

// TestSubscriptionStoreAddPersistsEmptyTagsAsArray verifies that a
// subscription added with no InitialTags writes "[]" rather than
// "null" — matching the column's default and keeping the on-disk
// shape consistent.
func TestSubscriptionStoreAddPersistsEmptyTagsAsArray(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer db.Close()

	s, err := NewSubscriptionStore(db, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	ws, err := s.Add("empty/tags", router.LoopProfile{Mission: "automation"}, nil)
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	var stored string
	if err := db.QueryRow(`SELECT initial_tags_json FROM mqtt_wake_subscriptions WHERE id = ?`, ws.ID).Scan(&stored); err != nil {
		t.Fatalf("select: %v", err)
	}
	if stored != "[]" {
		t.Fatalf("initial_tags_json = %q, want %q", stored, "[]")
	}
}

func TestSubscriptionStoreTopics(t *testing.T) {
	s := newTestStore(t)

	profile := router.LoopProfile{Mission: "automation"}
	if err := s.LoadConfig([]config.SubscriptionConfig{
		{Topic: "topic/a", Wake: &profile},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if _, err := s.Add("topic/b", profile, nil); err != nil {
		t.Fatalf("add: %v", err)
	}

	topics := s.Topics()
	if len(topics) != 2 {
		t.Fatalf("topics len = %d, want 2", len(topics))
	}

	found := make(map[string]bool)
	for _, tp := range topics {
		found[tp] = true
	}
	if !found["topic/a"] || !found["topic/b"] {
		t.Errorf("topics = %v, want [topic/a, topic/b]", topics)
	}
}

func TestSubscriptionStoreSubscribeHook(t *testing.T) {
	s := newTestStore(t)

	var hookedTopics []string
	s.SetSubscribeHook(func(topics []string) {
		hookedTopics = append(hookedTopics, topics...)
	})

	_, err := s.Add("hook/test", router.LoopProfile{Mission: "automation"}, nil)
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	if len(hookedTopics) != 1 || hookedTopics[0] != "hook/test" {
		t.Errorf("hook received %v, want [hook/test]", hookedTopics)
	}
}

func TestSubscriptionStoreSubscribeHookNotCalledWithoutHook(t *testing.T) {
	s := newTestStore(t)

	// No hook set — Add should not panic.
	_, err := s.Add("no-hook/test", router.LoopProfile{}, nil)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
}

func TestSubscriptionStoreAddValidation(t *testing.T) {
	s := newTestStore(t)

	// Invalid topic filter.
	_, err := s.Add("", router.LoopProfile{}, nil)
	if err == nil {
		t.Fatal("expected error for empty topic")
	}

	// Invalid topic with bad wildcard.
	_, err = s.Add("foo/ba#r", router.LoopProfile{}, nil)
	if err == nil {
		t.Fatal("expected error for bad wildcard in topic")
	}

	// Invalid seed.
	_, err = s.Add("valid/topic", router.LoopProfile{QualityFloor: "99"}, nil)
	if err == nil {
		t.Fatal("expected error for invalid quality_floor")
	}

	// Valid — should succeed.
	_, err = s.Add("valid/topic", router.LoopProfile{Mission: "automation", QualityFloor: "7"}, nil)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
}
