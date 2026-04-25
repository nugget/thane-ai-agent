package media

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/buildinfo"
	"github.com/nugget/thane-ai-agent/internal/platform/httpkit"
)

func TestParseFeed_Atom(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Test Channel</title>
  <entry>
    <id>yt:video:abc123</id>
    <title>First Video</title>
    <link href="https://www.youtube.com/watch?v=abc123"/>
    <published>2026-02-20T12:00:00+00:00</published>
  </entry>
  <entry>
    <id>yt:video:def456</id>
    <title>Second Video</title>
    <link href="https://www.youtube.com/watch?v=def456"/>
    <published>2026-02-18T08:00:00+00:00</published>
  </entry>
</feed>`

	feed, err := parseFeed([]byte(xml))
	if err != nil {
		t.Fatalf("parseFeed() error: %v", err)
	}
	if feed.Title != "Test Channel" {
		t.Errorf("Title = %q, want %q", feed.Title, "Test Channel")
	}
	if len(feed.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(feed.Entries))
	}
	if feed.Entries[0].ID != "yt:video:abc123" {
		t.Errorf("entry[0].ID = %q, want %q", feed.Entries[0].ID, "yt:video:abc123")
	}
	if feed.Entries[0].Link != "https://www.youtube.com/watch?v=abc123" {
		t.Errorf("entry[0].Link = %q", feed.Entries[0].Link)
	}
	if feed.Entries[0].Published.IsZero() {
		t.Error("entry[0].Published should not be zero")
	}
}

func TestParseFeed_RSS(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Podcast Feed</title>
    <item>
      <title>Episode 42</title>
      <link>https://example.com/ep42</link>
      <guid>ep-42-guid</guid>
      <pubDate>Mon, 20 Feb 2026 12:00:00 +0000</pubDate>
    </item>
    <item>
      <title>Episode 41</title>
      <link>https://example.com/ep41</link>
      <guid>ep-41-guid</guid>
      <pubDate>Mon, 13 Feb 2026 12:00:00 +0000</pubDate>
    </item>
  </channel>
</rss>`

	feed, err := parseFeed([]byte(xml))
	if err != nil {
		t.Fatalf("parseFeed() error: %v", err)
	}
	if feed.Title != "Podcast Feed" {
		t.Errorf("Title = %q, want %q", feed.Title, "Podcast Feed")
	}
	if len(feed.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(feed.Entries))
	}
	if feed.Entries[0].ID != "ep-42-guid" {
		t.Errorf("entry[0].ID = %q, want %q", feed.Entries[0].ID, "ep-42-guid")
	}
	if feed.Entries[0].Link != "https://example.com/ep42" {
		t.Errorf("entry[0].Link = %q", feed.Entries[0].Link)
	}
}

func TestParseFeed_AtomMultipleLinks(t *testing.T) {
	xml := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
  <title>Multi-Link Feed</title>
  <entry>
    <id>entry-1</id><title>Entry</title>
    <link rel="self" href="https://example.com/self"/>
    <link rel="alternate" href="https://example.com/content"/>
    <link rel="edit" href="https://example.com/edit"/>
    <published>2026-02-20T12:00:00Z</published>
  </entry></feed>`

	feed, err := parseFeed([]byte(xml))
	if err != nil {
		t.Fatalf("parseFeed() error: %v", err)
	}
	// Should pick rel="alternate" over others.
	if feed.Entries[0].Link != "https://example.com/content" {
		t.Errorf("Link = %q, want alternate link", feed.Entries[0].Link)
	}
}

func TestParseFeed_AtomNoID(t *testing.T) {
	xml := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
  <title>No ID Feed</title>
  <entry>
    <title>Entry Without ID</title>
    <link href="https://example.com/fallback"/>
    <published>2026-02-20T12:00:00Z</published>
  </entry></feed>`

	feed, err := parseFeed([]byte(xml))
	if err != nil {
		t.Fatalf("parseFeed() error: %v", err)
	}
	// ID should fall back to Link when <id> is absent.
	if feed.Entries[0].ID != "https://example.com/fallback" {
		t.Errorf("ID = %q, want link as fallback", feed.Entries[0].ID)
	}
}

func TestParseFeed_RSSNoGUID(t *testing.T) {
	xml := `<rss version="2.0"><channel><title>T</title>
	<item><title>Ep</title><link>https://example.com/ep1</link></item>
	</channel></rss>`

	feed, err := parseFeed([]byte(xml))
	if err != nil {
		t.Fatalf("parseFeed() error: %v", err)
	}
	// When GUID is absent, Link should be used as ID.
	if feed.Entries[0].ID != "https://example.com/ep1" {
		t.Errorf("entry[0].ID = %q, want link as fallback", feed.Entries[0].ID)
	}
}

