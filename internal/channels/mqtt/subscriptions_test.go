package mqtt

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"

	_ "modernc.org/sqlite"
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

func wakeTarget(name string) messages.LoopWakeTarget {
	return messages.LoopWakeTarget{Name: name}
}

func TestSubscriptionStoreAddRemoveList(t *testing.T) {
	s := newTestStore(t)

	if subs := s.List(); len(subs) != 0 {
		t.Fatalf("expected empty list, got %d", len(subs))
	}

	ws, err := s.Add(AddRequest{Topic: "test/topic", WakeTarget: wakeTarget("triage")})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if ws.Topic != "test/topic" {
		t.Errorf("topic = %q, want %q", ws.Topic, "test/topic")
	}
	if ws.Source != "runtime" {
		t.Errorf("source = %q, want %q", ws.Source, "runtime")
	}
	if ws.WakeTarget.Name != "triage" {
		t.Errorf("wake_target name = %q, want triage", ws.WakeTarget.Name)
	}

	subs := s.List()
	if len(subs) != 1 {
		t.Fatalf("list len = %d, want 1", len(subs))
	}
	if subs[0].ID != ws.ID {
		t.Errorf("list[0].ID = %q, want %q", subs[0].ID, ws.ID)
	}

	if err := s.Remove(ws.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if subs := s.List(); len(subs) != 0 {
		t.Fatalf("expected empty list after remove, got %d", len(subs))
	}
}

func TestSubscriptionStoreAddRequiresWakeTarget(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.Add(AddRequest{Topic: "foo"}); err == nil {
		t.Fatal("expected error for missing wake_loop target")
	}
}

func TestSubscriptionStoreLoadConfigWakeLoop(t *testing.T) {
	s := newTestStore(t)

	target := wakeTarget("custom_handler")
	cfgSubs := []config.SubscriptionConfig{
		{Topic: "homeassistant/+/+/state"},           // ambient-awareness only
		{Topic: "frigate/events", WakeLoop: &target}, // wake_loop
		{Topic: "tagged/topic", WakeLoop: &target,
			InitialTags: []string{"owner", "security"}},
	}

	if err := s.LoadConfig(cfgSubs); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	subs := s.List()
	if len(subs) != 2 {
		t.Fatalf("list len = %d, want 2 (only wake-enabled)", len(subs))
	}
	for _, ws := range subs {
		if ws.Source != "config" {
			t.Errorf("source = %q, want config", ws.Source)
		}
		if ws.WakeTarget.Name != "custom_handler" {
			t.Errorf("wake_target.Name = %q, want custom_handler", ws.WakeTarget.Name)
		}
	}
	// The tagged entry's InitialTags should merge into wake_target.Tags.
	taggedFound := false
	for _, ws := range subs {
		if ws.Topic == "tagged/topic" {
			taggedFound = true
			gotTags := map[string]bool{}
			for _, t := range ws.WakeTarget.Tags {
				gotTags[t] = true
			}
			if !gotTags["owner"] || !gotTags["security"] {
				t.Errorf("wake_target.Tags = %v, want owner+security", ws.WakeTarget.Tags)
			}
		}
	}
	if !taggedFound {
		t.Fatal("tagged subscription not found")
	}
}

func TestSubscriptionStoreLoadConfigMigratesLegacyWake(t *testing.T) {
	s := newTestStore(t)

	legacyProfile := router.LoopProfile{Mission: "automation", Instructions: "watch hard"}
	cfgSubs := []config.SubscriptionConfig{
		{Topic: "legacy/topic", Wake: &legacyProfile, InitialTags: []string{"alpha"}},
	}
	if err := s.LoadConfig(cfgSubs); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	subs := s.List()
	if len(subs) != 1 {
		t.Fatalf("list len = %d, want 1", len(subs))
	}
	got := subs[0]
	if got.WakeTarget.Name != DefaultHandlerLoopName {
		t.Errorf("migrated wake_target.Name = %q, want %q", got.WakeTarget.Name, DefaultHandlerLoopName)
	}
	if got.WakeTarget.Instructions != "watch hard" {
		t.Errorf("migrated instructions = %q, want %q", got.WakeTarget.Instructions, "watch hard")
	}
	if len(got.WakeTarget.Tags) != 1 || got.WakeTarget.Tags[0] != "alpha" {
		t.Errorf("migrated tags = %v, want [alpha]", got.WakeTarget.Tags)
	}
}

func TestSubscriptionStoreLoadConfigRejectsEmptyWakeLoop(t *testing.T) {
	s := newTestStore(t)
	empty := messages.LoopWakeTarget{}
	err := s.LoadConfig([]config.SubscriptionConfig{
		{Topic: "broken/topic", WakeLoop: &empty},
	})
	if err == nil {
		t.Fatal("expected error for empty wake_loop selector")
	}
}

func TestSubscriptionStoreConfigNotRemovable(t *testing.T) {
	s := newTestStore(t)

	target := wakeTarget("handler")
	if err := s.LoadConfig([]config.SubscriptionConfig{
		{Topic: "test/topic", WakeLoop: &target},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	subs := s.List()
	if len(subs) != 1 {
		t.Fatalf("expected 1 sub, got %d", len(subs))
	}

	if err := s.Remove(subs[0].ID); err == nil {
		t.Fatal("expected error removing config subscription, got nil")
	}
}

func TestSubscriptionStoreMatches(t *testing.T) {
	s := newTestStore(t)

	target := wakeTarget("handler")
	if err := s.LoadConfig([]config.SubscriptionConfig{
		{Topic: "frigate/+/events", WakeLoop: &target},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	matches := s.Matches("frigate/camera1/events")
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].Topic != "frigate/+/events" {
		t.Errorf("matched topic = %q, want %q", matches[0].Topic, "frigate/+/events")
	}

	if matches := s.Matches("homeassistant/sensor/temperature/state"); len(matches) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(matches))
	}
}

func TestSubscriptionStoreMatchesFanOut(t *testing.T) {
	s := newTestStore(t)

	targetA := messages.LoopWakeTarget{Name: "handler_a"}
	targetB := messages.LoopWakeTarget{Name: "handler_b"}
	if err := s.LoadConfig([]config.SubscriptionConfig{
		{Topic: "sensors/temperature", WakeLoop: &targetA},
		{Topic: "sensors/temperature", WakeLoop: &targetB},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	matches := s.Matches("sensors/temperature")
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches for fan-out, got %d", len(matches))
	}
	if matches[0].ID == matches[1].ID {
		t.Errorf("fan-out subscriptions have duplicate IDs: %q", matches[0].ID)
	}
	names := map[string]bool{
		matches[0].WakeTarget.Name: true,
		matches[1].WakeTarget.Name: true,
	}
	if !names["handler_a"] || !names["handler_b"] {
		t.Errorf("expected both handler names, got %v / %v", matches[0].WakeTarget.Name, matches[1].WakeTarget.Name)
	}
}

func TestSubscriptionStorePersistence(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer db.Close()

	s1, err := NewSubscriptionStore(db, nil)
	if err != nil {
		t.Fatalf("new store 1: %v", err)
	}
	if _, err := s1.Add(AddRequest{
		Topic:      "persist/test",
		WakeTarget: messages.LoopWakeTarget{Name: "handler", Tags: []string{"persisted"}},
	}); err != nil {
		t.Fatalf("add: %v", err)
	}

	s2, err := NewSubscriptionStore(db, nil)
	if err != nil {
		t.Fatalf("new store 2: %v", err)
	}
	subs := s2.List()
	if len(subs) != 1 {
		t.Fatalf("after reload, list len = %d, want 1", len(subs))
	}
	got := subs[0]
	if got.Topic != "persist/test" {
		t.Errorf("topic = %q, want %q", got.Topic, "persist/test")
	}
	if got.WakeTarget.Name != "handler" {
		t.Errorf("wake_target.Name = %q, want handler", got.WakeTarget.Name)
	}
	if len(got.WakeTarget.Tags) != 1 || got.WakeTarget.Tags[0] != "persisted" {
		t.Errorf("wake_target.Tags = %v, want [persisted]", got.WakeTarget.Tags)
	}
}

func TestSubscriptionStoreLoadRuntimeMigratesLegacyRow(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer db.Close()

	// Hydrate the schema first so the legacy columns exist.
	if _, err := NewSubscriptionStore(db, nil); err != nil {
		t.Fatalf("schema bootstrap: %v", err)
	}

	// Insert a legacy-shaped row directly: seed_json populated,
	// wake_target_json defaulted to "{}" by the column default.
	_, err = db.Exec(
		`INSERT INTO mqtt_wake_subscriptions (id, topic, seed_json, initial_tags_json, wake_target_json, source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"rt-legacy-1", "legacy/topic",
		`{"mission":"automation","instructions":"do the thing"}`,
		`["alpha","beta"]`,
		`{}`,
		"runtime",
		"2026-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	// Reopen — loadRuntime should migrate the row.
	s, err := NewSubscriptionStore(db, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	subs := s.List()
	if len(subs) != 1 {
		t.Fatalf("list len = %d, want 1", len(subs))
	}
	got := subs[0]
	if got.WakeTarget.Name != DefaultHandlerLoopName {
		t.Errorf("migrated wake_target.Name = %q, want %q", got.WakeTarget.Name, DefaultHandlerLoopName)
	}
	if got.WakeTarget.Instructions != "do the thing" {
		t.Errorf("migrated instructions = %q, want %q", got.WakeTarget.Instructions, "do the thing")
	}
	if len(got.WakeTarget.Tags) != 2 || got.WakeTarget.Tags[0] != "alpha" || got.WakeTarget.Tags[1] != "beta" {
		t.Errorf("migrated tags = %v, want [alpha beta]", got.WakeTarget.Tags)
	}

	// Verify the row was persisted with the new wake_target_json,
	// so the next boot doesn't re-migrate.
	var storedTarget string
	if err := db.QueryRow(`SELECT wake_target_json FROM mqtt_wake_subscriptions WHERE id = ?`, "rt-legacy-1").Scan(&storedTarget); err != nil {
		t.Fatalf("select wake_target_json: %v", err)
	}
	if storedTarget == "{}" || storedTarget == "" {
		t.Fatalf("wake_target_json was not persisted after migration: %q", storedTarget)
	}
}

func TestSubscriptionStoreTopics(t *testing.T) {
	s := newTestStore(t)

	target := wakeTarget("handler")
	if err := s.LoadConfig([]config.SubscriptionConfig{
		{Topic: "topic/a", WakeLoop: &target},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if _, err := s.Add(AddRequest{Topic: "topic/b", WakeTarget: target}); err != nil {
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

	if _, err := s.Add(AddRequest{Topic: "hook/test", WakeTarget: wakeTarget("handler")}); err != nil {
		t.Fatalf("add: %v", err)
	}

	if len(hookedTopics) != 1 || hookedTopics[0] != "hook/test" {
		t.Errorf("hook received %v, want [hook/test]", hookedTopics)
	}
}

// stubResolver implements messages.LoopResolver for VerifyTargets
// tests. Names listed at construction time resolve; everything else
// is treated as unregistered.
type stubResolver struct {
	known map[string]bool
}

func (s stubResolver) LoopExistsByID(string) bool        { return false }
func (s stubResolver) LoopExistsByName(name string) bool { return s.known[name] }
func (s stubResolver) KnownLoopNames() []string {
	out := make([]string, 0, len(s.known))
	for name := range s.known {
		out = append(out, name)
	}
	return out
}

// TestSubscriptionStoreVerifyTargetsFailsLoudOnUnregistered pins the
// Codex P2 fix: config-defined subscriptions referencing a loop that
// nobody registers now error out at the post-StartEnabledServices
// verification pass, instead of silently dropping the first matching
// message at delivery time.
func TestSubscriptionStoreVerifyTargetsFailsLoudOnUnregistered(t *testing.T) {
	s := newTestStore(t)
	good := wakeTarget("real_handler")
	bad := wakeTarget("typo_handler")
	if err := s.LoadConfig([]config.SubscriptionConfig{
		{Topic: "ok/topic", WakeLoop: &good},
		{Topic: "broken/topic", WakeLoop: &bad},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	resolver := stubResolver{known: map[string]bool{"real_handler": true}}
	err := s.VerifyTargets(resolver)
	if err == nil {
		t.Fatal("expected VerifyTargets to fail on unregistered target")
	}
	if !strings.Contains(err.Error(), "typo_handler") {
		t.Fatalf("error = %v, want mention of typo_handler", err)
	}
}

// TestSubscriptionStoreVerifyTargetsSkipsOrphanedRuntime pins the prod
// outage fix: a runtime subscription can outlive its target loop (the loop
// was deleted/disabled after the subscription was persisted). That orphan
// must be warned-and-skipped, not crash startup the way a config typo does.
func TestSubscriptionStoreVerifyTargetsSkipsOrphanedRuntime(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer db.Close()

	// Bootstrap the schema.
	if _, err := NewSubscriptionStore(db, nil); err != nil {
		t.Fatalf("schema bootstrap: %v", err)
	}

	// Insert a runtime subscription targeting a loop nobody runs. Add()
	// validates targets at add-time, so an orphan can only arise from the
	// loop being removed later — simulated here by a direct insert.
	wtJSON, err := json.Marshal(wakeTarget("deleted_loop"))
	if err != nil {
		t.Fatalf("marshal target: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO mqtt_wake_subscriptions (id, topic, seed_json, initial_tags_json, wake_target_json, source, created_at) VALUES (?, ?, '{}', '[]', ?, 'runtime', ?)`,
		"rt-orphan-1", "presence/zone_change", string(wtJSON), "2026-01-01T00:00:00Z",
	); err != nil {
		t.Fatalf("insert orphan: %v", err)
	}

	// Reconstruct so the store hydrates the orphaned row.
	s, err := NewSubscriptionStore(db, nil)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}

	resolver := stubResolver{known: map[string]bool{"some_other_loop": true}}
	if err := s.VerifyTargets(resolver); err != nil {
		t.Fatalf("orphaned runtime subscription must be skipped, not fatal: %v", err)
	}
}

// TestSubscriptionStoreRemoveByWakeLoop covers the cascade that prevents
// orphans at the source: deleting a loop removes runtime subscriptions that
// target it, reports (but keeps) config-sourced ones, and leaves
// subscriptions for other loops untouched.
func TestSubscriptionStoreRemoveByWakeLoop(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if _, err := NewSubscriptionStore(db, nil); err != nil {
		t.Fatalf("schema bootstrap: %v", err)
	}

	insert := func(id, topic, loop, source string) {
		t.Helper()
		wt, err := json.Marshal(wakeTarget(loop))
		if err != nil {
			t.Fatalf("marshal target: %v", err)
		}
		if _, err := db.Exec(
			`INSERT INTO mqtt_wake_subscriptions (id, topic, seed_json, initial_tags_json, wake_target_json, source, created_at) VALUES (?, ?, '{}', '[]', ?, ?, ?)`,
			id, topic, string(wt), source, "2026-01-01T00:00:00Z",
		); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	insert("rt-x", "x/topic", "loop_x", "runtime")
	insert("cfg-x", "xc/topic", "loop_x", "config")
	insert("rt-y", "y/topic", "loop_y", "runtime")

	s, err := NewSubscriptionStore(db, nil)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}

	removed, configRefs, err := s.RemoveByWakeLoop("loop_x")
	if err != nil {
		t.Fatalf("RemoveByWakeLoop: %v", err)
	}
	if len(removed) != 1 || removed[0].ID != "rt-x" {
		t.Errorf("removed = %+v, want one entry rt-x", removed)
	}
	if len(configRefs) != 1 || configRefs[0].ID != "cfg-x" {
		t.Errorf("configRefs = %+v, want one entry cfg-x", configRefs)
	}

	ids := map[string]bool{}
	for _, ws := range s.List() {
		ids[ws.ID] = true
	}
	if ids["rt-x"] {
		t.Error("rt-x (runtime, loop_x) should have been removed")
	}
	if !ids["cfg-x"] {
		t.Error("cfg-x (config, loop_x) must NOT be removed — config is source of truth")
	}
	if !ids["rt-y"] {
		t.Error("rt-y (different loop) must be untouched")
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mqtt_wake_subscriptions WHERE id='rt-x'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Error("rt-x row should be deleted from the database")
	}
}

func TestSubscriptionStoreVerifyTargetsNilResolverIsNoop(t *testing.T) {
	s := newTestStore(t)
	target := wakeTarget("anything")
	if err := s.LoadConfig([]config.SubscriptionConfig{
		{Topic: "ok/topic", WakeLoop: &target},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := s.VerifyTargets(nil); err != nil {
		t.Fatalf("VerifyTargets(nil) = %v, want nil", err)
	}
}

func TestSubscriptionStoreAddValidation(t *testing.T) {
	s := newTestStore(t)

	target := wakeTarget("handler")
	if _, err := s.Add(AddRequest{Topic: "", WakeTarget: target}); err == nil {
		t.Fatal("expected error for empty topic")
	}
	if _, err := s.Add(AddRequest{Topic: "foo/ba#r", WakeTarget: target}); err == nil {
		t.Fatal("expected error for bad wildcard in topic")
	}
	if _, err := s.Add(AddRequest{Topic: "valid/topic", WakeTarget: target}); err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
}
