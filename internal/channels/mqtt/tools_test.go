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
	return NewTools(store)
}

func TestToolsHandleListEmpty(t *testing.T) {
	tools := newTestTools(t)
	result, err := tools.HandleListWakeSubscriptions(context.Background(), nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(result, "No MQTT wake") {
		t.Errorf("expected empty message, got %q", result)
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

	// Add then remove.
	args := map[string]any{"topic": "remove/test"}
	_, err := tools.HandleAddWakeSubscription(context.Background(), args)
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	subs := tools.store.List()
	if len(subs) != 1 {
		t.Fatalf("expected 1 sub, got %d", len(subs))
	}

	result, err := tools.HandleRemoveWakeSubscription(context.Background(), map[string]any{"id": subs[0].ID})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !strings.Contains(result, "removed") {
		t.Errorf("expected removed message, got %q", result)
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

	_, err := tools.HandleRemoveWakeSubscription(context.Background(), map[string]any{"id": subs[0].ID})
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
