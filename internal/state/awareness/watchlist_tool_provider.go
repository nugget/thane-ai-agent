package awareness

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// LoopSubscriptionMutator applies a transformation to a named loop's
// Spec.Subscriptions, persisting the spec and patching the live loop.
// The app wires its subscription mutator here so the owner-parameter
// path of add/remove_entity_subscription routes through the same
// single-writer persistence discipline watch_entity uses.
type LoopSubscriptionMutator func(ctx context.Context, loopName string, mutate func([]looppkg.EntitySubscription) ([]looppkg.EntitySubscription, error)) ([]looppkg.EntitySubscription, error)

// WatchlistTools is the [tools.Provider] for the add_entity_subscription,
// list_entity_subscriptions, and remove_entity_subscription tools — the
// one tool family over the one subscription registry. Ownership is a
// parameter: no owner means the always-visible tier, a loop name routes
// to that loop's spec (compiled back into the registry on persist), and
// the reserved system owner is read-only. An optional HA registry
// client previews what a glob or area/label/floor target expands to at
// the moment it is authored — so the model learns immediately that
// area:office matches three entities, or that a typo'd area:atrium
// matches none.
type WatchlistTools struct {
	store          *WatchlistStore
	registry       HARegistryClient
	loopMutator    LoopSubscriptionMutator
	onIngestChange func()
	logger         *slog.Logger
}

// WatchlistToolsConfig captures the dependencies for [NewWatchlistTools].
type WatchlistToolsConfig struct {
	// Store is the persistent watchlist store. Required.
	Store *WatchlistStore
	// Registry is the HA client used to preview a target's current
	// membership when a subscription is authored or listed. Optional:
	// when nil, subscriptions still work but carry no expansion preview.
	Registry HARegistryClient
	// LoopMutator routes owner-addressed mutations onto the named
	// loop's Spec.Subscriptions. Optional: when nil, the owner
	// parameter returns a teaching error instead of mutating.
	LoopMutator LoopSubscriptionMutator
	// OnIngestChange is invoked after an always-visible-tier mutation
	// that may affect the state-watcher ingestion filter (an
	// ingest/both-mode add, or any removal), so the wiring can rebuild
	// the filter from the registry. Owner-addressed mutations do NOT
	// fire it — they persist the spec, and the app's persist hook
	// re-mirrors the registry and rebuilds the filter there. Optional.
	OnIngestChange func()
	// Logger defaults to slog.Default when nil.
	Logger *slog.Logger
}

