package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/nugget/thane-ai-agent/internal/httpkit"
	"github.com/nugget/thane-ai-agent/internal/opstate"
)

// FeedTools provides tool handlers and definitions for feed management.
type FeedTools struct {
	state    *opstate.Store
	http     *http.Client
	logger   *slog.Logger
	maxFeeds int
}

// NewFeedTools creates a FeedTools instance. The HTTP client is created
// internally via httpkit.
func NewFeedTools(state *opstate.Store, logger *slog.Logger, maxFeeds int) *FeedTools {
	if logger == nil {
		logger = slog.Default()
	}
	if maxFeeds <= 0 {
		maxFeeds = 50
	}
	return &FeedTools{
		state:    state,
		http:     httpkit.NewClient(httpkit.WithTimeout(30 * time.Second)),
		logger:   logger,
		maxFeeds: maxFeeds,
	}
}

// feedID generates a short, deterministic ID from a feed URL.
func feedID(feedURL string) string {
	h := sha256.Sum256([]byte(feedURL))
	return hex.EncodeToString(h[:6]) // 12 hex chars
}

// FollowHandler returns the tool handler for media_follow.
func (ft *FeedTools) FollowHandler() func(ctx context.Context, args map[string]any) (string, error) {
	return func(ctx context.Context, args map[string]any) (string, error) {
		rawURL, _ := args["url"].(string)
		if rawURL == "" {
			return "", fmt.Errorf("media_follow: url is required")
		}
		name, _ := args["name"].(string)

		notify := true
		if n, ok := args["notify"].(bool); ok {
			notify = n
		}

		// Check feed limit.
		ids, err := loadFeedIndex(ft.state)
		if err != nil {
			return "", fmt.Errorf("media_follow: load index: %w", err)
		}
		if len(ids) >= ft.maxFeeds {
			return "", fmt.Errorf("media_follow: feed limit reached (%d/%d) — unfollow a feed first", len(ids), ft.maxFeeds)
		}

		// Resolve YouTube channel URLs to RSS.
		feedURL, err := resolveYouTubeFeed(ctx, ft.http, rawURL)
		if err != nil {
			return "", fmt.Errorf("media_follow: resolve URL: %w", err)
		}

		// Fetch feed to validate and get title.
		feed, err := fetchFeed(ctx, ft.http, feedURL)
		if err != nil {
			return "", fmt.Errorf("media_follow: %w", err)
		}

		if name == "" {
			name = feed.Title
		}
		if name == "" {
			name = feedURL
		}

		id := feedID(feedURL)

		// Check for duplicate.
		for _, existing := range ids {
			if existing == id {
				return "", fmt.Errorf("media_follow: already following this feed (id: %s)", id)
			}
		}

		// Store feed state.
		now := time.Now().UTC().Format(time.RFC3339)
		notifyStr := "true"
		if !notify {
			notifyStr = "false"
		}

		if err := ft.state.Set(feedNamespace, feedKeyURL(id), feedURL); err != nil {
			return "", fmt.Errorf("media_follow: store URL: %w", err)
		}
		if err := ft.state.Set(feedNamespace, feedKeyName(id), name); err != nil {
			return "", fmt.Errorf("media_follow: store name: %w", err)
		}
		if err := ft.state.Set(feedNamespace, feedKeyNotify(id), notifyStr); err != nil {
			return "", fmt.Errorf("media_follow: store notify: %w", err)
		}
		if err := ft.state.Set(feedNamespace, feedKeyLastChecked(id), now); err != nil {
			return "", fmt.Errorf("media_follow: store last_checked: %w", err)
		}

		// Set high-water mark to latest entry (don't backfill).
		latestTitle := ""
		if len(feed.Entries) > 0 {
			if err := ft.state.Set(feedNamespace, feedKeyLastEntryID(id), feed.Entries[0].ID); err != nil {
				return "", fmt.Errorf("media_follow: store last_entry_id: %w", err)
			}
			latestTitle = feed.Entries[0].Title
			if err := ft.state.Set(feedNamespace, feedKeyLatestTitle(id), latestTitle); err != nil {
				ft.logger.Warn("failed to store latest_title", "feed_id", id, "error", err)
			}
		}

		// Add to index.
		ids = append(ids, id)
		if err := saveFeedIndex(ft.state, ids); err != nil {
			return "", fmt.Errorf("media_follow: update index: %w", err)
		}

		ft.logger.Info("feed followed",
			"feed_id", id,
			"name", name,
			"url", feedURL,
		)

		result := map[string]string{
			"feed_id":      id,
			"name":         name,
			"url":          feedURL,
			"latest_entry": latestTitle,
		}
		out, _ := json.Marshal(result)
		return string(out), nil
	}
}

