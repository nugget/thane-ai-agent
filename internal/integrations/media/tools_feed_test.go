package media

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFollowHandler_TrustZoneDefault(t *testing.T) {
	atomXML := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
	<title>Default Zone Feed</title>
	<entry><id>e1</id><title>First</title>
	<link href="https://example.com/1"/>
	<published>2026-02-20T12:00:00Z</published></entry></feed>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(atomXML))
	}))
	defer srv.Close()

	store := newTestStore(t)
	ft := NewFeedTools(store, nil, 10)
	handler := ft.FollowHandler()

	result, err := handler(context.Background(), map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("FollowHandler() error: %v", err)
	}

	var out map[string]string
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if out["trust_zone"] != "unknown" {
		t.Errorf("trust_zone = %q, want %q (default)", out["trust_zone"], "unknown")
	}

	// Verify stored in opstate.
	id := out["feed_id"]
	stored, _ := store.Get(feedNamespace, feedKeyTrustZone(id))
	if stored != "unknown" {
		t.Errorf("stored trust_zone = %q, want %q", stored, "unknown")
	}
}

func TestFollowHandler_TrustZoneExplicit(t *testing.T) {
	atomXML := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
	<title>Trusted Feed</title>
	<entry><id>e1</id><title>First</title>
	<link href="https://example.com/1"/>
	<published>2026-02-20T12:00:00Z</published></entry></feed>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(atomXML))
	}))
	defer srv.Close()

	store := newTestStore(t)
	ft := NewFeedTools(store, nil, 10)
	handler := ft.FollowHandler()

	result, err := handler(context.Background(), map[string]any{
		"url":        srv.URL,
		"trust_zone": "trusted",
	})
	if err != nil {
		t.Fatalf("FollowHandler() error: %v", err)
	}

	var out map[string]string
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if out["trust_zone"] != "trusted" {
		t.Errorf("trust_zone = %q, want %q", out["trust_zone"], "trusted")
	}

	// Verify stored in opstate.
	id := out["feed_id"]
	stored, _ := store.Get(feedNamespace, feedKeyTrustZone(id))
	if stored != "trusted" {
		t.Errorf("stored trust_zone = %q, want %q", stored, "trusted")
	}
}

func TestFollowHandler_TrustZoneInvalid(t *testing.T) {
	store := newTestStore(t)
	ft := NewFeedTools(store, nil, 10)
	handler := ft.FollowHandler()

	_, err := handler(context.Background(), map[string]any{
		"url":        "https://example.com/feed.xml",
		"trust_zone": "admin",
	})
	if err == nil {
		t.Fatal("expected error for invalid trust_zone")
	}
}

func TestFollowHandler_TrustZoneKnown(t *testing.T) {
	atomXML := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
	<title>Known Feed</title>
	<entry><id>e1</id><title>First</title>
	<link href="https://example.com/1"/>
	<published>2026-02-20T12:00:00Z</published></entry></feed>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(atomXML))
	}))
	defer srv.Close()

	store := newTestStore(t)
	ft := NewFeedTools(store, nil, 10)
	handler := ft.FollowHandler()

	result, err := handler(context.Background(), map[string]any{
		"url":        srv.URL,
		"trust_zone": "known",
	})
	if err != nil {
		t.Fatalf("FollowHandler() error: %v", err)
	}

	var out map[string]string
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if out["trust_zone"] != "known" {
		t.Errorf("trust_zone = %q, want %q", out["trust_zone"], "known")
	}
}

func TestUnfollowHandler_CleansTrustZone(t *testing.T) {
	store := newTestStore(t)

	// Manually set up a feed.
	id := "testfeed1"
	saveFeedIndex(store, []string{id})
	store.Set(feedNamespace, feedKeyURL(id), "https://example.com/feed.xml")
	store.Set(feedNamespace, feedKeyName(id), "Test Feed")
	store.Set(feedNamespace, feedKeyNotify(id), "true")
	store.Set(feedNamespace, feedKeyTrustZone(id), "trusted")

	ft := NewFeedTools(store, nil, 10)
	handler := ft.UnfollowHandler()

	_, err := handler(context.Background(), map[string]any{"feed_id": id})
	if err != nil {
		t.Fatalf("UnfollowHandler() error: %v", err)
	}

	// Verify trust_zone was cleaned up.
	val, _ := store.Get(feedNamespace, feedKeyTrustZone(id))
	if val != "" {
		t.Errorf("trust_zone should be deleted after unfollow, got %q", val)
	}
}

func TestFeedsHandler_IncludesTrustZone(t *testing.T) {
	store := newTestStore(t)

	// Set up two feeds with different trust zones.
	saveFeedIndex(store, []string{"f1", "f2"})
	store.Set(feedNamespace, feedKeyURL("f1"), "https://example.com/feed1.xml")
	store.Set(feedNamespace, feedKeyName("f1"), "Feed One")
	store.Set(feedNamespace, feedKeyNotify("f1"), "true")
	store.Set(feedNamespace, feedKeyTrustZone("f1"), "trusted")
	store.Set(feedNamespace, feedKeyURL("f2"), "https://example.com/feed2.xml")
	store.Set(feedNamespace, feedKeyName("f2"), "Feed Two")
	store.Set(feedNamespace, feedKeyNotify("f2"), "true")
	// No trust_zone for f2 — should default to "unknown".

	ft := NewFeedTools(store, nil, 10)
	handler := ft.FeedsHandler()

	result, err := handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("FeedsHandler() error: %v", err)
	}

	type feedInfo struct {
		FeedID    string `json:"feed_id"`
		Name      string `json:"name"`
		TrustZone string `json:"trust_zone"`
	}
	var feeds []feedInfo
	if err := json.Unmarshal([]byte(result), &feeds); err != nil {
		t.Fatalf("unmarshal feeds: %v", err)
	}

	if len(feeds) != 2 {
		t.Fatalf("got %d feeds, want 2", len(feeds))
	}

	// Find each feed by ID.
	zones := map[string]string{}
	for _, f := range feeds {
		zones[f.FeedID] = f.TrustZone
	}
	if zones["f1"] != "trusted" {
		t.Errorf("f1 trust_zone = %q, want %q", zones["f1"], "trusted")
	}
	if zones["f2"] != "unknown" {
		t.Errorf("f2 trust_zone = %q, want %q (default)", zones["f2"], "unknown")
	}
}

func TestFollowHandler_FeedDiscovery(t *testing.T) {
	// Serve an RSS feed at /feed.xml.
	atomXML := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
	<title>Blog Feed</title>
	<entry><id>post-1</id><title>Post One</title>
	<link href="https://example.com/post1"/>
	<published>2026-02-20T12:00:00Z</published></entry></feed>`

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		// HTML page with feed link.
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head>
		<link rel="alternate" type="application/atom+xml" href="/feed.xml">
		</head><body>Blog</body></html>`))
	})
	mux.HandleFunc("/feed.xml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(atomXML))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	store := newTestStore(t)
	ft := NewFeedTools(store, nil, 10)
	handler := ft.FollowHandler()

	// Give it the HTML page URL, not the feed URL.
	result, err := handler(context.Background(), map[string]any{"url": srv.URL + "/"})
	if err != nil {
		t.Fatalf("FollowHandler() error: %v", err)
	}

	var out map[string]string
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	// The discovered feed URL should be the resolved /feed.xml path.
	if out["url"] != srv.URL+"/feed.xml" {
		t.Errorf("url = %q, want %q", out["url"], srv.URL+"/feed.xml")
	}
	if out["name"] != "Blog Feed" {
		t.Errorf("name = %q, want %q", out["name"], "Blog Feed")
	}
}
