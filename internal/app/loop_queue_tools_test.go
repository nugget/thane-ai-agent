package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

func TestQueueDeferToolRetainsWorkAndMovesItBehindThePartition(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := loopqueue.NewStore(db, nil)
	if err != nil {
		t.Fatalf("new loop queue: %v", err)
	}
	if err := store.Enqueue(t.Context(), "archivist", "contact:blocked", 5, []byte(`{"source":"session"}`)); err != nil {
		t.Fatalf("enqueue blocked: %v", err)
	}
	if err := store.Enqueue(t.Context(), "archivist", "session:ready", 0, []byte(`{}`)); err != nil {
		t.Fatalf("enqueue ready: %v", err)
	}

	tools := buildLoopQueueTools(store, "archivist")
	var deferToolNameFound bool
	for _, tool := range tools {
		if tool.Name != "queue_defer" {
			continue
		}
		deferToolNameFound = true
		result, err := tool.Handler(context.Background(), map[string]any{"subject": "contact:blocked"})
		if err != nil {
			t.Fatalf("queue_defer: %v", err)
		}
		var response map[string]string
		if err := json.Unmarshal([]byte(result), &response); err != nil {
			t.Fatalf("decode queue_defer result: %v", err)
		}
		if response["status"] != "deferred" || response["subject"] != "contact:blocked" {
			t.Fatalf("queue_defer result = %v", response)
		}
	}
	if !deferToolNameFound {
		t.Fatal("queue_defer runtime tool was not generated")
	}

	items, err := store.Peek(t.Context(), "archivist", 10)
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if len(items) != 2 || items[0].DedupKey != "session:ready" || items[1].DedupKey != "contact:blocked" {
		t.Fatalf("deferred queue order = %#v", items)
	}
	if got := string(items[1].Payload); got != `{"source":"session"}` {
		t.Fatalf("deferred payload = %q", got)
	}
}

func TestQueueAckToolRetainsNewerCoalescedGeneration(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := loopqueue.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	const subject = "contact:changing"
	if err := store.Enqueue(t.Context(), "archivist", subject, 0, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}

	tools := buildLoopQueueTools(store, "archivist")
	var pull, ack func(context.Context, map[string]any) (string, error)
	for _, tool := range tools {
		switch tool.Name {
		case "queue_pull":
			pull = tool.Handler
		case "queue_ack":
			ack = tool.Handler
		}
	}
	if pull == nil || ack == nil {
		t.Fatal("queue pull/ack tools not generated")
	}
	if _, err := pull(t.Context(), map[string]any{"limit": 1}); err != nil {
		t.Fatalf("queue_pull: %v", err)
	}
	if err := store.Enqueue(t.Context(), "archivist", subject, 0, []byte(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}
	result, err := ack(t.Context(), map[string]any{"subject": subject})
	if err != nil {
		t.Fatalf("queue_ack: %v", err)
	}
	var response map[string]string
	if err := json.Unmarshal([]byte(result), &response); err != nil {
		t.Fatal(err)
	}
	if response["status"] != "retained_newer" {
		t.Fatalf("queue_ack response = %v, want retained_newer", response)
	}
	items, err := store.Peek(t.Context(), "archivist", 1)
	if err != nil || len(items) != 1 || string(items[0].Payload) != `{"v":2}` {
		t.Fatalf("newer item was not retained: items=%#v err=%v", items, err)
	}
}
