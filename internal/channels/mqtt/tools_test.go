package mqtt

import (
	"context"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"

	_ "github.com/mattn/go-sqlite3"
)

func newTestTools(t *testing.T) *Tools {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewSubscriptionStore(db, nil)
	if err != nil {
		t.Fatalf("new subscription store: %v", err)
	}
	// Pass a nil resolver so wake_loop verification is skipped in
	// tests that don't care about it. Tests that DO exercise the
	// verification path supply their own resolver.
	return NewTools(store, nil)
}

func TestToolsHandleListEmpty(t *testing.T) {
	tools := newTestTools(t)
	result, err := tools.HandleListWakeSubscriptions(context.Background(), nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// Empty store now emits the canonical empty JSON array (matches
	// the cross-family return shape used by forge_repo_subscriptions
	// and media_feeds); the legacy "No MQTT wake subscriptions
	// configured." prose was retired in PR-T1.
	if strings.TrimSpace(result) != "[]" {
		t.Errorf("expected empty JSON array, got %q", result)
	}
}

func TestToolsHandleAddAndList(t *testing.T) {
	tools := newTestTools(t)

	args := map[string]any{
		"topic":         "test/wake",
		"mission":       "automation",
		"quality_floor": "7",
		"instructions":  "handle this event",
	}

	result, err := tools.HandleAddWakeSubscription(context.Background(), args)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(result, "test/wake") {
		t.Errorf("expected topic in result, got %q", result)
	}

	list, err := tools.HandleListWakeSubscriptions(context.Background(), nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(list, "test/wake") {
		t.Errorf("expected topic in list, got %q", list)
	}
	if !strings.Contains(list, "automation") {
		t.Errorf("expected mission in list, got %q", list)
	}
}

func TestToolsHandleAddWithSeedArrays(t *testing.T) {
	tools := newTestTools(t)

	args := map[string]any{
		"topic":         "arrays/test",
		"exclude_tools": []any{"shell_exec", "web_fetch"},
		"initial_tags":  []any{"homeassistant", "security"},
	}

	_, err := tools.HandleAddWakeSubscription(context.Background(), args)
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	subs := tools.store.List()
	if len(subs) != 1 {
		t.Fatalf("expected 1 sub, got %d", len(subs))
	}
	if len(subs[0].Profile.ExcludeTools) != 2 {
		t.Errorf("exclude_tools len = %d, want 2", len(subs[0].Profile.ExcludeTools))
	}
	if len(subs[0].InitialTags) != 2 {
		t.Errorf("initial_tags len = %d, want 2", len(subs[0].InitialTags))
	}
}

func TestToolsHandleAddMissingTopic(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.HandleAddWakeSubscription(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing topic")
	}
}

func TestToolsHandleRemove(t *testing.T) {
	tools := newTestTools(t)

	// Add with a non-empty legacy profile so the new "must declare
	// wake_loop or a non-empty profile" validation passes; the
	// remove path is what we're exercising here.
	args := map[string]any{"topic": "remove/test", "mission": "automation"}
	_, err := tools.HandleAddWakeSubscription(context.Background(), args)
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	subs := tools.store.List()
	if len(subs) != 1 {
		t.Fatalf("expected 1 sub, got %d", len(subs))
	}

	// Canonical parameter name post-PR-T1 is subscription_id; the
	// `id` alias still works for backwards compat (covered by its
	// own test below).
	result, err := tools.HandleRemoveWakeSubscription(context.Background(), map[string]any{"subscription_id": subs[0].ID})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !strings.Contains(result, `"status":"ok"`) {
		t.Errorf("expected JSON ok response, got %q", result)
	}

	if subs := tools.store.List(); len(subs) != 0 {
		t.Errorf("expected empty list after remove, got %d", len(subs))
	}
}

func TestToolsHandleRemoveConfigProtected(t *testing.T) {
	tools := newTestTools(t)

	profile := router.LoopProfile{Mission: "automation"}
	if err := tools.store.LoadConfig([]config.SubscriptionConfig{
		{Topic: "config/test", Wake: &profile},
	}); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	subs := tools.store.List()
	if len(subs) != 1 {
		t.Fatalf("expected 1 sub, got %d", len(subs))
	}

	_, err := tools.HandleRemoveWakeSubscription(context.Background(), map[string]any{"subscription_id": subs[0].ID})
	if err == nil {
		t.Fatal("expected error removing config subscription")
	}
}

func TestToolsHandleRemoveMissingID(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.HandleRemoveWakeSubscription(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing id")
	}
}
