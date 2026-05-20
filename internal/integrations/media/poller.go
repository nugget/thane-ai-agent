package media

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/httpkit"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
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
func feedKeyTrustZone(id string) string   { return "feed:" + id + ":trust_zone" }

// validFeedTrustZones is the set of trust zones applicable to media feeds.
// Admin and household zones don't apply to media sources — only the 3-tier
// model (trusted/known/unknown) is relevant.
var validFeedTrustZones = map[string]bool{
	"trusted": true,
	"known":   true,
	"unknown": true,
}

// FeedPoller checks followed RSS/Atom feeds for new entries by
// comparing entry IDs against a persisted high-water mark. When run
// inside the loop infrastructure, it reports per-iteration metrics
// (feeds_checked, new_entries) via [loop.IterationSummary].
type FeedPoller struct {
	state      *opstate.Store
	logger     *slog.Logger
	http       *http.Client
	messageBus *messages.Bus
}

// FeedPollerOption customizes feed poller behavior.
type FeedPollerOption func(*FeedPoller)

// WithFeedMessageBus enables event-source wake delivery for feeds with a
// wake_loop target.
func WithFeedMessageBus(bus *messages.Bus) FeedPollerOption {
	return func(p *FeedPoller) {
		p.messageBus = bus
	}
}

// NewFeedPoller creates a feed poller that checks all followed feeds
// and tracks state in the provided opstate store.
func NewFeedPoller(state *opstate.Store, logger *slog.Logger, opts ...FeedPollerOption) *FeedPoller {
	if logger == nil {
		logger = slog.Default()
	}
	poller := &FeedPoller{
		state:  state,
		logger: logger,
		http:   httpkit.NewClient(httpkit.WithTimeout(30 * time.Second)),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(poller)
		}
	}
	return poller
}

type feedUpdate struct {
	FeedID         string
	LastEntryID    string
	Section        string
	Events         []messages.LoopEventPayload
	WakeTarget     messages.LoopWakeTarget
	WakeConfigured bool
	Notify         bool
}

