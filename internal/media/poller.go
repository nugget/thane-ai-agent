package media

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/httpkit"
	"github.com/nugget/thane-ai-agent/internal/opstate"
)

const (
	// feedNamespace is the opstate namespace for media feed state.
	feedNamespace = "media_feed"

	// feedIndexKey stores a JSON array of feed IDs.
	feedIndexKey = "feeds"
)

// opstate key helpers — all under the feedNamespace.
func feedKeyURL(id string) string         { return "feed:" + id + ":url" }
func feedKeyName(id string) string        { return "feed:" + id + ":name" }
func feedKeyNotify(id string) string      { return "feed:" + id + ":notify" }
func feedKeyLastEntryID(id string) string { return "feed:" + id + ":last_entry_id" }
func feedKeyLastChecked(id string) string { return "feed:" + id + ":last_checked" }
func feedKeyLatestTitle(id string) string { return "feed:" + id + ":latest_title" }

// FeedPoller checks followed RSS/Atom feeds for new entries by
// comparing entry IDs against a persisted high-water mark. It follows
// the same pattern as email.Poller — infrastructure code called by the
// scheduler task executor.
type FeedPoller struct {
	state  *opstate.Store
	logger *slog.Logger
	http   *http.Client
}

// NewFeedPoller creates a feed poller that checks all followed feeds
// and tracks state in the provided opstate store.
func NewFeedPoller(state *opstate.Store, logger *slog.Logger) *FeedPoller {
	if logger == nil {
		logger = slog.Default()
	}
	return &FeedPoller{
		state:  state,
		logger: logger,
		http:   httpkit.NewClient(httpkit.WithTimeout(30 * time.Second)),
	}
}

// CheckFeeds checks all followed feeds for new entries. Returns a
// formatted wake message describing new content, or empty string if
// nothing new was found. Network errors are logged and skipped
// per-feed; a failure on one feed does not prevent checking others.
func (p *FeedPoller) CheckFeeds(ctx context.Context) (string, error) {
	ids, err := loadFeedIndex(p.state)
	if err != nil {
		return "", fmt.Errorf("load feed index: %w", err)
	}
	if len(ids) == 0 {
		return "", nil
	}

	var sections []string

	for _, id := range ids {
		section, err := p.checkFeed(ctx, id)
		if err != nil {
			p.logger.Warn("feed poll failed",
				"feed_id", id,
				"error", err,
			)
			continue
		}
		if section != "" {
			sections = append(sections, section)
		}
	}

	if len(sections) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("New media content detected:\n")
	for _, s := range sections {
		sb.WriteString("\n")
		sb.WriteString(s)
	}
	return sb.String(), nil
}

// checkFeed checks a single feed for new entries. Returns a formatted
// section for the wake message, or empty string if nothing new.
func (p *FeedPoller) checkFeed(ctx context.Context, feedID string) (string, error) {
	feedURL, err := p.state.Get(feedNamespace, feedKeyURL(feedID))
	if err != nil {
		return "", fmt.Errorf("get feed URL: %w", err)
	}
	if feedURL == "" {
		return "", fmt.Errorf("feed %q has no URL", feedID)
	}

	feedName, _ := p.state.Get(feedNamespace, feedKeyName(feedID))
	if feedName == "" {
		feedName = feedID
	}

	lastEntryID, _ := p.state.Get(feedNamespace, feedKeyLastEntryID(feedID))

	feed, err := fetchFeed(ctx, p.http, feedURL)
	if err != nil {
		return "", fmt.Errorf("fetch %q: %w", feedName, err)
	}

	// Update last_checked timestamp.
	now := time.Now().UTC().Format(time.RFC3339)
	if err := p.state.Set(feedNamespace, feedKeyLastChecked(feedID), now); err != nil {
		p.logger.Warn("failed to update last_checked", "feed_id", feedID, "error", err)
	}

	if len(feed.Entries) == 0 {
		return "", nil
	}

	// Update latest title for display purposes.
	if err := p.state.Set(feedNamespace, feedKeyLatestTitle(feedID), feed.Entries[0].Title); err != nil {
		p.logger.Warn("failed to update latest_title", "feed_id", feedID, "error", err)
	}

	// First run: set high-water mark without reporting.
	if lastEntryID == "" {
		if err := p.state.Set(feedNamespace, feedKeyLastEntryID(feedID), feed.Entries[0].ID); err != nil {
			p.logger.Warn("failed to set initial high-water mark", "feed_id", feedID, "error", err)
		}
		p.logger.Info("feed high-water mark initialized",
			"feed_id", feedID,
			"feed_name", feedName,
			"latest_entry", feed.Entries[0].Title,
		)
		return "", nil
	}

	// Collect new entries (entries newer than the high-water mark).
	var newEntries []FeedEntry
	for _, entry := range feed.Entries {
		if entry.ID == lastEntryID {
			break
		}
		newEntries = append(newEntries, entry)
	}

	if len(newEntries) == 0 {
		return "", nil
	}

	// Update high-water mark to the newest entry.
	if err := p.state.Set(feedNamespace, feedKeyLastEntryID(feedID), newEntries[0].ID); err != nil {
		p.logger.Warn("failed to update high-water mark", "feed_id", feedID, "error", err)
	}

	p.logger.Info("new feed entries detected",
		"feed_id", feedID,
		"feed_name", feedName,
		"new_count", len(newEntries),
	)

	// Format section for wake message.
	var sb strings.Builder
	for _, entry := range newEntries {
		fmt.Fprintf(&sb, "**%s**: %s\n%s\n", feedName, entry.Title, entry.Link)
	}
	return sb.String(), nil
}

// loadFeedIndex and saveFeedIndex are defined in tools_feed.go as
// package-level functions shared by both FeedPoller and FeedTools.
