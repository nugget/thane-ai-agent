package loop

import "time"

// EntitySubscription is one entity that a loop wants to see in context
// every iteration. It carries everything the awareness renderer needs to
// fetch current state plus optional history windows and forecast shape,
// without any indirection through a separate watchlist row.
//
// Subscriptions live directly on [Spec.Subscriptions]. A descendant
// loop's effective subscription list is the union of its own +
// every container ancestor's, deduplicated by EntityID with first-wins
// (own declarations take precedence over inherited ones) — see
// [Registry.AncestorSubscriptions].
//
// Lens tags belong on the subscription rather than on the loop. They
// describe properties of the subscription itself (visibility scope,
// future lens routing) and are not load-bearing for the loop→
// subscription binding.
type EntitySubscription struct {
	// EntityID is the Home Assistant entity identifier, e.g.
	// "sensor.upstairs_temperature" or "weather.home".
	EntityID string `yaml:"entity_id" json:"entity_id"`

	// History is the list of look-back windows (in seconds) the
	// renderer should summarize each turn. Empty means "no history."
	History []int `yaml:"history,omitempty" json:"history,omitempty"`

	// Forecast is the Home Assistant forecast type ("daily", "hourly",
	// "twice_daily") for weather.* entities. Empty means no forecast.
	Forecast string `yaml:"forecast,omitempty" json:"forecast,omitempty"`

	// TTLSeconds is the auto-expire window. Zero means never expires.
	// Combined with AddedAt at render time to decide whether to drop.
	TTLSeconds int `yaml:"ttl_seconds,omitempty" json:"ttl_seconds,omitempty"`

	// AddedAt is when the subscription first landed on the spec. Every
	// write-side helper (thane_curate creation, watch_entity,
	// update_entity_subscriptions add, the legacy-rows migration)
	// stamps a real timestamp; the field exists to make TTL countdown
	// meaningful. Hand-authored Specs that leave it zero will not
	// expire — [IsExpired] treats zero as "never set, never ages."
	AddedAt time.Time `yaml:"added_at,omitempty" json:"added_at,omitempty"`

	// Tags carry lens-style classifiers (visibility, lens routing,
	// future filtering). They are NOT used as a binding handle from
	// loop to subscription — the binding is structural.
	Tags []string `yaml:"tags,omitempty" json:"tags,omitempty"`
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
		out[i] = sub
		if len(sub.History) > 0 {
			out[i].History = append([]int(nil), sub.History...)
		}
		if len(sub.Tags) > 0 {
			out[i].Tags = append([]string(nil), sub.Tags...)
		}
	}
	return out
}

// EffectiveOriginSelf is the [EffectiveSubscription.From] /
// [EffectiveTag.From] value used for entries the loop declared
// directly. A constant prevents callers from accidentally comparing
// against a freshly-typed string literal in user-facing surfaces.
const EffectiveOriginSelf = "self"

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
