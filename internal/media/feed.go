package media

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
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
	ID        string   `xml:"id"`
	Title     string   `xml:"title"`
	Link      atomLink `xml:"link"`
	Published string   `xml:"published"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
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
func atomToFeed(af *atomFeed) *Feed {
	f := &Feed{Title: af.Title}
	for _, e := range af.Entries {
		pub, _ := time.Parse(time.RFC3339, e.Published)
		f.Entries = append(f.Entries, FeedEntry{
			ID:        e.ID,
			Title:     e.Title,
			Link:      e.Link.Href,
			Published: pub,
		})
	}
	return f
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

// resolveYouTubeFeed converts a YouTube channel URL to the corresponding
// Atom feed URL. Accepts @handle or /channel/ URLs. Returns the original
// URL unchanged if it's already a feed URL or not a YouTube channel.
func resolveYouTubeFeed(ctx context.Context, httpClient *http.Client, rawURL string) (string, error) {
	// Already a feed URL — return as-is.
	if strings.Contains(rawURL, "/feeds/videos.xml") {
		return rawURL, nil
	}

	// Only resolve YouTube channel URLs.
	if !strings.Contains(rawURL, "youtube.com/@") && !strings.Contains(rawURL, "youtube.com/channel/") {
		return rawURL, nil
	}

	// /channel/UCXXXX → construct directly.
	if strings.Contains(rawURL, "/channel/UC") {
		parts := strings.Split(rawURL, "/channel/")
		if len(parts) == 2 {
			channelID := strings.Split(parts[1], "/")[0]
			channelID = strings.Split(channelID, "?")[0]
			return "https://www.youtube.com/feeds/videos.xml?channel_id=" + channelID, nil
		}
	}

	// @handle → fetch page and extract channel_id.
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
