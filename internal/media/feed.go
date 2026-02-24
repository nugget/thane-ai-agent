package media

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/httpkit"
)

// Feed represents a parsed RSS or Atom feed with its entries normalized
// into a common structure.
type Feed struct {
	Title   string
	Entries []FeedEntry
}

// FeedEntry represents a single item in a feed.
type FeedEntry struct {
	ID        string // <guid> (RSS) or <id> (Atom)
	Title     string
	Link      string
	Published time.Time
}

// rssFeed is the XML structure for RSS 2.0 feeds.
type rssFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title string    `xml:"title"`
	Items []rssItem `xml:"item"`
}

type rssItem struct {
	Title   string `xml:"title"`
	Link    string `xml:"link"`
	GUID    string `xml:"guid"`
	PubDate string `xml:"pubDate"`
}

// atomFeed is the XML structure for Atom feeds (used by YouTube).
type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Title   string      `xml:"title"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID        string     `xml:"id"`
	Title     string     `xml:"title"`
	Links     []atomLink `xml:"link"`
	Published string     `xml:"published"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

// parseFeed parses XML data as either an Atom or RSS feed, returning
// a normalized Feed. Atom is tried first because YouTube uses it.
func parseFeed(data []byte) (*Feed, error) {
	// Try Atom first.
	var atom atomFeed
	if err := xml.Unmarshal(data, &atom); err == nil && atom.XMLName.Local == "feed" {
		return atomToFeed(&atom), nil
	}

	// Try RSS 2.0.
	var rss rssFeed
	if err := xml.Unmarshal(data, &rss); err == nil && rss.XMLName.Local == "rss" {
		return rssToFeed(&rss), nil
	}

	return nil, fmt.Errorf("unrecognized feed format (expected RSS 2.0 or Atom)")
}

// atomToFeed converts a parsed Atom feed to the normalized Feed type.
// When multiple <link> elements exist, the one with rel="alternate" is
// preferred. If no rel is specified, the first link is used. Entry IDs
// fall back to the link href when <id> is absent.
func atomToFeed(af *atomFeed) *Feed {
	f := &Feed{Title: af.Title}
	for _, e := range af.Entries {
		pub, _ := time.Parse(time.RFC3339, e.Published)
		link := atomBestLink(e.Links)
		id := e.ID
		if id == "" {
			id = link
		}
		f.Entries = append(f.Entries, FeedEntry{
			ID:        id,
			Title:     e.Title,
			Link:      link,
			Published: pub,
		})
	}
	return f
}

// atomBestLink selects the most appropriate link from an Atom entry's
// link list. Prefers rel="alternate"; falls back to the first link.
func atomBestLink(links []atomLink) string {
	if len(links) == 0 {
		return ""
	}
	for _, l := range links {
		if l.Rel == "alternate" || l.Rel == "" {
			return l.Href
		}
	}
	return links[0].Href
}

// rssToFeed converts a parsed RSS 2.0 feed to the normalized Feed type.
func rssToFeed(rf *rssFeed) *Feed {
	f := &Feed{Title: rf.Channel.Title}
	for _, item := range rf.Channel.Items {
		pub, _ := time.Parse(time.RFC1123Z, item.PubDate)
		if pub.IsZero() {
			// Try RFC1123 without numeric timezone.
			pub, _ = time.Parse(time.RFC1123, item.PubDate)
		}
		id := item.GUID
		if id == "" {
			id = item.Link
		}
		f.Entries = append(f.Entries, FeedEntry{
			ID:        id,
			Title:     item.Title,
			Link:      item.Link,
			Published: pub,
		})
	}
	return f
}

// fetchFeed retrieves and parses a feed from the given URL.
func fetchFeed(ctx context.Context, httpClient *http.Client, feedURL string) (*Feed, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch feed: %w", err)
	}
	defer httpkit.DrainAndClose(resp.Body, 1<<20) // 1 MB limit

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("feed returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read feed body: %w", err)
	}

	return parseFeed(body)
}

// ytChannelIDRe matches YouTube channel IDs in page HTML.
var ytChannelIDRe = regexp.MustCompile(`"channelId"\s*:\s*"(UC[a-zA-Z0-9_-]+)"`)

// ytCanonicalRe matches canonical URLs with channel IDs.
var ytCanonicalRe = regexp.MustCompile(`<link\s+rel="canonical"\s+href="https://www\.youtube\.com/channel/(UC[a-zA-Z0-9_-]+)"`)

// isYouTubeHost reports whether host is a known YouTube hostname.
func isYouTubeHost(host string) bool {
	switch strings.ToLower(host) {
	case "youtube.com", "www.youtube.com", "m.youtube.com":
		return true
	}
	return false
}

// resolveYouTubeFeed converts a YouTube channel URL to the corresponding
// Atom feed URL. Accepts @handle or /channel/ URLs. Returns the original
// URL unchanged if it's already a feed URL or not a YouTube channel.
// The hostname is validated to prevent unintended fetches on non-YouTube
// domains that happen to contain similar path patterns.
func resolveYouTubeFeed(ctx context.Context, httpClient *http.Client, rawURL string) (string, error) {
	// Already a feed URL — return as-is.
	if strings.Contains(rawURL, "/feeds/videos.xml") {
		return rawURL, nil
	}

	// Parse and validate hostname before doing any YouTube-specific resolution.
	parsed, err := url.Parse(rawURL)
	if err != nil || !isYouTubeHost(parsed.Hostname()) {
		return rawURL, nil
	}

	// /channel/UCXXXX → construct directly.
	if strings.HasPrefix(parsed.Path, "/channel/UC") {
		parts := strings.SplitN(parsed.Path, "/channel/", 2)
		if len(parts) == 2 {
			channelID := strings.Split(parts[1], "/")[0]
			return "https://www.youtube.com/feeds/videos.xml?channel_id=" + channelID, nil
		}
	}

	// @handle → fetch page and extract channel_id.
	if !strings.HasPrefix(parsed.Path, "/@") {
		return rawURL, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch channel page: %w", err)
	}
	defer httpkit.DrainAndClose(resp.Body, 1<<20)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("channel page returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return "", fmt.Errorf("read channel page: %w", err)
	}
	html := string(body)

	// Try canonical link first, then JSON metadata.
	if m := ytCanonicalRe.FindStringSubmatch(html); len(m) == 2 {
		return "https://www.youtube.com/feeds/videos.xml?channel_id=" + m[1], nil
	}
	if m := ytChannelIDRe.FindStringSubmatch(html); len(m) == 2 {
		return "https://www.youtube.com/feeds/videos.xml?channel_id=" + m[1], nil
	}

	return "", fmt.Errorf("could not extract channel ID from %s — try the direct RSS URL: https://www.youtube.com/feeds/videos.xml?channel_id=CHANNEL_ID", rawURL)
}
