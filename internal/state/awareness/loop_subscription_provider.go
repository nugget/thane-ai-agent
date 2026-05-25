package awareness

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// LoopSubscriptionProvider renders the effective entity subscriptions
// for the loop running the current iteration. It replaces the
// per-loop [WatchlistTagProvider] registration: there is exactly one
// of these in the system, and it discovers the right subscription set
// at render time by reading the current loop_id from context and
// walking the live registry's ancestor chain.
//
// Always-visible entities (those subscribed with no loop scope) are
// still emitted by [WatchlistProvider]; this provider only covers
// loop- and container-scoped subscriptions.
type LoopSubscriptionProvider struct {
	loops      *looppkg.Registry
	ha         StateGetter
	registries HARegistryClient
	logger     *slog.Logger
}

// NewLoopSubscriptionProvider creates a provider bound to the live
// loop registry. ha is the Home Assistant state getter used for the
// per-entity render; logger may be nil (defaults to slog.Default()).
func NewLoopSubscriptionProvider(loops *looppkg.Registry, ha StateGetter, logger *slog.Logger) *LoopSubscriptionProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return &LoopSubscriptionProvider{loops: loops, ha: ha, logger: logger}
}

// TagContextBucket places loop-scoped entity snapshots in live state,
// matching the existing watchlist providers so prompt assembly groups
// them in the same section.
func (p *LoopSubscriptionProvider) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketLiveState
}

// SetRegistryClient enables device/sibling/gateway/integration
// enrichment for unavailable entities. Pass nil to disable.
func (p *LoopSubscriptionProvider) SetRegistryClient(registries HARegistryClient) {
	p.registries = registries
}

// TagContext implements [agent.TagContextProvider]. Reads the current
// loop_id from ctx, walks ancestor containers via the live registry,
// and renders the effective subscription list. Returns empty string
// when no loop_id is bound to ctx, the loop is no longer registered,
// or the effective list is empty — each is a normal quiescent state,
// not an error. Registered as an always-on provider via
// [agent.Loop.RegisterAlwaysContextProvider].
func (p *LoopSubscriptionProvider) TagContext(ctx context.Context, _ agentctx.ContextRequest) (string, error) {
	if p.loops == nil {
		return "", nil
	}
	loopID := looppkg.LoopIDFromContext(ctx)
	if loopID == "" {
		return "", nil
	}
	subs := p.loops.AncestorSubscriptions(loopID)
	if len(subs) == 0 {
		return "", nil
	}

	now := time.Now()
	registries := newRenderRegistries(ctx, p.registries)

	var sb strings.Builder
	sb.WriteString("### Watched Entities (loop)\n\n")
	for _, sub := range subs {
		if sub.IsExpired(now) {
			continue
		}
		sb.WriteString(p.renderLoopSubscription(ctx, sub, now, registries))
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

// renderLoopSubscription adapts a loop.EntitySubscription into the
// same rendering pipeline the watchlist providers use. The
// intermediate WatchedSubscription is a thin shim — once the
// migration is complete and WatchedSubscription is no longer used
// as a storage type elsewhere, the renderer can take fields
// directly.
func (p *LoopSubscriptionProvider) renderLoopSubscription(ctx context.Context, sub looppkg.EntitySubscription, now time.Time, registries *renderRegistries) string {
	w := WatchedSubscription{
		EntityID: sub.EntityID,
		History:  append([]int(nil), sub.History...),
		Forecast: sub.Forecast,
	}
	state, err := p.ha.GetState(ctx, w.EntityID)
	if err != nil {
		p.logger.Warn("failed to fetch loop-scoped entity state",
			"entity_id", w.EntityID,
			"error", err,
		)
		return formatFetchError(w.EntityID)
	}
	state = watchlistStateWithForecast(ctx, p.ha, p.logger, w, state, "failed to fetch loop-scoped weather forecast")

	content := formatEntityContext(state, now)
	content = enrichWithLastKnownGood(ctx, p.ha, content, state, now)
	content = enrichUnavailable(content, state, registries)
	if len(w.History) == 0 {
		return content
	}

	summaries, truncated, err := buildWatchlistHistorySummaries(ctx, p.ha, state, w.History, now)
	if err != nil {
		p.logger.Warn("failed to fetch loop-scoped entity history",
			"entity_id", w.EntityID,
			"history", w.History,
			"error", err,
		)
		return content
	}
	if len(summaries) == 0 && !truncated {
		return content
	}
	return mergeHistoryIntoEntityContext(content, summaries, truncated)
}