func TestParseFeed_EmptyFeed(t *testing.T) {
	xml := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
	<title>Empty</title></feed>`

	feed, err := parseFeed([]byte(xml))
	if err != nil {
		t.Fatalf("parseFeed() error: %v", err)
	}
	if len(feed.Entries) != 0 {
		t.Errorf("got %d entries, want 0", len(feed.Entries))
	}
}

func TestParseFeed_Malformed(t *testing.T) {
	_, err := parseFeed([]byte("this is not xml at all"))
	if err == nil {
		t.Fatal("expected error for malformed input")
	}
	if !strings.Contains(err.Error(), "unrecognized feed format") {
		t.Errorf("error %q should mention unrecognized format", err.Error())
	}
}

func TestFetchFeed(t *testing.T) {
	atomXML := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
	<title>Test</title>
	<entry>
	  <id>test-1</id><title>Entry 1</title>
	  <link href="https://example.com/1"/>
	  <published>2026-02-20T12:00:00Z</published>
	</entry></feed>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(atomXML))
	}))
	defer srv.Close()

	feed, err := fetchFeed(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("fetchFeed() error: %v", err)
	}
	if feed.Title != "Test" {
		t.Errorf("Title = %q, want %q", feed.Title, "Test")
	}
	if len(feed.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(feed.Entries))
	}
}

func TestFetchFeed_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := fetchFeed(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("error %q should mention HTTP 404", err.Error())
	}
}

func TestResolveYouTubeFeed_AlreadyFeed(t *testing.T) {
	url := "https://www.youtube.com/feeds/videos.xml?channel_id=UCtest123"
	got, err := resolveYouTubeFeed(context.Background(), http.DefaultClient, url)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != url {
		t.Errorf("got %q, want %q (unchanged)", got, url)
	}
}

func TestResolveYouTubeFeed_ChannelURL(t *testing.T) {
	url := "https://www.youtube.com/channel/UCtest123abc"
	got, err := resolveYouTubeFeed(context.Background(), http.DefaultClient, url)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://www.youtube.com/feeds/videos.xml?channel_id=UCtest123abc"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveYouTubeFeed_HandleURL(t *testing.T) {
	html := `<html><head>
	<link rel="canonical" href="https://www.youtube.com/channel/UCabc123xyz">
	</head><body></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(html))
	}))
	defer srv.Close()

	// The function checks for "youtube.com/@" in the URL. We need to
	// construct a URL that matches but routes to our test server.
	// Instead, test the extraction logic by calling with the test server URL
	// modified to look like a YouTube handle URL.
	// For the actual HTTP call we pass the server URL through directly.

	// Test the JSON metadata path instead.
	html2 := `<html><body><script>"channelId":"UCjson456def"</script></body></html>`
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(html2))
	}))
	defer srv2.Close()

	// Verify the regex patterns work.
	if m := ytCanonicalRe.FindStringSubmatch(html); len(m) != 2 || m[1] != "UCabc123xyz" {
		t.Errorf("ytCanonicalRe failed to extract channel ID from canonical link")
	}
	if m := ytChannelIDRe.FindStringSubmatch(html2); len(m) != 2 || m[1] != "UCjson456def" {
		t.Errorf("ytChannelIDRe failed to extract channel ID from JSON")
	}
}

func TestResolveYouTubeFeed_HandleURL_UsesTruthfulUserAgent(t *testing.T) {
	html := `<html><head>
	<link rel="canonical" href="https://www.youtube.com/channel/UCabc123xyz">
	</head><body></body></html>`

	var seenUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenUA = r.Header.Get("User-Agent")
		w.Write([]byte(html))
	}))
	defer srv.Close()

	srvURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	transport := httpkit.NewTransport()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, srvURL.Host)
	}
	httpClient := httpkit.NewClient(httpkit.WithTransport(transport))

	handleURL := "http://www.youtube.com/@testhandle"
	got, err := resolveYouTubeFeed(context.Background(), httpClient, handleURL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantFeed := "https://www.youtube.com/feeds/videos.xml?channel_id=UCabc123xyz"
	if got != wantFeed {
		t.Fatalf("got %q, want %q", got, wantFeed)
	}
	if seenUA != buildinfo.UserAgent() {
		t.Fatalf("expected truthful UA %q, got %q", buildinfo.UserAgent(), seenUA)
	}
}

