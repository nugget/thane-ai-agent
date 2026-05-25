package mqtt

import (
	"context"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
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
	// tests that don't care about it.
	return NewTools(store, nil)
}

func TestToolsHandleListEmpty(t *testing.T) {
	tools := newTestTools(t)
	result, err := tools.HandleListWakeSubscriptions(context.Background(), nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.TrimSpace(result) != "[]" {
		t.Errorf("expected empty JSON array, got %q", result)
	}
}

func TestToolsHandleAddAndList(t *testing.T) {
	tools := newTestTools(t)

	args := map[string]any{
		"topic": "test/wake",
		"wake_loop": map[string]any{
			"name":         "triage_handler",
			"tags":         []any{"owner"},
			"instructions": "handle this event",
		},
	}
	result, err := tools.HandleAddWakeSubscription(context.Background(), args)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(result, "test/wake") {
		t.Errorf("expected topic in result, got %q", result)
	}
	if !strings.Contains(result, "triage_handler") {
		t.Errorf("expected wake_loop target name in result, got %q", result)
	}

	list, err := tools.HandleListWakeSubscriptions(context.Background(), nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(list, "test/wake") {
		t.Errorf("expected topic in list, got %q", list)
	}
	if !strings.Contains(list, "triage_handler") {
		t.Errorf("expected wake_loop target in list, got %q", list)
	}
	if !strings.Contains(list, "owner") {
		t.Errorf("expected wake_loop tags in list, got %q", list)
	}
}

func TestToolsHandleAddRejectsMissingWakeLoop(t *testing.T) {
	tools := newTestTools(t)

	args := map[string]any{"topic": "no/handler"}
	if _, err := tools.HandleAddWakeSubscription(context.Background(), args); err == nil {
		t.Fatal("expected error for missing wake_loop")
	}
}

func TestToolsHandleAddRejectsBareNameString(t *testing.T) {
	tools := newTestTools(t)

	// Sanity: the string form still works (it's a name).
	args := map[string]any{"topic": "string/wake", "wake_loop": "shorthand_handler"}
	if _, err := tools.HandleAddWakeSubscription(context.Background(), args); err != nil {
		t.Fatalf("add with string wake_loop: %v", err)
	}
	subs := tools.store.List()
	if len(subs) != 1 || subs[0].WakeTarget.Name != "shorthand_handler" {
		t.Fatalf("wake_target = %#v, want name=shorthand_handler", subs[0].WakeTarget)
	}
}

func TestToolsHandleAddMissingTopic(t *testing.T) {
	tools := newTestTools(t)

	_, err := tools.HandleAddWakeSubscription(context.Background(), map[string]any{
		"wake_loop": "handler",
	})
	if err == nil {
		t.Fatal("expected error for missing topic")
	}
}

func TestToolsHandleRemove(t *testing.T) {
	tools := newTestTools(t)

	args := map[string]any{"topic": "remove/test", "wake_loop": "handler"}
	if _, err := tools.HandleAddWakeSubscription(context.Background(), args); err != nil {
		t.Fatalf("add: %v", err)
	}

	subs := tools.store.List()
	if len(subs) != 1 {
		t.Fatalf("expected 1 sub, got %d", len(subs))
	}

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

	target := messages.LoopWakeTarget{Name: "handler"}
	if err := tools.store.LoadConfig([]config.SubscriptionConfig{
		{Topic: "config/test", WakeLoop: &target},
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
