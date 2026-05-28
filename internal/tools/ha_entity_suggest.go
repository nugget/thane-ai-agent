package tools

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

// maxEntitySuggestions caps the "did you mean?" candidate list returned by
// the HA not-found envelope. Small enough to stay compact in the model's
// context, large enough to cover a typo's plausible neighbors.
const maxEntitySuggestions = 8

// EntityLister is the slice of the Home Assistant client that
// SuggestEntityNotFound needs: enumerate entities (optionally
// domain-scoped) so it can rank "did you mean?" candidates. The concrete
// *homeassistant.Client satisfies it, and so does any awareness-side
// client that exposes entity discovery. Taking an interface lets tools in
// other packages reuse the shared not-found envelope.
type EntityLister interface {
	GetEntities(ctx context.Context, domain string) ([]homeassistant.EntityInfo, error)
}

// EntityNotFoundResult is the structured envelope the native Home
// Assistant tools return when a caller-supplied entity_id is not present
// in Home Assistant's live state machine. It mirrors the
// {found:false, reason, candidates} shape that ha_find_entity and
// ha_device already use, so a model can recover in one follow-up call
// instead of guessing another id.
type EntityNotFoundResult struct {
	Found             bool               `json:"found"` // always false
	Reason            string             `json:"reason"`
	RequestedEntityID string             `json:"requested_entity_id"`
	Candidates        []EntitySuggestion `json:"candidates,omitempty"`
	Note              string             `json:"note"`
}

// EntitySuggestion is one "did you mean?" candidate: the canonical
// entity_id needed for the follow-up call plus the friendly name that
// explains it.
type EntitySuggestion struct {
	EntityID     string  `json:"entity_id"`
	FriendlyName string  `json:"friendly_name,omitempty"`
	Score        float64 `json:"score"`
}

// ControlDeviceNoMatchResult is returned by ha_control_device when no
// device matches the description. It reports explicitly that nothing was
// changed and offers candidates (found by broadening past the inferred
// domain) so the model refines rather than assuming the device was
// controlled.
type ControlDeviceNoMatchResult struct {
	Acted                bool               `json:"acted"` // always false
	Reason               string             `json:"reason"`
	RequestedDescription string             `json:"requested_description"`
	Candidates           []EntitySuggestion `json:"candidates,omitempty"`
	Note                 string             `json:"note"`
}

// SuggestEntityNotFound builds the not-found envelope for a missing
// entity_id, fuzzy-matching it against the live entity set (scoped to the
// id's own domain when it carries one) so the result surfaces plausible
// corrections for a typo'd or stale id. It is the shared recovery path for
// the native HA tools that take an exact entity_id.
//
// It never returns an error: a discovery failure degrades to an empty
// candidate list with a note pointing at ha_find_entity, which is still
// strictly more useful to the model than a raw 404 or a silent no-op.
func SuggestEntityNotFound(ctx context.Context, ha EntityLister, requested string) string {
	domain := ""
	if i := strings.IndexByte(requested, '.'); i > 0 {
		domain = requested[:i]
	}

	entities, err := ha.GetEntities(ctx, domain)
	if (err != nil || len(entities) == 0) && domain != "" {
		// The domain itself may be the typo (e.g. lights.x vs light.x).
		// Fall back to the full set so we can still suggest neighbors.
		entities, err = ha.GetEntities(ctx, "")
	}

	var candidates []EntitySuggestion
	if err == nil {
		for i, m := range fuzzyMatchEntityInfos(requested, entities) {
			if i >= maxEntitySuggestions {
				break
			}
			candidates = append(candidates, EntitySuggestion{
				EntityID:     m.EntityID,
				FriendlyName: m.FriendlyName,
				Score:        m.Score,
			})
		}
	}

	note := "entity_id not found in Home Assistant. Pick a candidate, or use ha_find_entity (look up by description) or ha_control_device (find + act) instead of retrying a guessed entity_id."
	if len(candidates) == 0 {
		note = "entity_id not found in Home Assistant and nothing similar exists. Use ha_find_entity or ha_list_entities to discover a valid entity_id; do not retry with a guessed id."
	}

	return toJSON(EntityNotFoundResult{
		Found:             false,
		Reason:            "not_found",
		RequestedEntityID: requested,
		Candidates:        candidates,
		Note:              note,
	})
}

// IsHAEntityNotFound reports whether err indicates Home Assistant could
// not find the requested entity — an HTTP 404 from the states API, or a
// registry "not_found" sentinel. Tools use it to separate a bad/stale
// entity_id (recoverable via SuggestEntityNotFound) from an upstream
// failure that should surface as-is.
func IsHAEntityNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *homeassistant.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusNotFound
	}
	return isEntityRegistryNotFound(err)
}
