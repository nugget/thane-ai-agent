package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/runtime/archivist"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"

	_ "modernc.org/sqlite"
)

// TestArchivistDefinitionEnabledGatesQueueWiring pins the single-gate
// contract: the session-close producer wires exactly when the archivist's
// effective loop definition exists and is enabled. The legacy config flag
// must be irrelevant — before this gate existed, config said whether the
// queue filled while the definition said whether the loop ran, and the two
// could disagree in only one direction: silently.
func TestArchivistDefinitionEnabledGatesQueueWiring(t *testing.T) {
	archivistSpec := func(enabled bool) looppkg.Spec {
		return looppkg.Spec{
			Name:      archivist.DefinitionName,
			Enabled:   enabled,
			Task:      "archive things",
			Operation: looppkg.OperationService,
		}
	}
	registryWith := func(t *testing.T, specs ...looppkg.Spec) *looppkg.DefinitionRegistry {
		t.Helper()
		reg, err := looppkg.NewDefinitionRegistry(specs)
		if err != nil {
			t.Fatalf("NewDefinitionRegistry: %v", err)
		}
		return reg
	}

	t.Run("enabled definition wires, config flag not consulted", func(t *testing.T) {
		a := &App{
			loopDefinitionRegistry: registryWith(t, archivistSpec(true)),
			// The legacy flag says off; the definition says on. The
			// definition wins because it is the same declaration that
			// runs the loop.
			cfg: &config.Config{},
		}
		if !a.archivistDefinitionEnabled() {
			t.Error("enabled definition should gate the wiring on, regardless of config.archivist.enabled")
		}
	})

	t.Run("disabled definition does not wire", func(t *testing.T) {
		a := &App{loopDefinitionRegistry: registryWith(t, archivistSpec(false))}
		if a.archivistDefinitionEnabled() {
			t.Error("disabled definition should gate the wiring off")
		}
	})

	t.Run("absent definition does not wire", func(t *testing.T) {
		a := &App{loopDefinitionRegistry: registryWith(t)}
		if a.archivistDefinitionEnabled() {
			t.Error("absent definition should gate the wiring off")
		}
	})

	t.Run("nil registry does not wire", func(t *testing.T) {
		a := &App{}
		if a.archivistDefinitionEnabled() {
			t.Error("nil registry should gate the wiring off")
		}
	})
}

func newQueueTestApp(t *testing.T) (*App, *loopqueue.Store) {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := loopqueue.NewStore(db, nil)
	if err != nil {
		t.Fatalf("new queue store: %v", err)
	}
	return &App{loopQueue: store}, store
}

// TestEnqueueSessionCloseWork verifies a closed session becomes a pending
// archivist work item keyed dedup on session:<id> — and is NOT delivered
// as a loop notification (the decoupling fix, #1024).
func TestEnqueueSessionCloseWork(t *testing.T) {
	a, store := newQueueTestApp(t)
	const sessionID = "019e6867-00fc-7d6d-88be-58fab5c173c4"
	const convID = "signal-15125551234" // interactive origin → archivable

	if err := a.enqueueSessionCloseWork(context.Background(), sessionID, convID, "idle_timeout"); err != nil {
		t.Fatalf("enqueueSessionCloseWork: %v", err)
	}

	items, err := store.Peek(context.Background(), archivist.DefinitionName, 10)
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("archivist queue has %d items, want 1", len(items))
	}
	if items[0].DedupKey != "session:"+sessionID {
		t.Errorf("dedup_key = %q, want session:%s", items[0].DedupKey, sessionID)
	}
	source, _ := projectQueuePayload(items[0].Payload)
	if source != "session_close" {
		t.Errorf("payload source = %q, want session_close", source)
	}

	// Re-enqueue of the same session coalesces (dedup), not duplicates.
	if err := a.enqueueSessionCloseWork(context.Background(), sessionID, convID, "again"); err != nil {
		t.Fatalf("re-enqueue: %v", err)
	}
	if n, _ := store.PendingCount(context.Background(), archivist.DefinitionName); n != 1 {
		t.Errorf("pending after re-enqueue = %d, want 1 (coalesced)", n)
	}
}

func TestEnqueueSessionCloseWork_EmptySessionID(t *testing.T) {
	a, _ := newQueueTestApp(t)
	if err := a.enqueueSessionCloseWork(context.Background(), "", "signal-x", "x"); err == nil {
		t.Fatal("enqueueSessionCloseWork with empty session_id should error")
	}
}

