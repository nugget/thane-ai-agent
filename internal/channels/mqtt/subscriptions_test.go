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

	s.LoadConfig(cfgSubs)

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
	s.LoadConfig([]config.SubscriptionConfig{
		{Topic: "test/topic", Wake: &seed},
	})

	subs := s.List()
	if len(subs) != 1 {
		t.Fatalf("expected 1 sub, got %d", len(subs))
	}

	err := s.Remove(subs[0].ID)
	if err == nil {
		t.Fatal("expected error removing config subscription, got nil")
	}
}

func TestSubscriptionStoreMatch(t *testing.T) {
	s := newTestStore(t)

	seed := router.LoopSeed{Mission: "automation"}
	s.LoadConfig([]config.SubscriptionConfig{
		{Topic: "frigate/+/events", Wake: &seed},
	})

	// Matching topic.
	ws, ok := s.Match("frigate/camera1/events")
	if !ok {
		t.Fatal("expected match, got false")
	}
	if ws.Topic != "frigate/+/events" {
		t.Errorf("matched topic = %q, want %q", ws.Topic, "frigate/+/events")
	}

	// Non-matching topic.
	_, ok = s.Match("homeassistant/sensor/temperature/state")
	if ok {
		t.Fatal("expected no match")
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
	s.LoadConfig([]config.SubscriptionConfig{
		{Topic: "topic/a", Wake: &seed},
	})
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
