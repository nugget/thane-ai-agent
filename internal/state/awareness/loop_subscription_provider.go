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
// Always-visible entities (core-owned rows) are still emitted by
// [WatchlistProvider]; this provider only covers loop- and
// container-scoped subscriptions.
type LoopSubscriptionProvider struct {
	loops            *looppkg.Registry
	store            *WatchlistStore
	ha               StateGetter
	registries       HARegistryClient
	transitions      TransitionSource // optional; nil marks requested logs unavailable
	logger           *slog.Logger
	maxGlobExpansion int
}

// SetTransitionSource wires the per-entity retention that backs
// declared transition logs (#1210). Pass nil to render requested logs
// as unavailable rather than empty.
func (p *LoopSubscriptionProvider) SetTransitionSource(source TransitionSource) {
	p.transitions = source
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
func (p *LoopSubscriptionProvider) TagContext(ctx context.Context, req agentctx.ContextRequest) (string, error) {
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
	// [WatchlistStore.CoreEntityGates] (bounded IN-clause query,
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
		gates, err := p.store.CoreEntityGates(candidates)
		if err != nil {
			p.logger.Warn("loop subscription provider could not enumerate always-visible store",
				"error", err,
			)
		} else {
			// Only rows the global tier will actually render this turn
			// suppress a loop-scoped render: a gated global row whose
			// tag is inactive renders nothing, so the loop's own
			// subscription for the same entity must still show (#1213).
			for id, gate := range gates {
				if gate == "" || req.ActiveTags[gate] {
					alreadyVisible[id] = struct{}{}
				}
			}
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
		// Ingest-only entries feed the state-change window's push
		// pipeline; they don't render per-turn state here (#1192).
		// Tag-gated entries render only while their capability tag is
		// active (#1213). First-wins dedup in the ancestor walk
		// composes with the gate: a leaf's gated declaration shadows a
		// container's ungated one for the same entity, so the entity
		// is absent while the leaf's tag is off — the closest
		// declaration wins, conditions included.
		if !sub.RendersState() || !sub.GateOpen(req.ActiveTags) {
			continue
		}
		target := ParseSubscriptionTarget(sub.EntityID)
		switch {
		case target.Kind == TargetGlob:
			states, statesErr := snap.get(ctx)
			// Pass alreadyVisible so a loop glob (e.g. sensor.*) doesn't
			// re-render entities the always-visible watchlist already
			// injects — same dedup the concrete path applies below.
			body.WriteString(expandGlobSubscription(ctx, p.ha, p.logger, sub, states, statesErr, now, registries, p.transitions, p.maxGlobExpansion, alreadyVisible))
		case target.IsRegistryTarget():
			states, statesErr := snap.get(ctx)
			body.WriteString(expandRegistryTargetSubscription(ctx, p.ha, p.logger, sub, target, states, statesErr, now, registries, p.transitions, p.maxGlobExpansion, alreadyVisible))
		default:
			if _, dup := alreadyVisible[sub.EntityID]; dup {
				continue
			}
			body.WriteString(p.renderLoopSubscription(ctx, sub, now, registries))
			body.WriteByte('\n')
		}
	}
	if body.Len() == 0 {
		return "", nil
	}
	var sb strings.Builder
	sb.WriteString("### Watched Entities (loop)\n\n")
	sb.WriteString(body.String())
	return sb.String(), nil
}

// renderLoopSubscription feeds a spec-declared subscription through
// the same per-entity renderer the always-visible watchlist uses. Both
// sources speak [looppkg.EntitySubscription] — the render-time adapter
// that used to translate between two vocabularies is gone (#1209).
func (p *LoopSubscriptionProvider) renderLoopSubscription(ctx context.Context, sub looppkg.EntitySubscription, now time.Time, registries *renderRegistries) string {
	state, err := p.ha.GetState(ctx, sub.EntityID)
	if err != nil {
		p.logger.Warn("failed to fetch loop-scoped entity state",
			"entity_id", sub.EntityID,
			"error", err,
		)
		return formatFetchError(sub.EntityID)
	}
	return renderWatchedState(ctx, p.ha, p.logger, sub, state, now, registries, p.transitions)
}
