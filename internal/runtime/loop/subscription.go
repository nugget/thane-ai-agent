package loop

import (
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

// EntitySubscription is one entity that a loop wants to see in context
// every iteration. It carries everything the awareness renderer needs to
// fetch current state plus optional history windows and forecast shape,
// without any indirection through a separate watchlist row.
//
// Subscriptions live directly on [Spec.Subscriptions]. A descendant
// loop's effective subscription list is the union of its own +
// every container ancestor's, deduplicated by EntityID with first-wins
// (own declarations take precedence over inherited ones) — see
// [Registry.AncestorSubscriptions]. A container exempts one entry from
// that cascade with [EntitySubscription.SelfOnly].
//
// This is the one subscription declaration shape in the system: the
// awareness registry persists the same struct as owner-keyed rows
// (loop-owned rows are compiled from the spec at persist time), so the
// spec, the registry, and the renderer speak a single vocabulary.
type EntitySubscription struct {
	// EntityID is the subscription target: a concrete Home Assistant
	// entity ("sensor.upstairs_temperature"), a glob
	// ("binary_sensor.*door*"), or an organizational target
	// ("area:office", "label:critical", "floor:upstairs") whose members
	// are re-resolved from the registry each render. The field name is
	// historical; the awareness renderer parses the kind.
	EntityID string `yaml:"entity_id" json:"entity_id"`

	// History is the list of look-back windows (in seconds) the
	// renderer should summarize each turn. Empty means "no history."
	History []int `yaml:"history,omitempty" json:"history,omitempty"`

	// Forecast is the Home Assistant forecast type ("daily", "hourly",
	// "twice_daily") for weather.* entities. Empty means no forecast.
	Forecast string `yaml:"forecast,omitempty" json:"forecast,omitempty"`

	// Include declares optional HA registry metadata to include when
	// rendering this subscription into model context. Empty keeps the
	// subscription state-only.
	Include *homeassistant.EntityMetadataIncludes `yaml:"include,omitempty" json:"include,omitempty"`

	// TTLSeconds is the auto-expire window. Zero means never expires.
	// Combined with AddedAt at render time to decide whether to drop.
	TTLSeconds int `yaml:"ttl_seconds,omitempty" json:"ttl_seconds,omitempty"`

	// AddedAt is when the subscription first landed on the spec. Every
	// write-side helper (thane_loop_create creation, watch_entity,
	// add_entity_subscription with an owner) stamps a real timestamp;
	// the field exists to make TTL countdown meaningful. Hand-authored
	// Specs that leave it zero will not expire — [IsExpired] treats
	// zero as "never set, never ages."
	AddedAt time.Time `yaml:"added_at,omitempty" json:"added_at,omitempty"`

	// Mode declares what the subscription feeds. Empty (the canonical
	// form of "render") injects live state into context each turn;
	// [SubscriptionModeIngest] feeds the recent-state-changes window's
	// push pipeline without a per-turn render; [SubscriptionModeBoth]
	// does both. Normalized on load by [normalizeSubscriptionsOnLoad].
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`

	// SelfOnly exempts this entry from container inheritance: a
	// container's SelfOnly subscription renders for the container
	// itself but is not unioned into descendants' effective sets. On a
	// non-container loop the flag is inert (nothing inherits from
	// non-containers).
	SelfOnly bool `yaml:"self_only,omitempty" json:"self_only,omitempty"`

	// RequiresTag gates rendering on a capability tag: when set, the
	// subscription renders only while that tag is active in the
	// consuming context (see [EntitySubscription.GateOpen]). Empty
	// means unconditional — today's default. The gate is render-only:
	// combining it with an ingest-feeding mode is rejected at every
	// authoring door and at JSON hydration, and the ingestion filter
	// ignores gated rows as a backstop, so capture never depends on
	// tag state (#1213). A gated subscription MAY carry a transition
	// log: capture for the log runs unconditionally (so the log is
	// warm when the tag activates) while its rendering follows the
	// gate.
	RequiresTag string `yaml:"requires_tag,omitempty" json:"requires_tag,omitempty"`

	// Transitions asks the render to include the entity's last n
	// observed state changes ({from, to, ago} in the class-aware
	// vocabulary). Zero means no transition log. Declaring a log
	// derives capture: the subscription's target joins the state
	// watcher's ingestion filter automatically (#1210) — no
	// user-facing mode required. Clamped to the per-subscription
	// render cap; truncation is advertised.
	Transitions int `yaml:"transitions,omitempty" json:"transitions,omitempty"`

	// TransitionsWindowSeconds bounds the transition log to changes
	// observed within the trailing window. Zero means no window
	// bound — the per-entity retention's count bound is the only
	// limit. May combine with Transitions (window-filtered last-n);
	// set alone it renders every retained change inside the window,
	// still clamped to the render cap.
	TransitionsWindowSeconds int `yaml:"transitions_window_seconds,omitempty" json:"transitions_window_seconds,omitempty"`

	// Wake declares the wake feed (#1211): the OWNING loop is
	// awakened when the watched entity changes — debounced,
	// coalesced per entity, and delivered through the shared
	// loopqueue chassis, never a private wake driver. Requires a
	// loop owner (always-visible and system rows have nobody to
	// wake) and, like every capture-dependent option, derives the
	// entity into the ingestion filter and refuses registry targets
	// and requires_tag (wake must not follow tag state).
	Wake bool `yaml:"wake,omitempty" json:"wake,omitempty"`

	// WakeDebounceSeconds is this subscription's ask for how long
	// changes coalesce before its loop wakes. Zero uses the shared
	// default. A loop's effective wake cadence follows its twitchiest
	// wake subscription — one wake drains everything pending — so a
	// slower ask here bounds only how fast THIS subscription's
	// changes demand a wake.
	WakeDebounceSeconds int `yaml:"wake_debounce_seconds,omitempty" json:"wake_debounce_seconds,omitempty"`
}

// IsExpired reports whether this subscription's TTL has elapsed
// relative to now. Zero TTL means never expires.
func (s EntitySubscription) IsExpired(now time.Time) bool {
	if s.TTLSeconds <= 0 {
		return false
	}
	if s.AddedAt.IsZero() {
		return false
	}
	return now.After(s.AddedAt.Add(time.Duration(s.TTLSeconds) * time.Second))
}

func cloneEntitySubscriptions(src []EntitySubscription) []EntitySubscription {
	if len(src) == 0 {
		return nil
	}
	out := make([]EntitySubscription, len(src))
	for i, sub := range src {
		out[i] = sub.Clone()
	}
	return out
}

// Clone returns a deep copy of the subscription (History and Include
// are the only reference-typed fields).
func (s EntitySubscription) Clone() EntitySubscription {
	out := s
	if len(s.History) > 0 {
		out.History = append([]int(nil), s.History...)
	}
	if s.Include != nil {
		include := *s.Include
		out.Include = &include
	}
	return out
}

// EffectiveOriginSelf is the [EffectiveSubscription.From] /
// [EffectiveTag.From] value used for entries the loop declared
// directly. A constant prevents callers from accidentally comparing
// against a freshly-typed string literal in user-facing surfaces.
const EffectiveOriginSelf = "self"

// Subscription modes: what a subscription feeds. The empty string is
// the canonical stored form of "render" so pre-mode declarations and
// new default declarations serialize identically; the named constant
// exists for tool parameters and display.
const (
	// SubscriptionModeRender is the default: inject live state into
	// model context each turn.
	SubscriptionModeRender = "render"
	// SubscriptionModeIngest feeds the recent-state-changes window's
	// push pipeline only; no per-turn state render.
	SubscriptionModeIngest = "ingest"
	// SubscriptionModeBoth feeds the state-change window and renders
	// live state each turn.
	SubscriptionModeBoth = "both"
)

// NormalizeSubscriptionMode returns the canonical stored mode: empty
// and "render" collapse to "" (render is the absent-field default),
// ingest/both pass through, anything else is an actionable error.
func NormalizeSubscriptionMode(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case "", SubscriptionModeRender:
		return "", nil
	case SubscriptionModeIngest:
		return SubscriptionModeIngest, nil
	case SubscriptionModeBoth:
		return SubscriptionModeBoth, nil
	default:
		return "", fmt.Errorf("mode must be one of [render, ingest, both], got %q", raw)
	}
}

// FeedsIngest reports whether this subscription feeds the
// state-change window's push pipeline (mode ingest or both).
func (s EntitySubscription) FeedsIngest() bool {
	return s.Mode == SubscriptionModeIngest || s.Mode == SubscriptionModeBoth
}

// MaxSubscriptionTransitions caps how many transitions one
// subscription may ask to render per turn (#1210). Authoring
// boundaries reject a larger ask with a teaching error; the awareness
// render clamps to it defensively for declarations arriving by other
// routes. Lives here because the cap is a property of the declaration,
// shared by every door that parses one.
const MaxSubscriptionTransitions = 20

// WantsTransitions reports whether this subscription declared a
// transition log (last-n and/or windowed). A true result derives
// capture: the target must reach the state watcher's ingestion
// filter for the log to have anything to render (#1210).
func (s EntitySubscription) WantsTransitions() bool {
	return s.Transitions > 0 || s.TransitionsWindowSeconds > 0
}

// RendersState reports whether this subscription renders live state
// into context each turn (mode render — stored as "" — or both).
func (s EntitySubscription) RendersState() bool {
	return s.Mode != SubscriptionModeIngest
}

// GateOpen reports whether the subscription's RequiresTag gate admits
// rendering for a context with the given active capability tags. An
// ungated subscription is always admitted; a gated one only while its
// tag is active. Callers pass the assembly request's ActiveTags — a
// nil map simply means no tags are active, closing every gate.
func (s EntitySubscription) GateOpen(activeTags map[string]bool) bool {
	return s.RequiresTag == "" || activeTags[s.RequiresTag]
}

// NormalizeSubscriptionForecast returns the canonical forecast
// value for persisted subscriptions. "none" and empty collapse to
// "" (meaning "no forecast fetch"); the three real HA forecast
// types pass through unchanged; anything else is an actionable
// error. Lives in the loop package because the forecast string is
// a property of [EntitySubscription], and centralizing it here
// lets [Spec.UnmarshalJSON] guard hydration without depending on
// the tools or awareness packages.
//
// Tool-boundary callers (thane_loop_create, watch_entity) and the
// awareness watchlist store have their own normalizers that match
// this contract; consolidation is a follow-up.
func NormalizeSubscriptionForecast(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	switch v {
	case "", "none":
		return "", nil
	case "daily", "hourly", "twice_daily":
		return v, nil
	default:
		return "", fmt.Errorf("forecast must be one of [daily, hourly, twice_daily, none], got %q", raw)
	}
}

// normalizeSubscriptionsOnLoad sweeps a freshly-hydrated
// subscription list and applies the boundary invariants the
// write-side tool handlers enforce: forecast values are
// canonicalized (or rejected), and TTL-bearing entries that lack
// an AddedAt stamp get one. The latter closes the documented
// footgun where `ttl_seconds > 0 && AddedAt.IsZero()` causes
// [EntitySubscription.IsExpired] to return false forever —
// hand-edited YAML or externally-pushed specs would otherwise
// produce "immortal" watchers that ignore their declared TTL.
//
// now is threaded through so callers (notably tests) can pin a
// clock value. The default real-world callsite is
// [Spec.UnmarshalJSON] which passes time.Now().
func normalizeSubscriptionsOnLoad(subs []EntitySubscription, now time.Time) ([]EntitySubscription, error) {
	if len(subs) == 0 {
		return subs, nil
	}
	out := make([]EntitySubscription, len(subs))
	for i, sub := range subs {
		forecast, err := NormalizeSubscriptionForecast(sub.Forecast)
		if err != nil {
			return nil, fmt.Errorf("subscriptions[%d] (entity_id=%q): %w", i, sub.EntityID, err)
		}
		sub.Forecast = forecast
		mode, err := NormalizeSubscriptionMode(sub.Mode)
		if err != nil {
			return nil, fmt.Errorf("subscriptions[%d] (entity_id=%q): %w", i, sub.EntityID, err)
		}
		sub.Mode = mode
		sub.RequiresTag = strings.TrimSpace(sub.RequiresTag)
		// The gate is render-only (#1213): rejecting the combination
		// here makes the invariant uniform across every JSON-hydrated
		// spec (loop_definition_set, persisted records), matching the
		// tool boundaries; the awareness IngestGlobs skip remains the
		// backstop for rows that arrive by other routes.
		if sub.RequiresTag != "" && sub.FeedsIngest() {
			return nil, fmt.Errorf("subscriptions[%d] (entity_id=%q): requires_tag gates rendering only and cannot combine with mode %q — drop requires_tag, or use mode render", i, sub.EntityID, sub.Mode)
		}
		if sub.Transitions < 0 {
			return nil, fmt.Errorf("subscriptions[%d] (entity_id=%q): transitions must be >= 0, got %d", i, sub.EntityID, sub.Transitions)
		}
		if sub.TransitionsWindowSeconds < 0 {
			return nil, fmt.Errorf("subscriptions[%d] (entity_id=%q): transitions_window_seconds must be >= 0, got %d", i, sub.EntityID, sub.TransitionsWindowSeconds)
		}
		if sub.WakeDebounceSeconds < 0 {
			return nil, fmt.Errorf("subscriptions[%d] (entity_id=%q): wake_debounce_seconds must be >= 0, got %d", i, sub.EntityID, sub.WakeDebounceSeconds)
		}
		// The wake feed must not follow tag state (#1213's boundary
		// extended to #1211: capture-adjacent behavior stays
		// unconditional) and cannot ride registry targets (their
		// members never reach the ingestion filter).
		if sub.Wake && sub.RequiresTag != "" {
			return nil, fmt.Errorf("subscriptions[%d] (entity_id=%q): wake cannot combine with requires_tag — waking must not follow tag state; drop one of the two", i, sub.EntityID)
		}
		if sub.Wake && homeassistant.IsRegistryTarget(sub.EntityID) {
			return nil, fmt.Errorf("subscriptions[%d] (entity_id=%q): wake needs the entity's event stream, and area/label/floor targets cannot feed the ingestion filter — use a concrete entity or glob", i, sub.EntityID)
		}
		// The transition log is a render feature; an ingest-only mode
		// renders nothing, so the combination declares a log nobody
		// would ever see. Same uniform-hydration posture as the
		// requires_tag combos.
		if sub.WantsTransitions() && sub.Mode == SubscriptionModeIngest {
			return nil, fmt.Errorf("subscriptions[%d] (entity_id=%q): a transition log renders into context, but mode ingest never renders — drop the transition options, or use mode render or both", i, sub.EntityID)
		}
		// Registry targets (area:/label:/floor:) never reach the
		// ingestion filter, so capture-dependent options on them would
		// silently do nothing. Enforced here so loop_definition_set
		// and persisted records obey the same rule as the tool doors.
		if homeassistant.IsRegistryTarget(sub.EntityID) {
			if sub.WantsTransitions() {
				return nil, fmt.Errorf("subscriptions[%d] (entity_id=%q): a transition log needs the entity's event stream, and area/label/floor targets cannot feed the ingestion filter — use a concrete entity or glob", i, sub.EntityID)
			}
			if sub.FeedsIngest() {
				return nil, fmt.Errorf("subscriptions[%d] (entity_id=%q): mode %q accepts entity ids and globs only — area/label/floor targets cannot feed the ingestion filter; use mode render", i, sub.EntityID, sub.Mode)
			}
		}
		if sub.TTLSeconds > 0 && sub.AddedAt.IsZero() {
			sub.AddedAt = now
		}
		out[i] = sub
	}
	return out, nil
}

// EffectiveSubscription is an entity subscription annotated with its
// origin in the loop graph. Embeds [EntitySubscription] so JSON
// encoding stays flat — every field appears alongside `from`.
// Returned by [Registry.EffectiveSubscriptions] for surfaces that
// need to render effective state with provenance
// (loop_definition_get, loop_status).
type EffectiveSubscription struct {
	EntitySubscription

	// From is [EffectiveOriginSelf] when this loop declared the
	// subscription, or the ancestor loop's name when it was
	// inherited. Operators editing a loop's subscriptions read this
	// to see which entries are local vs. inherited from a container.
	From string `yaml:"from" json:"from"`
}

// EffectiveTag is a capability tag annotated with its origin in the
// loop graph. Mirror of [EffectiveSubscription] for tags. Returned
// by [Registry.EffectiveTags].
type EffectiveTag struct {
	// Tag is the capability tag name.
	Tag string `yaml:"tag" json:"tag"`

	// From follows the same contract as
	// [EffectiveSubscription.From]: [EffectiveOriginSelf] for
	// directly-declared tags, ancestor loop name otherwise.
	From string `yaml:"from" json:"from"`
}

// EffectiveExcludeTool is a tool-exclusion entry annotated with its
// origin in the loop graph. ExcludeTools cascades by union — every
// ancestor's excludes contribute and a child cannot un-exclude a
// container's restriction. This makes "no shell_exec in this
// subtree" a structural safety guarantee. Returned by
// [Registry.EffectiveExcludeTools].
type EffectiveExcludeTool struct {
	// Tool is the tool name that is excluded.
	Tool string `yaml:"tool" json:"tool"`
	// From follows the same provenance contract as [EffectiveTag.From].
	From string `yaml:"from" json:"from"`
}

// EffectiveRoutingFactor is one routing-factor entry annotated with
// its origin in the loop graph. RoutingFactors cascade with
// child-wins semantics on key collision — a descendant's value
// overrides the ancestor's. Returned by
// [Registry.EffectiveRoutingFactors].
type EffectiveRoutingFactor struct {
	// Key is the routing-factor name.
	Key string `yaml:"key" json:"key"`
	// Value is the routing-factor value at this level of the graph.
	Value string `yaml:"value" json:"value"`
	// From is [EffectiveOriginSelf] when this loop declared the
	// value, or the ancestor loop's name when it was inherited.
	From string `yaml:"from" json:"from"`
}

// EffectiveDelegationGating is the resolved delegation-gating
// directive plus its origin. DelegationGating cascades with
// closest-non-empty semantics — the loop's own value wins if set;
// otherwise the closest ancestor that declares a non-empty value
// wins; otherwise the result is "". Returned by
// [Registry.EffectiveDelegationGating] as a pointer so the absence
// of a declaration anywhere in the chain is distinguishable from
// the empty-string value (today empty means "no override" so the
// distinction doesn't load-bear, but it leaves room).
type EffectiveDelegationGating struct {
	// Value is the resolved gating string (e.g. "disabled").
	Value string `yaml:"value" json:"value"`
	// From follows the same provenance contract as [EffectiveTag.From].
	From string `yaml:"from" json:"from"`
}
