package mqtt

import (
	"testing"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/database"
	"github.com/nugget/thane-ai-agent/internal/router"

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
	ws, err := s.Add("test/topic", router.LoopSeed{Mission: "automation"})
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

	seed := router.LoopSeed{QualityFloor: "7", Mission: "automation"}
	cfgSubs := []config.SubscriptionConfig{
		{Topic: "homeassistant/+/+/state"},     // no wake
		{Topic: "frigate/events", Wake: &seed}, // wake-enabled
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
	if subs[0].Seed.QualityFloor != "7" {
		t.Errorf("seed.QualityFloor = %q, want %q", subs[0].Seed.QualityFloor, "7")
	}
}

func TestSubscriptionStoreConfigNotRemovable(t *testing.T) {
	s := newTestStore(t)

	seed := router.LoopSeed{Mission: "automation"}
	if err := s.LoadConfig([]config.SubscriptionConfig{
		{Topic: "test/topic", Wake: &seed},
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

	seed := router.LoopSeed{Mission: "automation"}
	if err := s.LoadConfig([]config.SubscriptionConfig{
		{Topic: "frigate/+/events", Wake: &seed},
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

	// Two subscriptions on the same topic with different seeds.
	seedA := router.LoopSeed{Mission: "automation", Instructions: "check temperature"}
	seedB := router.LoopSeed{Mission: "background", Instructions: "log to database"}
	if err := s.LoadConfig([]config.SubscriptionConfig{
		{Topic: "sensors/temperature", Wake: &seedA},
		{Topic: "sensors/temperature", Wake: &seedB},
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

	// Verify distinct seeds carried through.
	missions := map[string]bool{
		matches[0].Seed.Mission: true,
		matches[1].Seed.Mission: true,
	}
	if !missions["automation"] || !missions["background"] {
		t.Errorf("expected both missions, got %v and %v",
			matches[0].Seed.Mission, matches[1].Seed.Mission)
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

	_, err = s1.Add("persist/test", router.LoopSeed{Mission: "automation", QualityFloor: "5"})
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
	if subs[0].Seed.Mission != "automation" {
		t.Errorf("seed.Mission = %q, want %q", subs[0].Seed.Mission, "automation")
	}
}

func TestSubscriptionStoreTopics(t *testing.T) {
	s := newTestStore(t)

	seed := router.LoopSeed{Mission: "automation"}
	if err := s.LoadConfig([]config.SubscriptionConfig{
		{Topic: "topic/a", Wake: &seed},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if _, err := s.Add("topic/b", seed); err != nil {
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

	_, err := s.Add("hook/test", router.LoopSeed{Mission: "automation"})
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
	_, err := s.Add("no-hook/test", router.LoopSeed{})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
}

func TestSubscriptionStoreAddValidation(t *testing.T) {
	s := newTestStore(t)

	// Invalid topic filter.
	_, err := s.Add("", router.LoopSeed{})
	if err == nil {
		t.Fatal("expected error for empty topic")
	}

	// Invalid topic with bad wildcard.
	_, err = s.Add("foo/ba#r", router.LoopSeed{})
	if err == nil {
		t.Fatal("expected error for bad wildcard in topic")
	}

	// Invalid seed.
	_, err = s.Add("valid/topic", router.LoopSeed{QualityFloor: "99"})
	if err == nil {
		t.Fatal("expected error for invalid quality_floor")
	}

	// Valid — should succeed.
	_, err = s.Add("valid/topic", router.LoopSeed{Mission: "automation", QualityFloor: "7"})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
}