func TestResolveYouTubeFeed_PlaylistURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "standard playlist",
			url:  "https://www.youtube.com/playlist?list=PL96C35uN7xGLLeET0dOWaKHkAlPsrkcha",
			want: "https://www.youtube.com/feeds/videos.xml?playlist_id=PL96C35uN7xGLLeET0dOWaKHkAlPsrkcha",
		},
		{
			name: "playlist with extra params",
			url:  "https://www.youtube.com/playlist?list=PLtest123&si=abcdef",
			want: "https://www.youtube.com/feeds/videos.xml?playlist_id=PLtest123",
		},
		{
			name: "m.youtube.com playlist",
			url:  "https://m.youtube.com/playlist?list=PLmobile456",
			want: "https://www.youtube.com/feeds/videos.xml?playlist_id=PLmobile456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveYouTubeFeed(context.Background(), http.DefaultClient, tt.url)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveYouTubeFeed_PlaylistMissingList(t *testing.T) {
	// /playlist without a list param should return the URL unchanged.
	url := "https://www.youtube.com/playlist"
	got, err := resolveYouTubeFeed(context.Background(), http.DefaultClient, url)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != url {
		t.Errorf("got %q, want %q (unchanged)", got, url)
	}
}

func TestResolveYouTubeFeed_NonYouTube(t *testing.T) {
	url := "https://example.com/feed.xml"
	got, err := resolveYouTubeFeed(context.Background(), http.DefaultClient, url)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != url {
		t.Errorf("non-YouTube URL should be returned unchanged, got %q", got)
	}
}

// ytAlternateFeedMatch extracts the feed URL from a ytAlternateFeedRe match,
// handling both alternation groups (type-before-href and href-before-type).
func ytAlternateFeedMatch(html string) string {
	m := ytAlternateFeedRe.FindStringSubmatch(html)
	if m == nil {
		return ""
	}
	if m[1] != "" {
		return m[1]
	}
	return m[2]
}

func TestYTAlternateFeedRe(t *testing.T) {
	want := "https://www.youtube.com/feeds/videos.xml?channel_id=UCabc123xyz"

	tests := []struct {
		name string
		html string
	}{
		{
			name: "type before href, double quotes",
			html: `<link rel="alternate" type="application/rss+xml" title="RSS" href="https://www.youtube.com/feeds/videos.xml?channel_id=UCabc123xyz">`,
		},
		{
			name: "href before type, double quotes",
			html: `<link rel="alternate" href="https://www.youtube.com/feeds/videos.xml?channel_id=UCabc123xyz" type="application/rss+xml" title="RSS">`,
		},
		{
			name: "type before href, single quotes",
			html: `<link rel='alternate' type='application/rss+xml' title='RSS' href='https://www.youtube.com/feeds/videos.xml?channel_id=UCabc123xyz'>`,
		},
		{
			name: "href before type, single quotes",
			html: `<link rel='alternate' href='https://www.youtube.com/feeds/videos.xml?channel_id=UCabc123xyz' type='application/rss+xml' title='RSS'>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ytAlternateFeedMatch(tt.html)
			if got == "" {
				t.Fatal("ytAlternateFeedRe did not match")
			}
			if got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

func TestYTRegexes_LargePageOffset(t *testing.T) {
	// YouTube @handle pages place all channel metadata at ~600KB+.
	// Verify that the regexes match when content sits past the old
	// 512KB limit — the read limit was bumped to 1MB in #517.
	wantFeed := "https://www.youtube.com/feeds/videos.xml?channel_id=UCtest600k"
	padding := strings.Repeat("x", 600*1024)
	page := "<html><head>" + padding +
		`<link rel="alternate" type="application/rss+xml" title="RSS" href="` + wantFeed + `">` +
		`<link rel="canonical" href="https://www.youtube.com/channel/UCtest600k">` +
		`<script>"channelId":"UCtest600k"</script>` +
		"</head></html>"

	// Simulate reading up to 1MB (new limit).
	limit := 1 << 20
	if len(page) > limit {
		page = page[:limit]
	}

	if got := ytAlternateFeedMatch(page); got != wantFeed {
		t.Errorf("ytAlternateFeedRe failed at 600KB offset: got %q", got)
	}
	if m := ytCanonicalRe.FindStringSubmatch(page); len(m) != 2 || m[1] != "UCtest600k" {
		t.Errorf("ytCanonicalRe failed at 600KB offset: got %v", m)
	}
	if m := ytChannelIDRe.FindStringSubmatch(page); len(m) != 2 || m[1] != "UCtest600k" {
		t.Errorf("ytChannelIDRe failed at 600KB offset: got %v", m)
	}

	// Verify the old 512KB limit would have missed these.
	truncated := page[:512*1024]
	if got := ytAlternateFeedMatch(truncated); got != "" {
		t.Error("ytAlternateFeedRe should NOT match within 512KB")
	}
	if m := ytCanonicalRe.FindStringSubmatch(truncated); len(m) != 0 {
		t.Error("ytCanonicalRe should NOT match within 512KB")
	}
}

func TestDiscoverFeedURL_RSS(t *testing.T) {
	html := `<html><head>
	<link rel="alternate" type="application/rss+xml" href="https://example.com/feed.xml" title="RSS Feed">
	</head><body></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(html))
	}))
	defer srv.Close()

	got, err := discoverFeedURL(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("discoverFeedURL() error: %v", err)
	}
	if got != "https://example.com/feed.xml" {
		t.Errorf("got %q, want %q", got, "https://example.com/feed.xml")
	}
}