// NewWatchlistTools constructs the provider. Panics if Store is nil
// so misconfiguration is caught at wiring time rather than the first
// tool invocation.
func NewWatchlistTools(cfg WatchlistToolsConfig) *WatchlistTools {
	if cfg.Store == nil {
		panic("awareness: WatchlistTools requires a non-nil Store")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &WatchlistTools{
		store:          cfg.Store,
		registry:       cfg.Registry,
		loopMutator:    cfg.LoopMutator,
		onIngestChange: cfg.OnIngestChange,
		logger:         logger,
	}
}

// Name implements [tools.Provider].
func (w *WatchlistTools) Name() string { return "awareness.watchlist" }

// retiredTagsError is the teaching error for the retired lens-tag
// vocabulary (#1209). It distinguishes the two things a model
// reaching for the old parameter might actually mean: binding a
// subscription to a loop (the owner parameter) and gating its
// visibility on a capability tag (the requires_tag parameter, #1213).
func retiredTagsError(param string) error {
	return fmt.Errorf("the %s parameter is retired: lens-tag scoping no longer exists — to bind a subscription to a loop use the owner parameter; to render it only while a capability tag is active use requires_tag", param)
}

// Tools implements [tools.Provider]. Returns the three subscription
// registry tools with handlers bound to w's store.
func (w *WatchlistTools) Tools() []*tools.Tool {
	ownerParam := map[string]any{
		"type":        "string",
		"description": "Who owns the subscription. Omit (or pass \"core\") for an always-visible subscription: it lands on core — the root container whose context every turn shares — and appears everywhere. Pass a loop definition name to subscribe that loop instead: the entry lands on its spec's subscriptions and follows the loop's lifecycle. From inside a loop's own turn, prefer watch_entity (no name needed). The reserved owner \"system\" is read-only (runtime-seeded rows such as the person-presence ingestion floor, configured via person.track).",
	}
	return []*tools.Tool{
		{
			Name: "add_entity_subscription",
			Description: "Subscribe to a Home Assistant entity so its live state is injected into the model's context. " +
				"Without owner this adds an always-visible subscription owned by core (the root container whose context every turn shares): the entity appears on every turn — this is how you, or a conversation, watch an entity in your own field of view. " +
				"With owner set to a loop definition name, the subscription lands on that loop's spec instead; from inside a loop's own turn use watch_entity. " +
				"Use ttl_seconds for subscriptions that should expire after a bounded task. Use history to include historical state snapshots at specific intervals. Use forecast for weather entities when future weather context is needed. " +
				"When Home Assistant is connected, a glob or area/label/floor target reports how many entities it currently matches (with a sample) — and flags a zero-member expansion, which almost always means a typo'd id or an empty group.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"entity_id": map[string]any{
						"type":        "string",
						"description": "What to subscribe to. Any of: a concrete entity ID (sensor.office_temperature); a glob (binary_sensor.*door*, *_temperature); or an organizational target — area:<area_id>, label:<label_id>, floor:<floor_id> (e.g. area:office) — which watches that group's current members, re-resolved live each turn so membership follows the home as devices move (capped per turn like globs). Use ha_registry_search to find area/label/floor IDs.",
					},
					"owner": ownerParam,
					"history": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "integer"},
						"description": "Historical context windows in seconds. When set, watched-entity context includes a compact summary for each window. E.g., [600, 3600, 86400] adds recent summaries for 10min, 1hr, and 1day windows.",
					},
					"forecast": map[string]any{
						"type":        "string",
						"enum":        []string{"daily", "hourly", "twice_daily", "none"},
						"description": "For weather.* entities, fetch this Home Assistant weather.get_forecasts type each turn and include the compact forecast in watched-entity context. Use none to clear forecast fetching.",
					},
					"ttl_seconds": map[string]any{
						"type":        "integer",
						"description": "Optional expiration in seconds. After this TTL elapses, the subscription is automatically removed from future context injection.",
					},
					"mode": map[string]any{
						"type":        "string",
						"enum":        []string{"render", "ingest", "both"},
						"description": "What the subscription feeds. render (default): inject live state into context each turn. ingest: feed the recent-state-changes window only — the entity's transitions appear there without per-turn state injection. both: do both. ingest/both accept entity ids and globs, not area/label/floor targets.",
					},
					"self_only": map[string]any{
						"type":        "boolean",
						"description": "Only meaningful with owner set to a container loop: true keeps the subscription out of descendant loops' inherited sets (the container still sees it). Default false — container subscriptions cascade.",
					},
					"requires_tag": map[string]any{
						"type":        "string",
						"description": "Optional capability tag gating visibility: the subscription renders only while this tag is active in the consuming context. Use it to build a macro set — one tag activation surfaces a subject's tagged documents and its related entities together, and deactivation drops both. Render-only: incompatible with mode ingest/both (a transition log MAY be gated — its capture continues, only the rendering follows the tag).",
					},
					"transitions": map[string]any{
						"type":        "integer",
						"description": "Include the entity's last n observed state changes in its rendered block ({from, to, ago}, class-aware). Declaring a log automatically feeds this entity into the state-change capture — no mode needed. Capped per subscription; truncation is advertised. Entity ids and globs only.",
					},
					"transitions_window_seconds": map[string]any{
						"type":        "integer",
						"description": "Bound the transition log to changes within this trailing window (seconds). Combine with transitions for window-filtered last-n, or set alone to render every retained change inside the window (still capped).",
					},
					"wake": map[string]any{
						"type":        "boolean",
						"description": "Wake the owning loop when this entity changes — debounced and coalesced, so a chattering sensor becomes one wake carrying the latest change, never a wakestorm. Requires owner (an always-visible subscription has nobody to wake). Capture follows automatically. Entity ids and globs only; incompatible with requires_tag.",
					},
					"wake_debounce_seconds": map[string]any{
						"type":        "integer",
						"description": "How long this subscription's changes coalesce before waking its loop (default a few seconds). A loop's effective cadence follows its twitchiest wake subscription — one wake drains everything pending.",
					},
					"include": tools.EntityMetadataIncludeParameter(),
				},
				"required": []string{"entity_id"},
			},
			Handler: w.handleAddEntitySubscription,
		},
		{
			Name: "list_entity_subscriptions",
			Description: "List the entity-subscription registry: every subscription with its owner — always-visible rows (owner \"\"), loop-owned rows compiled from loop specs, and system-seeded rows such as the person-presence ingestion floor. " +
				"When Home Assistant is connected, each glob or area/label/floor subscription carries an expansion object with its current member count and a sample, so a subscription that currently matches nothing is visible at a glance. " +
				"Inherited effective sets for a running loop (own + container ancestors') are surfaced by loop_definition_get on that loop's name.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"owner": map[string]any{
						"type":        "string",
						"description": "Optional owner filter: omit the parameter to list everything; pass \"core\" for the always-visible tier, a loop definition name, or \"system\" for runtime-seeded rows.",
					},
					"entity_id": map[string]any{
						"type":        "string",
						"description": "Optional exact entity_id filter.",
					},
				},
			},
			Handler: w.handleListEntitySubscriptions,
		},
		{
			Name:        "remove_entity_subscription",
			Description: "Remove an entity subscription. Without owner this removes an always-visible row; with owner set to a loop definition name it removes that loop's own spec entry (inherited entries from container ancestors are untouched — remove them on the ancestor). System-owned rows cannot be removed here; they re-seed from configuration.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"entity_id": map[string]any{
						"type":        "string",
						"description": "The Home Assistant entity ID to unsubscribe from.",
					},
					"owner": ownerParam,
				},
				"required": []string{"entity_id"},
			},
			Handler: w.handleRemoveEntitySubscription,
		},
	}
}

