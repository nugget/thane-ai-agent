package awareness

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/homeassistant"
)

// StateGetter abstracts the Home Assistant client methods the watchlist
// providers need. Using an interface keeps the providers testable without a
// real HA instance.
type StateGetter interface {
	GetState(ctx context.Context, entityID string) (*homeassistant.State, error)
	GetStateHistory(ctx context.Context, entityID string, startTime, endTime time.Time) ([]homeassistant.State, error)
}

// WatchlistProvider implements agent.ContextProvider by fetching live state for
// all watched entities and formatting them as a markdown block for
// system prompt injection.
type WatchlistProvider struct {
	store  *WatchlistStore
	ha     StateGetter
	logger *slog.Logger
}

// NewWatchlistProvider creates a watchlist context provider.
func NewWatchlistProvider(store *WatchlistStore, ha StateGetter, logger *slog.Logger) *WatchlistProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return &WatchlistProvider{
		store:  store,
		ha:     ha,
		logger: logger,
	}
}

// GetContext returns a formatted block of watched entity states for
// injection into the agent's system prompt. Returns an empty string
// when the watchlist is empty. Implements agent.ContextProvider.
//
// Entities with rich domains (weather, climate, light, person) are
// formatted as compact JSON with relevant attributes. Default domains
// use a markdown line with state and unit. All timestamps use delta
// format per #458.
func (p *WatchlistProvider) GetContext(ctx context.Context, _ string) (string, error) {
	// Only emit untagged entities in the always-on context provider.
	// Tagged entities are emitted through WatchlistTagProvider when
	// their capability tag is active.
	subs, err := p.store.ListUntaggedSubscriptions()
	if err != nil {
		return "", fmt.Errorf("list watched entities: %w", err)
	}
	if len(subs) == 0 {
		return "", nil
	}

	now := time.Now()

	var sb strings.Builder
	sb.WriteString("### Watched Entities\n\n")

	for _, sub := range subs {
		sb.WriteString(p.renderSubscriptionContext(ctx, sub, now))
		sb.WriteByte('\n')
	}

	return sb.String(), nil
}

// WatchlistTagProvider emits watched entity context for a specific
// capability tag. Implements agent.TagContextProvider via structural
// typing. Entities are fetched fresh each turn.
type WatchlistTagProvider struct {
	tag    string
	store  *WatchlistStore
	ha     StateGetter
	logger *slog.Logger
}

// NewWatchlistTagProvider creates a tag-scoped watchlist provider.
func NewWatchlistTagProvider(tag string, store *WatchlistStore, ha StateGetter, logger *slog.Logger) *WatchlistTagProvider {
	return &WatchlistTagProvider{tag: tag, store: store, ha: ha, logger: logger}
}

// TagContext returns context for watched entities tagged with this
// provider's tag. Implements agent.TagContextProvider.
func (p *WatchlistTagProvider) TagContext(ctx context.Context) (string, error) {
	entities, err := p.store.ListByTag(p.tag)
	if err != nil {
		return "", fmt.Errorf("list watched entities for tag %s: %w", p.tag, err)
	}
	if len(entities) == 0 {
		return "", nil
	}

	now := time.Now()

	var sb strings.Builder
	fmt.Fprintf(&sb, "### Watched Entities (%s)\n\n", p.tag)

	for _, e := range entities {
		sb.WriteString(p.renderSubscriptionContext(ctx, e, now))
		sb.WriteByte('\n')
	}

	return sb.String(), nil
}

func (p *WatchlistProvider) renderSubscriptionContext(ctx context.Context, sub WatchedSubscription, now time.Time) string {
	state, err := p.ha.GetState(ctx, sub.EntityID)
	if err != nil {
		p.logger.Warn("failed to fetch watched entity state",
			"entity_id", sub.EntityID,
			"error", err,
		)
		return fmt.Sprintf("- **%s**: unavailable", sub.EntityID)
	}

	content := formatEntityContext(state, now)
	if len(sub.History) == 0 {
		return content
	}

	summaries, truncated, err := buildWatchlistHistorySummaries(ctx, p.ha, state, sub.History, now)
	if err != nil {
		p.logger.Warn("failed to fetch watched entity history",
			"entity_id", sub.EntityID,
			"history", sub.History,
			"error", err,
		)
		return content
	}
	if len(summaries) == 0 && !truncated {
		return content
	}

	return mergeHistoryIntoEntityContext(content, summaries, truncated)
}

func (p *WatchlistTagProvider) renderSubscriptionContext(ctx context.Context, sub WatchedSubscription, now time.Time) string {
	state, err := p.ha.GetState(ctx, sub.EntityID)
	if err != nil {
		p.logger.Warn("failed to fetch tagged entity state",
			"entity_id", sub.EntityID,
			"tag", p.tag,
			"error", err,
		)
		return ""
	}

	content := formatEntityContext(state, now)
	if len(sub.History) == 0 {
		return content
	}

	summaries, truncated, err := buildWatchlistHistorySummaries(ctx, p.ha, state, sub.History, now)
	if err != nil {
		p.logger.Warn("failed to fetch tagged entity history",
			"entity_id", sub.EntityID,
			"tag", p.tag,
			"history", sub.History,
			"error", err,
		)
		return content
	}
	if len(summaries) == 0 && !truncated {
		return content
	}

	return mergeHistoryIntoEntityContext(content, summaries, truncated)
}