// CheckFeeds checks all followed feeds for new entries. Returns a
// formatted wake message describing new content, or empty string if
// nothing new was found. Network errors are logged and skipped
// per-feed; a failure on one feed does not prevent checking others.
func (p *FeedPoller) CheckFeeds(ctx context.Context) (string, error) {
	summary := loop.IterationSummary(ctx)

	ids, err := loadFeedIndex(p.state)
	if err != nil {
		return "", fmt.Errorf("load feed index: %w", err)
	}
	if len(ids) == 0 {
		if summary != nil {
			summary["feeds_checked"] = 0
		}
		return "", nil
	}

	var sections []string
	newEntryCount := 0
	eventWakeCount := 0
	suppressedCount := 0

	for _, id := range ids {
		update, n, err := p.checkFeed(ctx, id)
		if err != nil {
			p.logger.Warn("feed poll failed",
				"feed_id", id,
				"error", err,
			)
			continue
		}
		newEntryCount += n
		if update == nil || n == 0 {
			continue
		}
		switch {
		case update.WakeConfigured:
			dispatched, err := p.dispatchFeedEventBatches(ctx, update)
			eventWakeCount += dispatched
			if err != nil {
				p.logger.Warn("feed event wake failed",
					"feed_id", id,
					"delivered_events", dispatched,
					"error", err,
				)
				continue
			}
		case update.Notify:
			if err := p.advanceFeedHighWater(update); err != nil {
				p.logger.Warn("failed to update high-water mark",
					"feed_id", id,
					"error", err,
				)
			}
			if update.Section != "" {
				sections = append(sections, update.Section)
			}
		default:
			if err := p.advanceFeedHighWater(update); err != nil {
				p.logger.Warn("failed to update high-water mark",
					"feed_id", id,
					"error", err,
				)
			}
			suppressedCount += n
		}
	}

	if summary != nil {
		summary["feeds_checked"] = len(ids)
		summary["new_entries"] = newEntryCount
		if eventWakeCount > 0 {
			summary["event_wakes"] = eventWakeCount
		}
		if suppressedCount > 0 {
			summary["suppressed_entries"] = suppressedCount
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

func (p *FeedPoller) dispatchFeedEventBatches(ctx context.Context, update *feedUpdate) (int, error) {
	delivered := 0
	for end := len(update.Events); end > 0; end -= messages.MaxLoopEventsPerWake {
		start := end - messages.MaxLoopEventsPerWake
		if start < 0 {
			start = 0
		}
		chunk := update.Events[start:end]
		batch := *update
		batch.Events = chunk
		if len(chunk) > 0 {
			batch.LastEntryID = chunk[0].ID
		}
		if batch.LastEntryID == "" {
			return delivered, fmt.Errorf("feed event batch missing high-water id")
		}

		dispatched, err := p.dispatchFeedEvents(ctx, &batch)
		if err != nil {
			return delivered, err
		}
		delivered += dispatched
		if err := p.advanceFeedHighWater(&batch); err != nil {
			return delivered, err
		}
	}
	return delivered, nil
}

func (p *FeedPoller) dispatchFeedEvents(ctx context.Context, update *feedUpdate) (int, error) {
	if p.messageBus == nil {
		return 0, fmt.Errorf("message bus is not configured")
	}
	env, err := messages.NewEventSourceEnvelope(
		messages.Identity{Kind: messages.IdentitySystem, Name: "media_feed_poller"},
		update.WakeTarget,
		"media_feed",
		update.Events,
	)
	if err != nil {
		return 0, err
	}
	if _, err := p.messageBus.Send(ctx, env); err != nil {
		return 0, err
	}
	return len(update.Events), nil
}

func (p *FeedPoller) advanceFeedHighWater(update *feedUpdate) error {
	if update == nil || update.FeedID == "" || update.LastEntryID == "" {
		return nil
	}
	if err := p.state.Set(feedNamespace, feedKeyLastEntryID(update.FeedID), update.LastEntryID); err != nil {
		return fmt.Errorf("update high-water mark: %w", err)
	}
	return nil
}

// checkFeed checks a single feed for new entries. Returns structured update
// data (nil if nothing new), the number of new entries found, and any error.
func (p *FeedPoller) checkFeed(ctx context.Context, feedID string) (*feedUpdate, int, error) {
	feedURL, err := p.state.Get(feedNamespace, feedKeyURL(feedID))
	if err != nil {
		return nil, 0, fmt.Errorf("get feed URL: %w", err)
	}
	if feedURL == "" {
		return nil, 0, fmt.Errorf("feed %q has no URL", feedID)
	}

	feedName, _ := p.state.Get(feedNamespace, feedKeyName(feedID))
	if feedName == "" {
		feedName = feedID
	}

	trustZone, _ := p.state.Get(feedNamespace, feedKeyTrustZone(feedID))
	if trustZone == "" {
		trustZone = "unknown"
	}
	notifyStr, _ := p.state.Get(feedNamespace, feedKeyNotify(feedID))
	notify := notifyStr != "false"
	wakeTarget, wakeConfigured, err := loadFeedWakeTarget(p.state, feedID)
	if err != nil {
		return nil, 0, fmt.Errorf("get wake target: %w", err)
	}

	lastEntryID, _ := p.state.Get(feedNamespace, feedKeyLastEntryID(feedID))

	feed, err := fetchFeed(ctx, p.http, feedURL)
	if err != nil {
		return nil, 0, fmt.Errorf("fetch %q: %w", feedName, err)
	}

	// Update last_checked timestamp.
	now := time.Now().UTC().Format(time.RFC3339)
	if err := p.state.Set(feedNamespace, feedKeyLastChecked(feedID), now); err != nil {
		p.logger.Warn("failed to update last_checked", "feed_id", feedID, "error", err)
	}

	if len(feed.Entries) == 0 {
		return nil, 0, nil
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
		return nil, 0, nil
	}

	// Collect new entries (entries newer than the high-water mark).
	var newEntries []FeedEntry
	foundLast := false
	for _, entry := range feed.Entries {
		if entry.ID == lastEntryID {
			foundLast = true
			break
		}
		newEntries = append(newEntries, entry)
	}

	// If the previous high-water mark is no longer present in the feed
	// (common when feeds drop older items), reseed the mark to the latest
	// entry without reporting. This avoids misreporting a large batch of
	// "new" entries that are actually old.
	if !foundLast {
		if err := p.state.Set(feedNamespace, feedKeyLastEntryID(feedID), feed.Entries[0].ID); err != nil {
			p.logger.Warn("failed to reseed high-water mark", "feed_id", feedID, "error", err)
		} else {
			p.logger.Info("feed high-water mark reseeded after missing last_entry_id",
				"feed_id", feedID,
				"feed_name", feedName,
				"latest_entry", feed.Entries[0].Title,
			)
		}
		return nil, 0, nil
	}

	if len(newEntries) == 0 {
		return nil, 0, nil
	}

	p.logger.Info("new feed entries detected",
		"feed_id", feedID,
		"feed_name", feedName,
		"new_count", len(newEntries),
	)

	// Events are always built (the wake path consumes them via
	// dispatchFeedEvents; even non-wake paths benefit from carrying
	// them in feedUpdate for observability). The legacy formatted
	// section string is only consumed by the !WakeConfigured && Notify
	// branch in CheckFeeds, so skip the string-builder work when the
	// section won't be read — for feeds with many entries this avoids
	// non-trivial wasted formatting work each poll.
	buildSection := !wakeConfigured && notify
	var sb strings.Builder
	events := make([]messages.LoopEventPayload, 0, len(newEntries))
	for _, entry := range newEntries {
		if buildSection {
			// Format section for wake message. Trust zone is shown in
			// brackets so the agent can adapt analysis depth per the
			// wake prompt guidance. The feed_id is included so the
			// agent can pass it to media_save_analysis for per-feed
			// output_path resolution.
			fmt.Fprintf(&sb, "**%s** [%s] (feed_id: %s): %s\n%s\n", feedName, trustZone, feedID, entry.Title, entry.Link)
		}
		events = append(events, messages.LoopEventPayload{
			Source:     "media_feed",
			Type:       "feed_entry",
			ID:         entry.ID,
			Title:      entry.Title,
			URL:        entry.Link,
			Summary:    fmt.Sprintf("%s [%s]", feedName, trustZone),
			ObservedAt: entry.Published,
			Metadata: map[string]string{
				"feed_id":    feedID,
				"feed_name":  feedName,
				"feed_url":   feedURL,
				"trust_zone": trustZone,
			},
		})
	}
	return &feedUpdate{
		FeedID:         feedID,
		LastEntryID:    newEntries[0].ID,
		Section:        sb.String(),
		Events:         events,
		WakeTarget:     wakeTarget,
		WakeConfigured: wakeConfigured,
		Notify:         notify,
	}, len(newEntries), nil
}

// loadFeedIndex and saveFeedIndex are defined in tools_feed.go as
// package-level functions shared by both FeedPoller and FeedTools.