// UnfollowHandler returns the tool handler for media_unfollow.
func (ft *FeedTools) UnfollowHandler() func(ctx context.Context, args map[string]any) (string, error) {
	return func(_ context.Context, args map[string]any) (string, error) {
		id, _ := args["feed_id"].(string)
		if id == "" {
			return "", fmt.Errorf("media_unfollow: feed_id is required")
		}

		// Verify feed exists.
		feedURL, err := ft.state.Get(feedNamespace, feedKeyURL(id))
		if err != nil {
			return "", fmt.Errorf("media_unfollow: %w", err)
		}
		if feedURL == "" {
			return "", fmt.Errorf("media_unfollow: feed %q not found", id)
		}

		name, _ := ft.state.Get(feedNamespace, feedKeyName(id))
		if name == "" {
			name = id
		}

		// Remove all feed state keys.
		for _, key := range []string{
			feedKeyURL(id),
			feedKeyName(id),
			feedKeyNotify(id),
			feedKeyLastEntryID(id),
			feedKeyLastChecked(id),
			feedKeyLatestTitle(id),
		} {
			if err := ft.state.Delete(feedNamespace, key); err != nil {
				ft.logger.Warn("failed to delete feed key", "key", key, "error", err)
			}
		}

		// Remove from index.
		ids, err := loadFeedIndex(ft.state)
		if err != nil {
			return "", fmt.Errorf("media_unfollow: load index: %w", err)
		}
		filtered := make([]string, 0, len(ids))
		for _, existing := range ids {
			if existing != id {
				filtered = append(filtered, existing)
			}
		}
		if err := saveFeedIndex(ft.state, filtered); err != nil {
			return "", fmt.Errorf("media_unfollow: update index: %w", err)
		}

		ft.logger.Info("feed unfollowed", "feed_id", id, "name", name)

		return fmt.Sprintf("Unfollowed %q (id: %s)", name, id), nil
	}
}

// FeedsHandler returns the tool handler for media_feeds.
func (ft *FeedTools) FeedsHandler() func(ctx context.Context, args map[string]any) (string, error) {
	return func(_ context.Context, _ map[string]any) (string, error) {
		ids, err := loadFeedIndex(ft.state)
		if err != nil {
			return "", fmt.Errorf("media_feeds: load index: %w", err)
		}

		type feedInfo struct {
			FeedID      string `json:"feed_id"`
			Name        string `json:"name"`
			URL         string `json:"url"`
			LastChecked string `json:"last_checked,omitempty"`
			LatestEntry string `json:"latest_entry,omitempty"`
			Notify      string `json:"notify"`
		}

		feeds := make([]feedInfo, 0, len(ids))
		for _, id := range ids {
			url, _ := ft.state.Get(feedNamespace, feedKeyURL(id))
			name, _ := ft.state.Get(feedNamespace, feedKeyName(id))
			lastChecked, _ := ft.state.Get(feedNamespace, feedKeyLastChecked(id))
			latestTitle, _ := ft.state.Get(feedNamespace, feedKeyLatestTitle(id))
			notify, _ := ft.state.Get(feedNamespace, feedKeyNotify(id))
			if notify == "" {
				notify = "true"
			}

			feeds = append(feeds, feedInfo{
				FeedID:      id,
				Name:        name,
				URL:         url,
				LastChecked: lastChecked,
				LatestEntry: latestTitle,
				Notify:      notify,
			})
		}

		out, _ := json.Marshal(feeds)
		return string(out), nil
	}
}

// FollowDefinition returns the JSON Schema for the media_follow tool.
func FollowDefinition() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "RSS/Atom feed URL, or a YouTube channel URL (e.g., https://www.youtube.com/@ChannelName) that will be resolved to the channel's feed.",
			},
			"name": map[string]any{
				"type":        "string",
				"description": "Display name for the feed. If omitted, auto-detected from the feed title.",
			},
			"notify": map[string]any{
				"type":        "boolean",
				"description": "Whether to notify the owner when new content is detected. Default: true.",
			},
		},
		"required": []string{"url"},
	}
}

// UnfollowDefinition returns the JSON Schema for the media_unfollow tool.
func UnfollowDefinition() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"feed_id": map[string]any{
				"type":        "string",
				"description": "The feed identifier returned by media_follow or media_feeds.",
			},
		},
		"required": []string{"feed_id"},
	}
}

// FeedsDefinition returns the JSON Schema for the media_feeds tool.
func FeedsDefinition() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

// loadFeedIndex reads the feed ID list from opstate (package-level for
// use by both FeedTools and FeedPoller).
func loadFeedIndex(state *opstate.Store) ([]string, error) {
	raw, err := state.Get(feedNamespace, feedIndexKey)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, fmt.Errorf("parse feed index: %w", err)
	}
	return ids, nil
}

// saveFeedIndex writes the feed ID list to opstate (package-level for
// use by both FeedTools and FeedPoller).
func saveFeedIndex(state *opstate.Store, ids []string) error {
	data, err := json.Marshal(ids)
	if err != nil {
		return fmt.Errorf("marshal feed index: %w", err)
	}
	return state.Set(feedNamespace, feedIndexKey, string(data))
}