func TestEnqueueContactDossierRefreshCoalescesByContact(t *testing.T) {
	a, store := newQueueTestApp(t)
	id := uuid.MustParse("019c76e4-2ff1-7918-8d6f-6c2488f5098d")
	iteration := 4
	mutation := contacts.ContactMutation{
		ContactID:   id,
		ContactName: "Contact Person",
		Created:     false,
		Fields:      []string{"ai_summary", "property:EMAIL"},
		Provenance: &contacts.PropertyProvenance{
			Source:         "contact_save",
			Model:          "test-model",
			LoopID:         "loop-1",
			ConversationID: "conversation-1",
			SessionID:      "session-1",
			RequestID:      "request-1",
			ToolCallID:     "tool-call-1",
			Iteration:      &iteration,
		},
	}
	if err := a.enqueueContactDossierRefresh(context.Background(), mutation); err != nil {
		t.Fatalf("enqueueContactDossierRefresh: %v", err)
	}

	items, err := store.Peek(context.Background(), archivist.DefinitionName, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].DedupKey != "contact:"+id.String() {
		t.Fatalf("queue items = %#v, want one contact-keyed item", items)
	}
	var payload messages.LoopNotifyPayload
	if err := json.Unmarshal(items[0].Payload, &payload); err != nil {
		t.Fatalf("decode queue payload: %v", err)
	}
	if len(payload.Events) != 1 {
		t.Fatalf("events = %#v, want 1", payload.Events)
	}
	event := payload.Events[0]
	if event.Source != "contact_save" || event.Type != "structured_contact_changed" || event.ID != id.String() {
		t.Errorf("event identity = %#v", event)
	}
	for _, want := range []string{`"Contact Person"`, "ai_summary, property:EMAIL", "exact name"} {
		if !strings.Contains(event.Summary, want) {
			t.Errorf("event summary = %q, want %q", event.Summary, want)
		}
	}
	if !event.ObservedAt.IsZero() {
		t.Errorf("contact refresh exposed scan time as observed_at: %v", event.ObservedAt)
	}
	for key, want := range map[string]string{
		"contact_id": id.String(), "contact_name": "Contact Person",
		"created": "false", "fields": "ai_summary,property:EMAIL",
		"source": "contact_save", "model": "test-model", "loop_id": "loop-1",
		"conversation_id": "conversation-1", "session_id": "session-1",
		"request_id": "request-1", "tool_call_id": "tool-call-1", "iteration": "4",
	} {
		if got := event.Metadata[key]; got != want {
			t.Errorf("metadata[%q] = %q, want %q", key, got, want)
		}
	}

	mutation.Fields = []string{"note"}
	if err := a.enqueueContactDossierRefresh(context.Background(), mutation); err != nil {
		t.Fatalf("re-enqueue contact refresh: %v", err)
	}
	if n, _ := store.PendingCount(context.Background(), archivist.DefinitionName); n != 1 {
		t.Fatalf("pending contact refreshes = %d, want 1 coalesced item", n)
	}
	items, err = store.Peek(context.Background(), archivist.DefinitionName, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, summary := projectQueuePayload(items[0].Payload); summary == "" {
		t.Error("coalesced contact refresh lost its model-facing summary")
	}
}

func TestEnqueueContactDossierRefreshSurvivesRequestCancellation(t *testing.T) {
	a, store := newQueueTestApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	id := uuid.MustParse("019c76e4-2ff1-7918-8d6f-6c2488f5098d")
	err := a.enqueueContactDossierRefresh(ctx, contacts.ContactMutation{
		ContactID:   id,
		ContactName: "Canceled Request Contact",
		Fields:      []string{"note"},
	})
	if err != nil {
		t.Fatalf("detached contact refresh enqueue: %v", err)
	}
	items, err := store.Peek(context.Background(), archivist.DefinitionName, 1)
	if err != nil || len(items) != 1 || items[0].DedupKey != "contact:"+id.String() {
		t.Fatalf("durable refresh after cancellation: items=%#v err=%v", items, err)
	}
}

// TestEnqueueSessionCloseWork_SkipsAutomationOrigins verifies the archival
// policy (issue #1024): sessions from autonomous/automation/auxiliary origins
// are not enqueued for the archivist, so it isn't drowned in service-loop and
// scheduled-task bookkeeping — while an interactive origin still enqueues.
func TestEnqueueSessionCloseWork_SkipsAutomationOrigins(t *testing.T) {
	a, store := newQueueTestApp(t)
	for _, convID := range []string{
		"loop-metacognitive-3-1780000000000",
		"sched-019c8366-b115-7203-88f7-b765f7c068be-019d6487",
		"metacog-abc",
		"owu-auxiliary",
	} {
		if err := a.enqueueSessionCloseWork(context.Background(), "sess-"+convID, convID, "idle_timeout"); err != nil {
			t.Fatalf("enqueue %s: %v", convID, err)
		}
	}
	if n, _ := store.PendingCount(context.Background(), archivist.DefinitionName); n != 0 {
		t.Errorf("automation-origin sessions enqueued %d items, want 0", n)
	}

	if err := a.enqueueSessionCloseWork(context.Background(), "sess-real", "signal-15125551234", "idle_timeout"); err != nil {
		t.Fatalf("enqueue interactive: %v", err)
	}
	if n, _ := store.PendingCount(context.Background(), archivist.DefinitionName); n != 1 {
		t.Errorf("after interactive enqueue, pending = %d, want 1", n)
	}
}

func TestIsArchivableSession(t *testing.T) {
	cases := []struct {
		conv string
		want bool
	}{
		{"signal-15125551234", true},
		{"email-handler-1", true},
		{"delegate-abc", true},
		{"media-feed-1", true},
		{"owu-a1b2c3d4e5f6a7b8", true}, // real OWU chat (owu-<hash>) is substantive
		{"", true},                     // unknown/empty origin defaults archivable (don't drop substance)
		{"loop-metacognitive-1-2", false},
		{"sched-task-exec", false},
		{"metacog-1", false},
		{"owu-auxiliary", false}, // only the fixed auxiliary id is skipped
	}
	for _, tc := range cases {
		if got := archivist.IsArchivableSession(tc.conv); got != tc.want {
			t.Errorf("archivist.IsArchivableSession(%q) = %v, want %v", tc.conv, got, tc.want)
		}
	}
}
