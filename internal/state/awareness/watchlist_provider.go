package awareness

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// StateGetter abstracts the Home Assistant client methods the watchlist
// providers need. Using an interface keeps the providers testable without a
// real HA instance.
type StateGetter interface {
	GetState(ctx context.Context, entityID string) (*homeassistant.State, error)
	GetStates(ctx context.Context) ([]homeassistant.State, error)
	GetStateHistory(ctx context.Context, entityID string, startTime, endTime time.Time) ([]homeassistant.State, error)
	GetWeatherForecasts(ctx context.Context, entityID, forecastType string) ([]map[string]any, error)
}

// WatchlistProvider implements [agent.TagContextProvider] by fetching
// live state for the always-visible (untagged) watchlist only and
// formatting it as a markdown block for system prompt injection.
// Loop-scoped subscriptions are handled by LoopSubscriptionProvider.
// Registered via [agent.Loop.RegisterAlwaysContextProvider].
type WatchlistProvider struct {
	store            *WatchlistStore
	ha               StateGetter
	registries       HARegistryClient // optional; nil disables unavailable enrichment
	logger           *slog.Logger
	maxGlobExpansion int
}

// NewWatchlistProvider creates a watchlist context provider.
func NewWatchlistProvider(store *WatchlistStore, ha StateGetter, logger *slog.Logger) *WatchlistProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return &WatchlistProvider{
		store:            store,
		ha:               ha,
		logger:           logger,
		maxGlobExpansion: defaultMaxGlobExpansion,
	}
}

// SetMaxGlobExpansion overrides the per-turn cap on how many entities a
// single glob subscription renders. A value <= 0 restores the default.
func (p *WatchlistProvider) SetMaxGlobExpansion(n int) {
	p.maxGlobExpansion = normalizeMaxGlobExpansion(n)
}

// TagContextBucket places watched entity snapshots in live state.
func (p *WatchlistProvider) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketLiveState
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
	// Always-visible entities only. Loop-owned rows are rendered by
	// [LoopSubscriptionProvider] after walking the ancestor chain, and
	// system-owned rows (the person-entity ingestion floor) never
	// render — they are ingest-mode by construction.
	rows, err := p.store.ListOwner("")
	if err != nil {
		return "", fmt.Errorf("list watched entities: %w", err)
	}
	// Ingest-only entries feed the state-change window's push pipeline;
	// they don't render per-turn state here (#1192).
	subs := make([]looppkg.EntitySubscription, 0, len(rows))
	for _, row := range rows {
		if !row.RendersState() {
			continue
		}
		subs = append(subs, row.EntitySubscription)
	}
	if len(subs) == 0 {
		return "", nil
	}

	now := time.Now()
	registries := newRenderRegistries(ctx, p.registries)

	// Glob subscriptions are re-expanded against the live entity set
	// each render. Fetch that snapshot once (lazily, only if a glob is
	// present) and reuse it for every glob — one bulk GetStates per
	// render, never a per-entity scan. Concrete subscriptions keep their
	// targeted GetState path.
	snap := newLazyStates(p.ha, p.logger)

	// Body first so an all-empty render (e.g. globs that matched nothing
	// this turn) yields no bare header.
	var body strings.Builder
	for _, sub := range subs {
		target := ParseSubscriptionTarget(sub.EntityID)
		switch {
		case target.Kind == TargetGlob:
			states, statesErr := snap.get(ctx)
			// No exclusion set — this provider IS the always-visible
			// surface, so there is nothing upstream to dedup against.
			body.WriteString(expandGlobSubscription(ctx, p.ha, p.logger, sub, states, statesErr, now, registries, p.maxGlobExpansion, nil))
		case target.IsRegistryTarget():
			states, statesErr := snap.get(ctx)
			body.WriteString(expandRegistryTargetSubscription(ctx, p.ha, p.logger, sub, target, states, statesErr, now, registries, p.maxGlobExpansion, nil))
		default:
			body.WriteString(p.renderSubscriptionContext(ctx, sub, now, registries))
			body.WriteByte('\n')
		}
	}
	if body.Len() == 0 {
		return "", nil
	}
	return "### Watched Entities\n\n" +
		"Live state for explicitly subscribed entities only. Wider Home " +
		"Assistant access (other rooms, devices, history, control, " +
		"automations) is behind the `ha` capability tag.\n\n" +
		body.String(), nil
}

func (p *WatchlistProvider) renderSubscriptionContext(ctx context.Context, sub looppkg.EntitySubscription, now time.Time, registries *renderRegistries) string {
	state, err := p.ha.GetState(ctx, sub.EntityID)
	if err != nil {
		p.logger.Warn("failed to fetch watched entity state",
			"entity_id", sub.EntityID,
			"error", err,
		)
		return formatFetchError(sub.EntityID)
	}
	return renderWatchedState(ctx, p.ha, p.logger, sub, state, now, registries)
}

func watchlistStateWithForecast(
	ctx context.Context,
	ha StateGetter,
	logger *slog.Logger,
	sub looppkg.EntitySubscription,
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
//
// Any pre-existing "forecast" attribute on the source state is
// dropped on the clone. Some HA components include a forecast array
// on /api/states; without this scrub, the rendered context could
// claim forecast_unavailable: true while still carrying a (probably
// stale) forecast array, which would mislead the model.
func stateMarkedForecastUnavailable(state *homeassistant.State, forecastType string) *homeassistant.State {
	next := *state
	attrs := make(map[string]any, len(state.Attributes)+2)
	for key, value := range state.Attributes {
		if key == "forecast" {
			continue
		}
		attrs[key] = value
	}
	attrs["forecast_type"] = forecastType
	attrs["forecast_unavailable"] = true
	next.Attributes = attrs
	return &next
}