func (w *WatchlistTools) handleAddEntitySubscription(ctx context.Context, args map[string]any) (string, error) {
	entityID, _ := args["entity_id"].(string)
	if entityID == "" {
		return "", fmt.Errorf("entity_id is required")
	}
	if err := homeassistant.ValidateEntityTarget(entityID); err != nil {
		return "", fmt.Errorf("entity_id %q: invalid glob pattern: %w", entityID, err)
	}
	if _, present := args["tags"]; present {
		return "", retiredTagsError("tags")
	}

	owner := strings.TrimSpace(stringArg(args, "owner"))
	if owner == OwnerSystem {
		return "", fmt.Errorf("owner %q is reserved for runtime-seeded rows (the person-presence ingestion floor, configured via person.track) and cannot be written by tools; omit owner for an always-visible subscription or name a loop", OwnerSystem)
	}
	// The anonymous always-visible tier collapsed into core (#1208):
	// omitted owner means core, and core routes store-direct — it has
	// no persisted definition for the spec-mutation path to edit.
	if owner == "" {
		owner = OwnerCore
	}

	history, err := parseWatchlistHistoryArg(args["history"])
	if err != nil {
		return "", err
	}
	ttlSeconds, err := watchlistIntArg(args["ttl_seconds"], "ttl_seconds")
	if err != nil {
		return "", err
	}
	if ttlSeconds < 0 {
		return "", fmt.Errorf("ttl_seconds must be >= 0")
	}
	rawForecast, forecastSet := args["forecast"]
	forecast, err := parseWatchlistForecastArg(rawForecast)
	if err != nil {
		return "", err
	}
	if forecast != "" && !strings.HasPrefix(entityID, "weather.") {
		return "", fmt.Errorf("forecast can only be set for weather.* entities; got %s", entityID)
	}
	include, err := tools.ParseEntityMetadataIncludesArg(args["include"], "include")
	if err != nil {
		return "", err
	}
	rawMode := stringArg(args, "mode")
	mode, err := looppkg.NormalizeSubscriptionMode(rawMode)
	if err != nil {
		return "", err
	}
	selfOnly, _ := args["self_only"].(bool)
	if selfOnly && owner == OwnerCore {
		return "", fmt.Errorf("self_only has no effect on core's subscriptions — they render in every context by definition; pass owner with another container's name, or drop self_only")
	}
	requiresTag := strings.TrimSpace(stringArg(args, "requires_tag"))
	transitions, err := watchlistIntArg(args["transitions"], "transitions")
	if err != nil {
		return "", err
	}
	transitionsWindow, err := watchlistIntArg(args["transitions_window_seconds"], "transitions_window_seconds")
	if err != nil {
		return "", err
	}
	if transitions < 0 || transitionsWindow < 0 {
		return "", fmt.Errorf("transitions and transitions_window_seconds must be >= 0")
	}
	if transitions > maxTransitionsPerSubscription {
		return "", fmt.Errorf("transitions is capped at %d per subscription — ask for fewer, or add transitions_window_seconds to bound by recency instead", maxTransitionsPerSubscription)
	}
	wake, _ := args["wake"].(bool)
	wakeDebounce, err := watchlistIntArg(args["wake_debounce_seconds"], "wake_debounce_seconds")
	if err != nil {
		return "", err
	}
	if wakeDebounce < 0 {
		return "", fmt.Errorf("wake_debounce_seconds must be >= 0")
	}

	sub := looppkg.EntitySubscription{
		EntityID:                 entityID,
		History:                  history,
		Forecast:                 forecast,
		Include:                  tools.EntityMetadataIncludesPointer(include),
		TTLSeconds:               ttlSeconds,
		AddedAt:                  time.Now().UTC(),
		Mode:                     mode,
		SelfOnly:                 selfOnly,
		RequiresTag:              requiresTag,
		Transitions:              transitions,
		TransitionsWindowSeconds: transitionsWindow,
		Wake:                     wake,
		WakeDebounceSeconds:      wakeDebounce,
	}

	if sub.Wake {
		// The wake feed awakens the OWNING loop; core is the root
		// container and never iterates, so a core-owned wake would
		// fire into the void.
		if owner == OwnerCore {
			return "", fmt.Errorf("wake awakens the owning loop, and core is a container that never iterates — pass owner with the executing loop to wake, or use watch_entity from inside it")
		}
		if requiresTag != "" {
			return "", fmt.Errorf("wake cannot combine with requires_tag — waking must not follow tag state; drop one of the two")
		}
		if ParseSubscriptionTarget(entityID).IsRegistryTarget() {
			return "", fmt.Errorf("wake needs the entity's event stream, and area/label/floor targets cannot feed the ingestion filter — subscribe a concrete entity or glob to wake on")
		}
	}
	if sub.WantsTransitions() {
		// A transition log is a render feature; an ingest-only mode
		// renders nothing, so the combination declares a log nobody
		// would ever see.
		if sub.Mode == looppkg.SubscriptionModeIngest {
			return "", fmt.Errorf("a transition log renders into context, but mode ingest never renders — drop the transition options, or use mode render or both")
		}
		// Derived capture rides the ingestion filter, which speaks
		// concrete ids and globs only (#1210).
		if ParseSubscriptionTarget(entityID).IsRegistryTarget() {
			return "", fmt.Errorf("a transition log needs the entity's event stream, and area/label/floor targets cannot feed the ingestion filter — subscribe a concrete entity or glob for transitions, or drop the transition options")
		}
	}
	if sub.FeedsIngest() {
		// The gate is render-only: capture must never depend on tag
		// state, or the StateWatcher filter would flap with tag
		// activation (#1213).
		if requiresTag != "" {
			return "", fmt.Errorf("requires_tag gates rendering only and cannot combine with mode %q — capture does not follow tag state; drop requires_tag, or use mode render for a tag-gated subscription", rawMode)
		}
		// The ingestion filter speaks the EntityFilter's native
		// vocabulary: concrete ids and globs. Registry targets would be
		// silently approximated, so they are rejected outright (#1192).
		if ParseSubscriptionTarget(entityID).IsRegistryTarget() {
			return "", fmt.Errorf("mode %q accepts entity ids and globs only — area/label/floor targets cannot feed the ingestion filter; subscribe the target with mode render, or list its member entities", rawMode)
		}
	}
	// Both explicit ingest modes and transition-log derivation occupy
	// ingestion-filter entries; the cap covers either route.
	if err := CheckIngestCapacity(w.store, sub); err != nil {
		return "", err
	}

	if owner != OwnerCore {
		if w.loopMutator == nil {
			return "", fmt.Errorf("loop-owned subscriptions are not available in this runtime; omit owner for an always-visible subscription")
		}
		// The mutator persists the spec, and the app's persist hook
		// re-mirrors the registry and rebuilds the ingestion filter —
		// no onIngestChange here or the filter would rebuild twice.
		if _, err := w.loopMutator(ctx, owner, func(current []looppkg.EntitySubscription) ([]looppkg.EntitySubscription, error) {
			return upsertLoopSubscription(current, sub), nil
		}); err != nil {
			return "", err
		}
	} else {
		// Core rows are the source of truth themselves — core has no
		// persisted spec by design, so the store write IS the mutation.
		if err := w.store.Upsert(OwnerCore, sub); err != nil {
			return "", fmt.Errorf("add to watchlist: %w", err)
		}
		// Explicit ingest modes and transition-log derivation both
		// change the ingestion filter.
		if (sub.FeedsIngest() || sub.WantsTransitions()) && w.onIngestChange != nil {
			w.onIngestChange()
		}
	}

	msg := fmt.Sprintf("Now watching %s", entityID)
	if owner != OwnerCore {
		msg = fmt.Sprintf("Loop %q is now watching %s", owner, entityID)
	}
	switch mode {
	case looppkg.SubscriptionModeIngest:
		msg += " (mode: ingest — transitions feed the recent-state-changes window; no per-turn state render)"
	case looppkg.SubscriptionModeBoth:
		msg += " (mode: both — transitions feed the recent-state-changes window and live state renders each turn)"
	}
	if len(history) > 0 {
		parts := make([]string, len(history))
		for i, h := range history {
			parts[i] = fmt.Sprintf("%ds", h)
		}
		msg += fmt.Sprintf(" (history windows: %s)", strings.Join(parts, ", "))
	}
	if forecast != "" {
		msg += fmt.Sprintf(" (forecast: %s)", forecast)
	} else if forecastSet {
		msg += " (forecast: none)"
	}
	if ttlSeconds > 0 {
		msg += fmt.Sprintf(" (expires in %ds)", ttlSeconds)
	}
	if selfOnly {
		msg += " (self_only: not inherited by descendant loops)"
	}
	if wake {
		if wakeDebounce > 0 {
			msg += fmt.Sprintf(" (wake: the loop wakes on change, coalesced over %ds)", wakeDebounce)
		} else {
			msg += " (wake: the loop wakes on change, debounced; capture follows automatically)"
		}
	}
	if requiresTag != "" {
		msg += fmt.Sprintf(" (renders only while tag %q is active)", requiresTag)
	}
	if sub.WantsTransitions() {
		switch {
		case transitions > 0 && transitionsWindow > 0:
			msg += fmt.Sprintf(" (transition log: last %d changes within %ds; capture follows automatically)", transitions, transitionsWindow)
		case transitions > 0:
			msg += fmt.Sprintf(" (transition log: last %d changes; capture follows automatically)", transitions)
		default:
			msg += fmt.Sprintf(" (transition log: changes within %ds, capped; capture follows automatically)", transitionsWindow)
		}
	}
	if include.Any() {
		msg += " (includes HA metadata)"
	}
	msg += "."

	// Author-time expansion preview: for a glob or area/label/floor
	// target, report what it matches right now so the model isn't left
	// guessing — and a zero-member expansion is flagged loudly, since it
	// is almost always a typo'd id that would otherwise inject nothing
	// forever. A concrete entity_id is its own membership and needs none.
	if target := ParseSubscriptionTarget(entityID); target.Kind != TargetEntity {
		exp, perr := previewTargetExpansion(newRenderRegistries(ctx, w.registry), target)
		switch {
		case perr != nil:
			// The preview couldn't run (transient registry read failure).
			// Say so rather than returning a bare "Now watching …" — an
			// unspoken failure would read as a clean, validated subscribe,
			// the exact silent-accept this preview exists to prevent.
			w.logger.Warn("subscription target expansion preview failed",
				"entity_id", entityID, "error", perr)
			msg += " Note: couldn't preview its current expansion this turn" +
				" (registry read failed), so the member count is unverified."
		case exp == nil:
			// No registry client wired — subscription stands without a preview.
		case exp.Count == 0:
			msg += " Note: this target matches no entities right now —" +
				" check the id (a likely typo or an empty group); it will" +
				" inject nothing until it has members."
		default:
			msg += fmt.Sprintf(" Currently matches %d %s: %s%s.",
				exp.Count, entityNoun(exp.Count), strings.Join(exp.Sample, ", "),
				moreMembersSuffix(exp.Count, len(exp.Sample)))
		}
	}

	w.logger.Info("entity subscription added",
		"entity_id", entityID, "owner", owner, "history", history, "forecast", forecast, "include", include, "ttl_seconds", ttlSeconds, "mode", mode, "self_only", selfOnly)
	return msg, nil
}

