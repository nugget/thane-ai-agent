package awareness

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
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
	loops            *looppkg.Registry
	store            *WatchlistStore
	ha               StateGetter
	registries       HARegistryClient
	logger           *slog.Logger
	maxGlobExpansion int
}

// NewLoopSubscriptionProvider creates a provider bound to the live
// loop registry. ha is the Home Assistant state getter used for the
// per-entity render; logger may be nil (defaults to slog.Default()).
// store is the always-visible watchlist store consulted at render
// time to skip entity_ids already rendered by [WatchlistProvider] —
// without that filter, an entity subscribed both always-on and
// loop-scoped would render twice in the prompt and double the HA
// fetch traffic. Pass nil only in tests that don't care about the
// double-render guard.
func NewLoopSubscriptionProvider(loops *looppkg.Registry, store *WatchlistStore, ha StateGetter, logger *slog.Logger) *LoopSubscriptionProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return &LoopSubscriptionProvider{loops: loops, store: store, ha: ha, logger: logger, maxGlobExpansion: defaultMaxGlobExpansion}
}

// SetMaxGlobExpansion overrides the per-turn cap on how many entities a
// single glob subscription renders. A value <= 0 restores the default.
func (p *LoopSubscriptionProvider) SetMaxGlobExpansion(n int) {
	p.maxGlobExpansion = normalizeMaxGlobExpansion(n)
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

	// Build the set of entity_ids already rendered by the always-
	// visible [WatchlistProvider] so we don't double-render any
	// entity that's both always-on and loop-scoped. Each duplicate
	// would add an HA fetch and a redundant prompt block; the
	// always-visible rendering wins because it would appear in
	// every loop's context anyway. We use
	// [WatchlistStore.UntaggedEntityIDSet] (bounded IN-clause query,
	// no TTL cleanup writes) so the dedup check costs one indexed
	// scan over the loop's own candidate list rather than a full
	// always-visible scan + cleanup pass — the cleanup is left to
	// [WatchlistProvider]'s own iteration on the same turn.
	// Defensive: if the store query errors, log and continue
	// without the filter (better to double-render than to break
	// context entirely).
	alreadyVisible := make(map[string]struct{})
	if p.store != nil && len(subs) > 0 {
		candidates := make([]string, 0, len(subs))
		for _, sub := range subs {
			candidates = append(candidates, sub.EntityID)
		}
		set, err := p.store.UntaggedEntityIDSet(candidates)
		if err != nil {
			p.logger.Warn("loop subscription provider could not enumerate always-visible store",
				"error", err,
			)
		} else {
			alreadyVisible = set
		}
	}

	// Glob subscriptions re-expand against the live entity set each
	// render; fetch that snapshot once (lazily, only if a glob is
	// present) and reuse it across every glob — one bulk GetStates per
	// render. Concrete subscriptions keep their targeted GetState path.
	snap := newLazyStates(p.ha, p.logger)

	// Render the body first so an all-expired list yields no header.
	// Otherwise a quiescent loop whose TTLs all elapsed would still
	// add a "### Watched Entities (loop)" line with no entries, which
	// is prompt noise.
	var body strings.Builder
	for _, sub := range subs {
		if sub.IsExpired(now) {
			continue
		}
		if homeassistant.IsEntityGlob(sub.EntityID) {
			states, statesErr := snap.get(ctx)
			// Pass alreadyVisible so a loop glob (e.g. sensor.*) doesn't
			// re-render entities the always-visible watchlist already
			// injects — same dedup the concrete path applies below.
			body.WriteString(expandGlobSubscription(ctx, p.ha, p.logger, watchedFromLoopSubscription(sub), states, statesErr, now, registries, p.maxGlobExpansion, alreadyVisible))
			continue
		}
		if _, dup := alreadyVisible[sub.EntityID]; dup {
			continue
		}
		body.WriteString(p.renderLoopSubscription(ctx, sub, now, registries))
		body.WriteByte('\n')
	}
	if body.Len() == 0 {
		return "", nil
	}
	var sb strings.Builder
	sb.WriteString("### Watched Entities (loop)\n\n")
	sb.WriteString(body.String())
	return sb.String(), nil
}

// renderLoopSubscription adapts a loop.EntitySubscription into the
// same rendering pipeline the watchlist providers use. The
// intermediate WatchedSubscription is a thin shim — once the
// migration is complete and WatchedSubscription is no longer used
// as a storage type elsewhere, the renderer can take fields
// directly.
func (p *LoopSubscriptionProvider) renderLoopSubscription(ctx context.Context, sub looppkg.EntitySubscription, now time.Time, registries *renderRegistries) string {
	w := watchedFromLoopSubscription(sub)
	state, err := p.ha.GetState(ctx, w.EntityID)
	if err != nil {
		p.logger.Warn("failed to fetch loop-scoped entity state",
			"entity_id", w.EntityID,
			"error", err,
		)
		return formatFetchError(w.EntityID)
	}
	return renderWatchedState(ctx, p.ha, p.logger, w, state, now, registries)
}

// watchedFromLoopSubscription adapts a loop.EntitySubscription into the
// WatchedSubscription shim the shared render/expansion path consumes.
func watchedFromLoopSubscription(sub looppkg.EntitySubscription) WatchedSubscription {
	return WatchedSubscription{
		EntityID: sub.EntityID,
		History:  append([]int(nil), sub.History...),
		Forecast: sub.Forecast,
		Include:  sub.Include.Clone(),
	}
}