func TestDiscoverFeedURL_Atom(t *testing.T) {
	html := `<html><head>
	<link rel="alternate" type="application/atom+xml" href="/atom.xml">
	</head><body></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(html))
	}))
	defer srv.Close()

	got, err := discoverFeedURL(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("discoverFeedURL() error: %v", err)
	}
	// Relative URL should be resolved against the page URL.
	want := srv.URL + "/atom.xml"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDiscoverFeedURL_HrefFirst(t *testing.T) {
	// Some sites put href before type — the regex must handle both orders.
	html := `<html><head>
	<link rel="alternate" href="/rss" type="application/rss+xml">
	</head><body></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(html))
	}))
	defer srv.Close()

	got, err := discoverFeedURL(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("discoverFeedURL() error: %v", err)
	}
	want := srv.URL + "/rss"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDiscoverFeedURL_NoFeedLink(t *testing.T) {
	html := `<html><head><title>No Feed</title></head><body></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(html))
	}))
	defer srv.Close()

	_, err := discoverFeedURL(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("expected error when no feed link present")
	}
	if !strings.Contains(err.Error(), "no RSS/Atom feed link found") {
		t.Errorf("error %q should mention no feed link found", err.Error())
	}
}

func TestFetchFeed_LargeFeed(t *testing.T) {
	// Build a valid RSS feed that exceeds 1 MB (old limit) but stays
	// under 10 MB (new limit). This simulates long-running podcast feeds.
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><rss version="2.0"><channel><title>Big Podcast</title>`)
	for i := range 5000 {
		fmt.Fprintf(&b, `<item><title>Episode %d — A Reasonably Long Title for Padding Purposes to Bulk Up the Feed</title>`+
			`<link>https://example.com/episodes/%d</link>`+
			`<guid>ep-%d</guid>`+
			`<pubDate>Mon, 20 Feb 2026 12:00:00 +0000</pubDate></item>`, i, i, i)
	}
	b.WriteString(`</channel></rss>`)
	feedXML := b.String()
	if len(feedXML) < 1<<20 {
		t.Fatalf("test feed too small (%d bytes), expected > 1 MB", len(feedXML))
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(feedXML))
	}))
	defer srv.Close()

	feed, err := fetchFeed(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("fetchFeed() should handle feeds > 1 MB: %v", err)
	}
	if feed.Title != "Big Podcast" {
		t.Errorf("Title = %q, want %q", feed.Title, "Big Podcast")
	}
	if len(feed.Entries) != 5000 {
		t.Errorf("got %d entries, want 5000", len(feed.Entries))
	}
}

func TestFetchFeed_ExceedsLimit(t *testing.T) {
	// Serve a response body that exceeds maxFeedSize. The server streams
	// the data so we don't need to allocate the full payload in the test.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		// Write a valid XML start so it's clearly not a format problem.
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Huge</title>`))
		// Pad with enough items to exceed 10 MB.
		chunk := []byte(`<item><title>Episode</title><link>https://example.com/ep</link></item>`)
		for written := 0; written < maxFeedSize; written += len(chunk) {
			w.Write(chunk)
		}
		w.Write([]byte(`</channel></rss>`))
	}))
	defer srv.Close()

	_, err := fetchFeed(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("expected error for feed exceeding size limit")
	}
	if !strings.Contains(err.Error(), "size limit") {
		t.Errorf("error %q should mention size limit", err.Error())
	}
}

func TestDiscoverFeedURL_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := discoverFeedURL(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
}
