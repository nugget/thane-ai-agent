package awareness

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// StateGetter abstracts the Home Assistant client methods the watchlist
// providers need. Using an interface keeps the providers testable without a
// real HA instance.
type StateGetter interface {
	GetState(ctx context.Context, entityID string) (*homeassistant.State, error)
	GetStateHistory(ctx context.Context, entityID string, startTime, endTime time.Time) ([]homeassistant.State, error)
	GetWeatherForecasts(ctx context.Context, entityID, forecastType string) ([]map[string]any, error)
}

// WatchlistProvider implements [agent.TagContextProvider] by fetching
// live state for all watched entities and formatting them as a
// markdown block for system prompt injection. Registered via
// [agent.Loop.RegisterAlwaysContextProvider].
type WatchlistProvider struct {
	store      *WatchlistStore
	ha         StateGetter
	registries HARegistryClient // optional; nil disables unavailable enrichment
	logger     *slog.Logger
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

// SetRegistryClient enables device/sibling/gateway/integration
// enrichment for unavailable entities. Pass nil to disable. The
// concrete homeassistant.Client satisfies HARegistryClient out of
// the box.
func (p *WatchlistProvider) SetRegistryClient(registries HARegistryClient) {
	p.registries = registries
}

// TagContext returns a formatted block of watched entity states for
// injection into the agent's system prompt. Returns an empty string
// when the watchlist is empty. Implements [agent.TagContextProvider];
// registered via RegisterAlwaysContextProvider.
//
// Entities with rich domains (weather, climate, light, person) are
// formatted as compact JSON with relevant attributes. Default domains
// use a markdown line with state and unit. All timestamps use delta
// format per #458.
func (p *WatchlistProvider) TagContext(ctx context.Context, _ agentctx.ContextRequest) (string, error) {
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
	registries := newRenderRegistries(ctx, p.registries)

	var sb strings.Builder
	sb.WriteString("### Watched Entities\n\n")

	for _, sub := range subs {
		sb.WriteString(p.renderSubscriptionContext(ctx, sub, now, registries))
		sb.WriteByte('\n')
	}

	return sb.String(), nil
}

// WatchlistTagProvider emits watched entity context for a specific
// capability tag. Implements agent.TagContextProvider via structural
// typing. Entities are fetched fresh each turn.
type WatchlistTagProvider struct {
	tag        string
	store      *WatchlistStore
	ha         StateGetter
	registries HARegistryClient
	logger     *slog.Logger
}

// NewWatchlistTagProvider creates a tag-scoped watchlist provider.
func NewWatchlistTagProvider(tag string, store *WatchlistStore, ha StateGetter, logger *slog.Logger) *WatchlistTagProvider {
	return &WatchlistTagProvider{tag: tag, store: store, ha: ha, logger: logger}
}

// SetRegistryClient enables unavailability enrichment for this
// tag-scoped provider. See WatchlistProvider.SetRegistryClient.
func (p *WatchlistTagProvider) SetRegistryClient(registries HARegistryClient) {
	p.registries = registries
}

// TagContext returns context for watched entities tagged with this
// provider's tag. Implements [agent.TagContextProvider]; registered
// via RegisterTagContextProvider with the matching tag.
func (p *WatchlistTagProvider) TagContext(ctx context.Context, _ agentctx.ContextRequest) (string, error) {
	entities, err := p.store.ListByTag(p.tag)
	if err != nil {
		return "", fmt.Errorf("list watched entities for tag %s: %w", p.tag, err)
	}
	if len(entities) == 0 {
		return "", nil
	}

	now := time.Now()
	registries := newRenderRegistries(ctx, p.registries)

	var sb strings.Builder
	fmt.Fprintf(&sb, "### Watched Entities (%s)\n\n", p.tag)

	for _, e := range entities {
		sb.WriteString(p.renderSubscriptionContext(ctx, e, now, registries))
		sb.WriteByte('\n')
	}

	return sb.String(), nil
}

func (p *WatchlistProvider) renderSubscriptionContext(ctx context.Context, sub WatchedSubscription, now time.Time, registries *renderRegistries) string {
	state, err := p.ha.GetState(ctx, sub.EntityID)
	if err != nil {
		p.logger.Warn("failed to fetch watched entity state",
			"entity_id", sub.EntityID,
			"error", err,
		)
		return formatFetchError(sub.EntityID)
	}
	state = p.stateWithForecast(ctx, sub, state)

	content := formatEntityContext(state, now)
	content = enrichWithLastKnownGood(ctx, p.ha, content, state, now)
	content = enrichUnavailable(content, state, registries)
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

func (p *WatchlistTagProvider) renderSubscriptionContext(ctx context.Context, sub WatchedSubscription, now time.Time, registries *renderRegistries) string {
	state, err := p.ha.GetState(ctx, sub.EntityID)
	if err != nil {
		p.logger.Warn("failed to fetch tagged entity state",
			"entity_id", sub.EntityID,
			"tag", p.tag,
			"error", err,
		)
		return formatFetchError(sub.EntityID)
	}
	state = p.stateWithForecast(ctx, sub, state)

	content := formatEntityContext(state, now)
	content = enrichWithLastKnownGood(ctx, p.ha, content, state, now)
	content = enrichUnavailable(content, state, registries)
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

func (p *WatchlistProvider) stateWithForecast(ctx context.Context, sub WatchedSubscription, state *homeassistant.State) *homeassistant.State {
	return watchlistStateWithForecast(ctx, p.ha, p.logger, sub, state, "failed to fetch watched weather forecast")
}

func (p *WatchlistTagProvider) stateWithForecast(ctx context.Context, sub WatchedSubscription, state *homeassistant.State) *homeassistant.State {
	return watchlistStateWithForecast(ctx, p.ha, p.logger, sub, state, "failed to fetch tagged weather forecast", "tag", p.tag)
}

func watchlistStateWithForecast(
	ctx context.Context,
	ha StateGetter,
	logger *slog.Logger,
	sub WatchedSubscription,
	state *homeassistant.State,
	warnMsg string,
	extraLogFields ...any,
) *homeassistant.State {
	next, err := stateWithWeatherForecast(ctx, ha, state, sub.Forecast)
	if err != nil {
		if logger == nil {
			logger = slog.Default()
		}
		fields := []any{
			"entity_id", sub.EntityID,
			"forecast", sub.Forecast,
			"error", err,
		}
		fields = append(fields, extraLogFields...)
		logger.Warn(warnMsg, fields...)
	}
	// next carries the unavailability marker on failure; the original
	// behavior of "return state silently on error" hid the requested
	// forecast from the model entirely. Always thread the marker-bearing
	// state through so the formatter can surface the gap.
	return next
}

// stateWithWeatherForecast returns a state with forecast attributes
// reflecting the outcome of fetching forecastType from Home Assistant.
//
// Three cases:
//
//   - The request is not applicable (no forecast requested, non-weather
//     entity, sentinel state): the original state is returned unchanged.
//   - The fetch succeeds: a clone is returned with attrs["forecast"]
//     and attrs["forecast_type"] set.
//   - The fetch fails (non-nil err) or returns no entries: a clone is
//     returned with attrs["forecast_type"] and
//     attrs["forecast_unavailable"] set so the model-facing formatter
//     can render an explicit "asked but missing" marker rather than
//     silently presenting state without forecast.
func stateWithWeatherForecast(ctx context.Context, ha StateGetter, state *homeassistant.State, forecastType string) (*homeassistant.State, error) {
	if state == nil || forecastType == "" || entityDomain(state.EntityID) != "weather" || isSentinelState(state.State) {
		return state, nil
	}
	forecast, err := ha.GetWeatherForecasts(ctx, state.EntityID, forecastType)
	if err != nil {
		return stateMarkedForecastUnavailable(state, forecastType), err
	}
	if len(forecast) == 0 {
		return stateMarkedForecastUnavailable(state, forecastType), nil
	}

	next := *state
	attrs := make(map[string]any, len(state.Attributes)+2)
	for key, value := range state.Attributes {
		attrs[key] = value
	}
	entries := make([]any, 0, len(forecast))
	for _, entry := range forecast {
		entries = append(entries, entry)
	}
	attrs["forecast"] = entries
	attrs["forecast_type"] = forecastType
	next.Attributes = attrs
	return &next, nil
}

// stateMarkedForecastUnavailable returns a clone of state with
// forecast_type and forecast_unavailable attributes set so the
// formatter can render an explicit unavailability marker for a
// forecast that was requested but could not be returned.
func stateMarkedForecastUnavailable(state *homeassistant.State, forecastType string) *homeassistant.State {
	next := *state
	attrs := make(map[string]any, len(state.Attributes)+2)
	for key, value := range state.Attributes {
		attrs[key] = value
	}
	attrs["forecast_type"] = forecastType
	attrs["forecast_unavailable"] = true
	next.Attributes = attrs
	return &next
}
