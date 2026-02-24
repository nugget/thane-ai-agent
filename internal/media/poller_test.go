package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/opstate"
)

// newTestStore creates a temporary opstate store for testing.
func newTestStore(t *testing.T) *opstate.Store {
	t.Helper()
	dir := t.TempDir()
	store, err := opstate.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	t.Cleanup(func() { store.Close() })
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
