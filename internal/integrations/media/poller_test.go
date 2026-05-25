package media

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
)

// newTestStore creates a temporary opstate store for testing.
func newTestStore(t *testing.T) *opstate.Store {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := opstate.NewStore(db, nil)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	return store
}

func TestCheckFeeds_NoFeeds(t *testing.T) {
	store := newTestStore(t)
	poller := NewFeedPoller(store, nil)

	msg, err := poller.CheckFeeds(context.Background())
	if err != nil {
		t.Fatalf("CheckFeeds() error: %v", err)
	}
	if msg != "" {
		t.Errorf("expected empty message, got %q", msg)
	}
}

func TestCheckFeeds_NewEntries(t *testing.T) {
	atomXML := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
	<title>Test Channel</title>
	<entry>
	  <id>vid-3</id><title>New Video</title>
	  <link href="https://example.com/vid3"/>
	  <published>2026-02-22T12:00:00Z</published>
	</entry>
	<entry>
	  <id>vid-2</id><title>Old Video</title>
	  <link href="https://example.com/vid2"/>
	  <published>2026-02-20T12:00:00Z</published>
	</entry></feed>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(atomXML))
	}))
	defer srv.Close()

	store := newTestStore(t)

	// Set up a feed with a high-water mark at vid-2.
	saveFeedIndex(store, []string{"test1"})
	store.Set(feedNamespace, feedKeyURL("test1"), srv.URL)
	store.Set(feedNamespace, feedKeyName("test1"), "Test Channel")
	store.Set(feedNamespace, feedKeyLastEntryID("test1"), "vid-2")

	poller := NewFeedPoller(store, nil)
	msg, err := poller.CheckFeeds(context.Background())
	if err != nil {
		t.Fatalf("CheckFeeds() error: %v", err)
	}
	if !strings.Contains(msg, "New Video") {
		t.Errorf("wake message should mention new video title, got: %q", msg)
	}
	if !strings.Contains(msg, "example.com/vid3") {
		t.Errorf("wake message should contain video URL, got: %q", msg)
	}
	if strings.Contains(msg, "Old Video") {
		t.Errorf("wake message should not contain old video")
	}
	// No trust_zone stored → should default to [unknown].
	if !strings.Contains(msg, "[unknown]") {
		t.Errorf("wake message should contain default trust zone [unknown], got: %q", msg)
	}
	// feed_id should be present for output path resolution.
	if !strings.Contains(msg, "(feed_id: test1)") {
		t.Errorf("wake message should contain feed_id, got: %q", msg)
	}

	// High-water mark should be updated.
	hwm, _ := store.Get(feedNamespace, feedKeyLastEntryID("test1"))
	if hwm != "vid-3" {
		t.Errorf("high-water mark = %q, want %q", hwm, "vid-3")
	}
}

func TestCheckFeeds_NoNewEntries(t *testing.T) {
	atomXML := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
	<title>Ch</title>
	<entry><id>vid-1</id><title>Video</title>
	<link href="https://example.com/1"/>
	<published>2026-02-20T12:00:00Z</published></entry></feed>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(atomXML))
	}))
	defer srv.Close()

	store := newTestStore(t)
	saveFeedIndex(store, []string{"f1"})
	store.Set(feedNamespace, feedKeyURL("f1"), srv.URL)
	store.Set(feedNamespace, feedKeyName("f1"), "Ch")
	store.Set(feedNamespace, feedKeyLastEntryID("f1"), "vid-1") // already seen

	poller := NewFeedPoller(store, nil)
	msg, err := poller.CheckFeeds(context.Background())
	if err != nil {
		t.Fatalf("CheckFeeds() error: %v", err)
	}
	if msg != "" {
		t.Errorf("expected empty message for no new entries, got %q", msg)
	}
}

