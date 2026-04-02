package homeassistant

import (
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/database"

	_ "github.com/mattn/go-sqlite3"
)

func newTestWakeTools(t *testing.T) *WakeTools {
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
	return NewWakeTools(store)
}

func TestWakeTools_Create(t *testing.T) {
	tools := newTestWakeTools(t)

	result, err := tools.Execute("create_anticipation", map[string]any{
		"topic":            "thane/test/motion",
		"name":             "Garage motion",
		"kb_ref":           "routines/security.md",
		"context":          "Check the cameras",
		"context_entities": []any{"binary_sensor.garage_motion"},
		"quality_floor":    float64(7),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if !strings.Contains(result, "Garage motion") {
		t.Errorf("result missing name: %s", result)
	}
	if !strings.Contains(result, "thane/test/motion") {
		t.Errorf("result missing topic: %s", result)
	}
	if !strings.Contains(result, "routines/security.md") {
		t.Errorf("result missing kb_ref: %s", result)
	}
}

func TestWakeTools_Create_MissingRequired(t *testing.T) {
	tools := newTestWakeTools(t)

	if _, err := tools.Execute("create_anticipation", map[string]any{
		"name": "No topic",
	}); err == nil {
		t.Error("expected error for missing topic")
	}

	if _, err := tools.Execute("create_anticipation", map[string]any{
		"topic": "thane/test/x",
	}); err == nil {
		t.Error("expected error for missing name")
	}
}

func TestWakeTools_List(t *testing.T) {
	tools := newTestWakeTools(t)

	// Empty list.
	result, err := tools.Execute("list_anticipations", nil)
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if !strings.Contains(result, "No active") {
		t.Errorf("expected empty message, got: %s", result)
	}

	// Create one and list again.
	if _, err := tools.Execute("create_anticipation", map[string]any{
		"topic":   "thane/test/motion",
		"name":    "Garage motion",
		"kb_ref":  "security.md",
		"context": "Check cameras",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	result, err = tools.Execute("list_anticipations", nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(result, "Garage motion") {
		t.Errorf("result missing name: %s", result)
	}
	if !strings.Contains(result, "Fires: 0") {
		t.Errorf("result missing fire count: %s", result)
	}
	if !strings.Contains(result, "Rate: 0.0/day") {
		t.Errorf("result missing rate: %s", result)
	}
}

func TestWakeTools_Cancel(t *testing.T) {
	tools := newTestWakeTools(t)

	result, err := tools.Execute("create_anticipation", map[string]any{
		"topic": "thane/test/cancel",
		"name":  "To cancel",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Extract ID from result.
	id := extractID(result)
	if id == "" {
		t.Fatalf("could not extract ID from: %s", result)
	}

	cancelResult, err := tools.Execute("cancel_anticipation", map[string]any{
		"id": id,
	})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if !strings.Contains(cancelResult, "Cancelled") {
		t.Errorf("expected cancelled message, got: %s", cancelResult)
	}

	// Verify it's gone from the list.
	listResult, err := tools.Execute("list_anticipations", nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(listResult, "No active") {
		t.Errorf("expected empty list after cancel, got: %s", listResult)
	}
}

func TestWakeTools_Update(t *testing.T) {
	tools := newTestWakeTools(t)

	result, err := tools.Execute("create_anticipation", map[string]any{
		"topic": "thane/test/original",
		"name":  "Original",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	id := extractID(result)

	updateResult, err := tools.Execute("update_anticipation", map[string]any{
		"id":   id,
		"name": "Updated",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !strings.Contains(updateResult, "Updated") {
		t.Errorf("expected updated message, got: %s", updateResult)
	}
}

func TestIsWakeTool(t *testing.T) {
	for _, name := range []string{"create_anticipation", "list_anticipations", "update_anticipation", "cancel_anticipation"} {
		if !IsWakeTool(name) {
			t.Errorf("IsWakeTool(%q) = false, want true", name)
		}
	}
	if IsWakeTool("unknown_tool") {
		t.Error("IsWakeTool(unknown_tool) = true, want false")
	}
}

// extractID extracts the wake subscription ID from a create result string.
func extractID(result string) string {
	for _, line := range strings.Split(result, "\n") {
		if strings.HasPrefix(line, "ID: ") {
			return strings.TrimPrefix(line, "ID: ")
		}
	}
	return ""
}