func (w *WatchlistTools) handleListEntitySubscriptions(ctx context.Context, args map[string]any) (string, error) {
	if _, present := args["tag"]; present {
		return "", retiredTagsError("tag")
	}
	ownerFilter, ownerSet := args["owner"].(string)
	ownerFilter = strings.TrimSpace(ownerFilter)
	entityID, _ := args["entity_id"].(string)

	var (
		rows []SubscriptionRow
		err  error
	)
	if ownerSet && ownerFilter != "" {
		rows, err = w.store.ListOwner(ownerFilter)
	} else {
		rows, err = w.store.ListAll()
	}
	if err != nil {
		return "", fmt.Errorf("list watchlist subscriptions: %w", err)
	}

	now := time.Now()
	// One registries bundle shared across the whole list: it fetches each
	// registry once and every glob/registry target's expansion reuses it.
	registries := newRenderRegistries(ctx, w.registry)
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if entityID != "" && row.EntityID != entityID {
			continue
		}
		mode := row.Mode
		if mode == "" {
			mode = looppkg.SubscriptionModeRender
		}
		item := map[string]any{
			"entity_id":      row.EntityID,
			"owner":          row.Owner,
			"mode":           mode,
			"always_visible": row.Owner == OwnerCore,
		}
		if len(row.History) > 0 {
			item["history"] = append([]int(nil), row.History...)
		}
		if row.Forecast != "" {
			item["forecast"] = row.Forecast
		}
		if row.Include != nil && row.Include.Any() {
			item["include"] = row.Include
		}
		if row.SelfOnly {
			item["self_only"] = true
		}
		// The gate is shown verbatim with no open/closed judgment:
		// it resolves per-consumer, and the list runs in one context.
		if row.RequiresTag != "" {
			item["requires_tag"] = row.RequiresTag
		}
		if row.Transitions > 0 {
			item["transitions"] = row.Transitions
		}
		if row.TransitionsWindowSeconds > 0 {
			item["transitions_window_seconds"] = row.TransitionsWindowSeconds
		}
		if row.Wake {
			item["wake"] = true
		}
		if row.WakeDebounceSeconds > 0 {
			item["wake_debounce_seconds"] = row.WakeDebounceSeconds
		}
		if row.TTLSeconds > 0 && !row.AddedAt.IsZero() {
			expiresAt := row.AddedAt.Add(time.Duration(row.TTLSeconds) * time.Second)
			item["expires_delta"] = promptfmt.FormatDeltaOnly(expiresAt, now)
		}
		// Show each glob/registry target's current expansion so a
		// silently-empty subscription is visible at a glance.
		if target := ParseSubscriptionTarget(row.EntityID); target.Kind != TargetEntity {
			if exp, err := previewTargetExpansion(registries, target); err != nil {
				// A failed read is marked, not omitted — an absent
				// expansion would read as "not a registry target" rather
				// than "couldn't resolve it this turn."
				w.logger.Warn("subscription target expansion preview failed",
					"entity_id", row.EntityID, "error", err)
				item["expansion"] = map[string]any{
					"unavailable": true,
					"note":        "registry read failed this turn; membership unverified",
				}
			} else if exp != nil {
				expObj := map[string]any{"count": exp.Count}
				if len(exp.Sample) > 0 {
					expObj["sample"] = exp.Sample
				}
				if exp.Count == 0 {
					expObj["note"] = "matches no entities right now — likely a typo'd id or an empty group"
				}
				item["expansion"] = expObj
			}
		}
		items = append(items, item)
	}

	payload, err := json.Marshal(map[string]any{
		"count": len(items),
		"items": items,
	})
	if err != nil {
		return "", fmt.Errorf("marshal watchlist subscriptions: %w", err)
	}
	return string(payload), nil
}