func TestCheckFeeds_FirstRun(t *testing.T) {
	atomXML := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
	<title>Ch</title>
	<entry><id>vid-1</id><title>Latest</title>
	<link href="https://example.com/1"/>
	<published>2026-02-20T12:00:00Z</published></entry></feed>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(atomXML))
	}))
	defer srv.Close()

	store := newTestStore(t)
	saveFeedIndex(store, []string{"f1"})
	store.Set(feedNamespace, feedKeyURL("f1"), srv.URL)
	store.Set(feedNamespace, feedKeyName("f1"), "Ch")
	// No last_entry_id — first run.

	poller := NewFeedPoller(store, nil)
	msg, err := poller.CheckFeeds(context.Background())
	if err != nil {
		t.Fatalf("CheckFeeds() error: %v", err)
	}
	if msg != "" {
		t.Errorf("first run should not report entries, got %q", msg)
	}

	// High-water mark should be set.
	hwm, _ := store.Get(feedNamespace, feedKeyLastEntryID("f1"))
	if hwm != "vid-1" {
		t.Errorf("high-water mark = %q, want %q", hwm, "vid-1")
	}
}

func TestCheckFeeds_FetchError(t *testing.T) {
	// Feed 1: broken server. Feed 2: working.
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer badSrv.Close()

	goodXML := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
	<title>Good</title>
	<entry><id>new-1</id><title>New Entry</title>
	<link href="https://example.com/new"/>
	<published>2026-02-22T12:00:00Z</published></entry>
	<entry><id>old-1</id><title>Old Entry</title>
	<link href="https://example.com/old"/>
	<published>2026-02-20T12:00:00Z</published></entry></feed>`

	goodSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(goodXML))
	}))
	defer goodSrv.Close()

	store := newTestStore(t)
	saveFeedIndex(store, []string{"bad", "good"})
	store.Set(feedNamespace, feedKeyURL("bad"), badSrv.URL)
	store.Set(feedNamespace, feedKeyName("bad"), "Bad Feed")
	store.Set(feedNamespace, feedKeyLastEntryID("bad"), "x")
	store.Set(feedNamespace, feedKeyURL("good"), goodSrv.URL)
	store.Set(feedNamespace, feedKeyName("good"), "Good Feed")
	store.Set(feedNamespace, feedKeyLastEntryID("good"), "old-1")

	poller := NewFeedPoller(store, nil)
	msg, err := poller.CheckFeeds(context.Background())
	if err != nil {
		t.Fatalf("CheckFeeds() error: %v", err)
	}
	// Good feed should still be checked despite bad feed failing.
	if !strings.Contains(msg, "New Entry") {
		t.Errorf("good feed entries should appear in wake message, got: %q", msg)
	}
}

func TestCheckFeeds_MultipleNewEntries(t *testing.T) {
	atomXML := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
	<title>Ch</title>
	<entry><id>vid-4</id><title>Fourth</title>
	<link href="https://example.com/4"/><published>2026-02-24T12:00:00Z</published></entry>
	<entry><id>vid-3</id><title>Third</title>
	<link href="https://example.com/3"/><published>2026-02-23T12:00:00Z</published></entry>
	<entry><id>vid-2</id><title>Second</title>
	<link href="https://example.com/2"/><published>2026-02-22T12:00:00Z</published></entry></feed>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(atomXML))
	}))
	defer srv.Close()

	store := newTestStore(t)
	saveFeedIndex(store, []string{"f1"})
	store.Set(feedNamespace, feedKeyURL("f1"), srv.URL)
	store.Set(feedNamespace, feedKeyName("f1"), "Ch")
	store.Set(feedNamespace, feedKeyLastEntryID("f1"), "vid-2")

	poller := NewFeedPoller(store, nil)
	msg, err := poller.CheckFeeds(context.Background())
	if err != nil {
		t.Fatalf("CheckFeeds() error: %v", err)
	}
	if !strings.Contains(msg, "Fourth") {
		t.Errorf("should contain Fourth, got: %q", msg)
	}
	if !strings.Contains(msg, "Third") {
		t.Errorf("should contain Third, got: %q", msg)
	}
	if strings.Contains(msg, "Second") {
		t.Errorf("should not contain Second (already seen)")
	}
}

func TestCheckFeeds_TrustZoneInWakeMessage(t *testing.T) {
	atomXML := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
	<title>Trusted Source</title>
	<entry><id>ep-2</id><title>New Episode</title>
	<link href="https://example.com/ep2"/>
	<published>2026-02-22T12:00:00Z</published></entry>
	<entry><id>ep-1</id><title>Old Episode</title>
	<link href="https://example.com/ep1"/>
	<published>2026-02-20T12:00:00Z</published></entry></feed>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(atomXML))
	}))
	defer srv.Close()

	store := newTestStore(t)
	saveFeedIndex(store, []string{"tf1"})
	store.Set(feedNamespace, feedKeyURL("tf1"), srv.URL)
	store.Set(feedNamespace, feedKeyName("tf1"), "Trusted Source")
	store.Set(feedNamespace, feedKeyLastEntryID("tf1"), "ep-1")
	store.Set(feedNamespace, feedKeyTrustZone("tf1"), "trusted")

	poller := NewFeedPoller(store, nil)
	msg, err := poller.CheckFeeds(context.Background())
	if err != nil {
		t.Fatalf("CheckFeeds() error: %v", err)
	}
	if !strings.Contains(msg, "[trusted]") {
		t.Errorf("wake message should contain [trusted], got: %q", msg)
	}
	if !strings.Contains(msg, "Trusted Source") {
		t.Errorf("wake message should contain feed name, got: %q", msg)
	}
	// Verify format: **Name** [zone] (feed_id: ID): Title
	if !strings.Contains(msg, "**Trusted Source** [trusted] (feed_id: tf1): New Episode") {
		t.Errorf("wake message format incorrect, got: %q", msg)
	}
}

func TestCheckFeeds_WakeLoopDispatchesStructuredEvents(t *testing.T) {
	atomXML := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
	<title>Curated Source</title>
	<entry><id>ep-2</id><title>New Episode</title>
	<link href="https://example.com/ep2"/>
	<published>2026-02-22T12:00:00Z</published></entry>
	<entry><id>ep-1</id><title>Old Episode</title>
	<link href="https://example.com/ep1"/>
	<published>2026-02-20T12:00:00Z</published></entry></feed>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(atomXML))
	}))
	defer srv.Close()

	store := newTestStore(t)
	saveFeedIndex(store, []string{"cf1"})
	store.Set(feedNamespace, feedKeyURL("cf1"), srv.URL)
	store.Set(feedNamespace, feedKeyName("cf1"), "Curated Source")
	store.Set(feedNamespace, feedKeyLastEntryID("cf1"), "ep-1")
	store.Set(feedNamespace, feedKeyTrustZone("cf1"), "trusted")
	if err := storeFeedWakeTarget(store, "cf1", messages.LoopWakeTarget{Name: "feed_curator", ForceSupervisor: true}, true); err != nil {
		t.Fatalf("storeFeedWakeTarget: %v", err)
	}

	var delivered messages.Envelope
	bus := messages.NewBus(nil)
	bus.RegisterRoute(messages.DestinationLoop, func(_ context.Context, env messages.Envelope) (messages.DeliveryResult, error) {
		delivered = env
		return messages.DeliveryResult{Route: "test", Status: messages.DeliveryDelivered}, nil
	})

	poller := NewFeedPoller(store, nil, WithFeedMessageBus(bus))
	msg, err := poller.CheckFeeds(context.Background())
	if err != nil {
		t.Fatalf("CheckFeeds() error: %v", err)
	}
	if msg != "" {
		t.Fatalf("legacy wake message = %q, want empty when wake_loop dispatches", msg)
	}
	if delivered.To.Target != "feed_curator" {
		t.Fatalf("delivered target = %q, want feed_curator", delivered.To.Target)
	}
	payload, ok := delivered.Payload.(messages.LoopNotifyPayload)
	if !ok {
		t.Fatalf("payload type = %T, want LoopNotifyPayload", delivered.Payload)
	}
	if !payload.ForceSupervisor {
		t.Fatal("expected force_supervisor payload")
	}
	if len(payload.Events) != 1 {
		t.Fatalf("events len = %d, want 1", len(payload.Events))
	}
	if payload.Events[0].Type != "feed_entry" || payload.Events[0].Metadata["feed_id"] != "cf1" {
		t.Fatalf("event = %+v", payload.Events[0])
	}
	hwm, _ := store.Get(feedNamespace, feedKeyLastEntryID("cf1"))
	if hwm != "ep-2" {
		t.Fatalf("high-water mark = %q, want ep-2 after wake delivery", hwm)
	}
}

func TestCheckFeeds_WakeLoopDeliveryFailureKeepsHighWater(t *testing.T) {
	atomXML := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
	<title>Curated Source</title>
	<entry><id>ep-2</id><title>New Episode</title>
	<link href="https://example.com/ep2"/>
	<published>2026-02-22T12:00:00Z</published></entry>
	<entry><id>ep-1</id><title>Old Episode</title>
	<link href="https://example.com/ep1"/>
	<published>2026-02-20T12:00:00Z</published></entry></feed>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(atomXML))
	}))
	defer srv.Close()

	store := newTestStore(t)
	saveFeedIndex(store, []string{"cf1"})
	store.Set(feedNamespace, feedKeyURL("cf1"), srv.URL)
	store.Set(feedNamespace, feedKeyName("cf1"), "Curated Source")
	store.Set(feedNamespace, feedKeyLastEntryID("cf1"), "ep-1")
	if err := storeFeedWakeTarget(store, "cf1", messages.LoopWakeTarget{Name: "feed_curator"}, true); err != nil {
		t.Fatalf("storeFeedWakeTarget: %v", err)
	}

	poller := NewFeedPoller(store, nil, WithFeedMessageBus(messages.NewBus(nil)))
	msg, err := poller.CheckFeeds(context.Background())
	if err != nil {
		t.Fatalf("CheckFeeds() error: %v", err)
	}
	if msg != "" {
		t.Fatalf("legacy wake message = %q, want empty when wake_loop dispatch fails", msg)
	}
	hwm, _ := store.Get(feedNamespace, feedKeyLastEntryID("cf1"))
	if hwm != "ep-1" {
		t.Fatalf("high-water mark = %q, want ep-1 after failed wake delivery", hwm)
	}
}

// TestStoreFeedWakeTargetRoundTripsTags pins the P3 fix: Tags on a
// feed's wake target persist alongside the other fields and round-trip
// through loadFeedWakeTarget. Pre-fix, feed_wake.go's storage helpers
// ignored target.Tags entirely.
func TestStoreFeedWakeTargetRoundTripsTags(t *testing.T) {
	store := newTestStore(t)

	target := messages.LoopWakeTarget{
		Name: "feed_curator",
		Tags: []string{"owner", "research"},
	}
	if err := storeFeedWakeTarget(store, "cf1", target, true); err != nil {
		t.Fatalf("storeFeedWakeTarget: %v", err)
	}
	got, ok, err := loadFeedWakeTarget(store, "cf1")
	if err != nil || !ok {
		t.Fatalf("loadFeedWakeTarget ok=%v err=%v", ok, err)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "owner" || got.Tags[1] != "research" {
		t.Fatalf("round-tripped Tags = %v, want [owner research]", got.Tags)
	}

	// JSON projection also surfaces Tags so list-style tool responses
	// show what the operator configured.
	jsonProj := feedWakeTargetJSON(got, true)
	tagsRaw, ok := jsonProj["tags"]
	if !ok {
		t.Fatal("feedWakeTargetJSON dropped tags")
	}
	tags, ok := tagsRaw.([]string)
	if !ok {
		t.Fatalf("feedWakeTargetJSON tags type = %T, want []string", tagsRaw)
	}
	if len(tags) != 2 || tags[0] != "owner" || tags[1] != "research" {
		t.Fatalf("feedWakeTargetJSON tags = %v, want [owner research]", tags)
	}
}

func TestStoreFeedWakeTargetClearsWhenOmitted(t *testing.T) {
	store := newTestStore(t)

	if err := storeFeedWakeTarget(store, "cf1", messages.LoopWakeTarget{Name: "feed_curator"}, true); err != nil {
		t.Fatalf("storeFeedWakeTarget configured: %v", err)
	}
	if _, ok, err := loadFeedWakeTarget(store, "cf1"); err != nil || !ok {
		t.Fatalf("load configured wake target ok=%v err=%v", ok, err)
	}

	if err := storeFeedWakeTarget(store, "cf1", messages.LoopWakeTarget{}, false); err != nil {
		t.Fatalf("storeFeedWakeTarget omitted: %v", err)
	}
	if got, ok, err := loadFeedWakeTarget(store, "cf1"); err != nil || ok {
		t.Fatalf("load wake target after clear = %+v ok=%v err=%v, want none", got, ok, err)
	}
}

func TestCheckFeeds_WakeLoopBatchesAndAdvancesHighWaterPerBatch(t *testing.T) {
	total := messages.MaxLoopEventsPerWake + 2
	var atom strings.Builder
	atom.WriteString(`<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Curated Source</title>`)
	for i := total; i >= 1; i-- {
		fmt.Fprintf(&atom, `<entry><id>ep-%02d</id><title>Episode %02d</title><link href="https://example.com/ep-%02d"/><published>2026-02-22T12:%02d:00Z</published></entry>`, i, i, i, i%60)
	}
	atom.WriteString(`<entry><id>ep-old</id><title>Old Episode</title><link href="https://example.com/old"/><published>2026-02-20T12:00:00Z</published></entry></feed>`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(atom.String()))
	}))
	defer srv.Close()

	store := newTestStore(t)
	saveFeedIndex(store, []string{"cf1"})
	store.Set(feedNamespace, feedKeyURL("cf1"), srv.URL)
	store.Set(feedNamespace, feedKeyName("cf1"), "Curated Source")
	store.Set(feedNamespace, feedKeyLastEntryID("cf1"), "ep-old")
	if err := storeFeedWakeTarget(store, "cf1", messages.LoopWakeTarget{Name: "feed_curator"}, true); err != nil {
		t.Fatalf("storeFeedWakeTarget: %v", err)
	}

	sendCount := 0
	var firstBatch messages.Envelope
	bus := messages.NewBus(nil)
	bus.RegisterRoute(messages.DestinationLoop, func(_ context.Context, env messages.Envelope) (messages.DeliveryResult, error) {
		sendCount++
		if sendCount == 1 {
			firstBatch = env
			return messages.DeliveryResult{Route: "test", Status: messages.DeliveryDelivered}, nil
		}
		return messages.DeliveryResult{}, errors.New("second batch failed")
	})

	poller := NewFeedPoller(store, nil, WithFeedMessageBus(bus))
	msg, err := poller.CheckFeeds(context.Background())
	if err != nil {
		t.Fatalf("CheckFeeds() error: %v", err)
	}
	if msg != "" {
		t.Fatalf("legacy wake message = %q, want empty when wake_loop dispatches", msg)
	}
	if sendCount != 2 {
		t.Fatalf("send count = %d, want 2", sendCount)
	}
	payload, ok := firstBatch.Payload.(messages.LoopNotifyPayload)
	if !ok {
		t.Fatalf("payload type = %T, want LoopNotifyPayload", firstBatch.Payload)
	}
	if len(payload.Events) != messages.MaxLoopEventsPerWake {
		t.Fatalf("first batch events len = %d, want %d", len(payload.Events), messages.MaxLoopEventsPerWake)
	}
	if payload.Events[0].ID != "ep-50" {
		t.Fatalf("first delivered high-water event = %q, want ep-50", payload.Events[0].ID)
	}
	hwm, _ := store.Get(feedNamespace, feedKeyLastEntryID("cf1"))
	if hwm != "ep-50" {
		t.Fatalf("high-water mark = %q, want ep-50 after first batch only", hwm)
	}
}

func TestCheckFeeds_ReseedOnMissingHighWaterMark(t *testing.T) {
	// Feed no longer contains the previous high-water mark entry (feed
	// dropped old items). The poller should reseed without reporting entries.
	atomXML := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
	<title>Ch</title>
	<entry><id>vid-10</id><title>Newest</title>
	<link href="https://example.com/10"/>
	<published>2026-02-24T12:00:00Z</published></entry>
	<entry><id>vid-9</id><title>Previous</title>
	<link href="https://example.com/9"/>
	<published>2026-02-23T12:00:00Z</published></entry></feed>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(atomXML))
	}))
	defer srv.Close()

	store := newTestStore(t)
	saveFeedIndex(store, []string{"f1"})
	store.Set(feedNamespace, feedKeyURL("f1"), srv.URL)
	store.Set(feedNamespace, feedKeyName("f1"), "Ch")
	store.Set(feedNamespace, feedKeyLastEntryID("f1"), "vid-1") // no longer in feed

	poller := NewFeedPoller(store, nil)
	msg, err := poller.CheckFeeds(context.Background())
	if err != nil {
		t.Fatalf("CheckFeeds() error: %v", err)
	}
	if msg != "" {
		t.Errorf("reseed should not report entries, got %q", msg)
	}

	// High-water mark should be reseeded to latest.
	hwm, _ := store.Get(feedNamespace, feedKeyLastEntryID("f1"))
	if hwm != "vid-10" {
		t.Errorf("high-water mark = %q, want %q (reseeded)", hwm, "vid-10")
	}
}
