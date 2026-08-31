package tools

import (
	"context"
	"reflect"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

func TestContactSaveToolStampsCurrentTurnProvenance(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := contacts.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	var mutation contacts.ContactMutation
	contactTools := contacts.NewTools(store, func(_ context.Context, got contacts.ContactMutation) error {
		mutation = got
		return nil
	})
	registry := NewEmptyRegistry()
	registry.SetContactTools(contactTools)

	ctx := context.Background()
	ctx = WithModel(ctx, "test-model")
	ctx = WithLoopID(ctx, "loop-archivist-1")
	ctx = WithConversationID(ctx, "loop-archivist-1-123")
	ctx = WithSessionID(ctx, "session-1")
	ctx = WithRequestID(ctx, "request-1")
	ctx = WithToolCallID(ctx, "tool-call-1")
	ctx = WithIterationIndex(ctx, 2)
	if _, err := registry.Get("contact_save").Handler(ctx, map[string]any{
		"name": "Turn Provenance",
		"kind": "individual",
		"facts": map[string]any{
			"email": "turn@example.com",
		},
	}); err != nil {
		t.Fatalf("contact_save handler: %v", err)
	}

	iteration := 2
	want := &contacts.PropertyProvenance{
		Source:         "contact_save",
		Model:          "test-model",
		LoopID:         "loop-archivist-1",
		ConversationID: "loop-archivist-1-123",
		SessionID:      "session-1",
		RequestID:      "request-1",
		ToolCallID:     "tool-call-1",
		Iteration:      &iteration,
	}
	if !reflect.DeepEqual(mutation.Provenance, want) {
		t.Fatalf("mutation provenance = %#v, want %#v", mutation.Provenance, want)
	}
	properties, err := store.GetProperties(mutation.ContactID)
	if err != nil {
		t.Fatal(err)
	}
	if len(properties) != 1 || !reflect.DeepEqual(properties[0].Provenance, want) {
		t.Fatalf("stored property provenance = %#v, want %#v", properties, want)
	}
}