func (w *WatchlistTools) handleRemoveEntitySubscription(ctx context.Context, args map[string]any) (string, error) {
	entityID, _ := args["entity_id"].(string)
	if entityID == "" {
		return "", fmt.Errorf("entity_id is required")
	}
	if _, present := args["tags"]; present {
		return "", retiredTagsError("tags")
	}

	owner := strings.TrimSpace(stringArg(args, "owner"))
	if owner == "" {
		owner = OwnerCore
	}
	switch {
	case owner == OwnerSystem:
		return "", fmt.Errorf("system-owned rows are seeded from configuration (person.track) and re-appear at the next startup; change the configuration instead of removing them here")
	case owner != OwnerCore:
		// The mutator persists the spec, and the app's persist hook
		// re-mirrors the registry and rebuilds the ingestion filter —
		// no onIngestChange here or the filter would rebuild twice.
		if w.loopMutator == nil {
			return "", fmt.Errorf("loop-owned subscriptions are not available in this runtime; omit owner to remove an always-visible subscription")
		}
		if _, err := w.loopMutator(ctx, owner, func(current []looppkg.EntitySubscription) ([]looppkg.EntitySubscription, error) {
			return dropLoopSubscription(current, entityID), nil
		}); err != nil {
			return "", err
		}
	default:
		if err := w.store.Remove(OwnerCore, entityID); err != nil {
			return "", fmt.Errorf("remove from watchlist: %w", err)
		}
		// Unconditional for global removes: the handler doesn't know
		// whether the removed row fed the filter, and a spurious
		// rebuild is cheaper than a stale one.
		if w.onIngestChange != nil {
			w.onIngestChange()
		}
	}
	w.logger.Info("entity subscription removed", "entity_id", entityID, "owner", owner)
	if owner != OwnerCore {
		return fmt.Sprintf("Loop %q stopped watching %s.", owner, entityID), nil
	}
	return fmt.Sprintf("Stopped watching %s.", entityID), nil
}

// upsertLoopSubscription replaces any existing entry for sub.EntityID
// and appends fresh ones at the end — the same replace-don't-duplicate
// contract watch_entity applies.
func upsertLoopSubscription(current []looppkg.EntitySubscription, sub looppkg.EntitySubscription) []looppkg.EntitySubscription {
	out := make([]looppkg.EntitySubscription, 0, len(current)+1)
	for _, c := range current {
		if c.EntityID == sub.EntityID {
			continue
		}
		out = append(out, c)
	}
	return append(out, sub)
}

// dropLoopSubscription returns current minus the entry matching
// entityID. Missing IDs are silently a no-op, matching unwatch_entity.
func dropLoopSubscription(current []looppkg.EntitySubscription, entityID string) []looppkg.EntitySubscription {
	out := make([]looppkg.EntitySubscription, 0, len(current))
	for _, c := range current {
		if c.EntityID == entityID {
			continue
		}
		out = append(out, c)
	}
	return out
}

func stringArg(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func parseWatchlistForecastArg(raw any) (string, error) {
	if raw == nil {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("forecast must be a string")
	}
	return looppkg.NormalizeSubscriptionForecast(value)
}

func parseWatchlistHistoryArg(raw any) ([]int, error) {
	rawHist, ok := raw.([]any)
	if !ok {
		return nil, nil
	}
	var history []int
	for i, rh := range rawHist {
		switch v := rh.(type) {
		case float64:
			if v != float64(int(v)) {
				return nil, fmt.Errorf("history[%d]: must be a whole number of seconds, got %v", i, v)
			}
			iv := int(v)
			if iv <= 0 {
				return nil, fmt.Errorf("history[%d]: must be positive, got %d", i, iv)
			}
			history = append(history, iv)
		case int:
			if v <= 0 {
				return nil, fmt.Errorf("history[%d]: must be positive, got %d", i, v)
			}
			history = append(history, v)
		default:
			return nil, fmt.Errorf("history[%d]: expected integer seconds, got %T", i, rh)
		}
	}
	return history, nil
}

func watchlistIntArg(raw any, field string) (int, error) {
	switch v := raw.(type) {
	case nil:
		return 0, nil
	case float64:
		if v != float64(int(v)) {
			return 0, fmt.Errorf("%s must be a whole number, got %v", field, v)
		}
		return int(v), nil
	case int:
		return v, nil
	default:
		return 0, fmt.Errorf("%s must be an integer, got %T", field, raw)
	}
}
