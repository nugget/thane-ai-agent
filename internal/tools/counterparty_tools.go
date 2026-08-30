package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/state/companions"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

// maxWhereaboutsSources caps the fused view's entry count; truncation
// is reported explicitly. Far above any real fleet.
const maxWhereaboutsSources = 16

// CounterpartyToolDeps are the join sources the fused counterparty
// view composes (#1450). An unconfigured optional source degrades to
// absence; a configured source that fails is reported to the caller.
type CounterpartyToolDeps struct {
	Contacts   *contacts.Store
	Companions *companions.Store
	// Presence returns the live tracker snapshot for an HA person
	// entity; nil when no tracker is configured.
	Presence func(entity string) (contacts.PersonSnapshot, bool)
	// AccountsForContact maps a canonical contact UUID string to the
	// companion accounts bound to it; nil when no bindings exist.
	AccountsForContact func(contactID string) []string
	// LiveIdentities reports which (account, client_id) pairs have an
	// open connection right now; nil when companions are unconfigured.
	LiveIdentities func() map[[2]string]bool
}

// EnableCounterpartyTools adds the fused counterparty view: one call
// that answers "what do we know about where this person is" from every
// source the contact record roots — HA person zone state, bermuda room
// while home, and companion location observations — ranked, with
// provenance and freshness per source.
func (r *Registry) EnableCounterpartyTools(deps CounterpartyToolDeps) {
	if deps.Contacts == nil {
		return
	}

	r.Register(&Tool{
		Name: "contact_whereabouts",
		Description: "Report everything known about where a contact is, fused from every source their " +
			"contact record roots and ranked best-first: room-level presence while they are home, their " +
			"freshest companion-device location when away, and zone-level person state as the floor. Each " +
			"source carries its own provenance and freshness — nothing is blended into a single guess. " +
			"Only the leading available device fix includes its location payload; every device entry " +
			"carries account, client_id, and device_id, so fetch any other device's payload with " +
			"companion_last_known_location. " +
			"This is the first door for \"where is <person>\" questions; reach for " +
			"companion_last_known_location only when you need one specific device's stored fix. " +
			"Pass name (resolved like other contact tools) or contact_id (UUID). A contact with no HA " +
			"person binding and no bound companion devices returns an error saying so — that is a " +
			"configuration gap, not an unknown location.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Contact name (formatted name or nickname; resolved with the standard contact cascade).",
				},
				"contact_id": map[string]any{
					"type":        "string",
					"description": "Exact contact UUID; takes precedence over name when both are given.",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			id, _ := args["contact_id"].(string)
			return handleContactWhereabouts(ctx, deps, strings.TrimSpace(name), strings.TrimSpace(id))
		},
	})
}

// whereaboutsSource is one provenance-carrying entry in the fused
// view. Entries are ordered best-first by the ranking the tool
// description promises; rank is the order, not a numeric field.
type whereaboutsSource struct {
	// Source identifies the provenance: "bermuda_room",
	// "ha_person_zone", or "companion_location".
	Source string `json:"source"`

	// Zone/room fields (presence sources).
	State     string `json:"state,omitempty"`
	Room      string `json:"room,omitempty"`
	RoomVia   string `json:"room_via,omitempty"`
	Since     string `json:"since,omitempty"`
	RoomSince string `json:"room_since,omitempty"`

	// Device fields (companion_location sources). Account, ClientID,
	// and DeviceID make each entry uniquely identifiable and let the
	// result chain into companion_last_known_location without guessing.
	Device       string          `json:"device,omitempty"`
	Account      string          `json:"account,omitempty"`
	ClientID     string          `json:"client_id,omitempty"`
	DeviceID     string          `json:"device_id,omitempty"`
	Platform     string          `json:"platform,omitempty"`
	Availability string          `json:"availability,omitempty"`
	Status       string          `json:"status,omitempty"`
	Freshness    string          `json:"freshness,omitempty"`
	ObservedAgo  string          `json:"observed_ago,omitempty"`
	ReceivedAgo  string          `json:"received_ago,omitempty"`
	Location     json.RawMessage `json:"location,omitempty"`
}

type whereaboutsResult struct {
	Contact   string `json:"contact"`
	ContactID string `json:"contact_id"`
	TrustZone string `json:"trust_zone"`
	// TruncatedDevices counts device entries dropped by the result
	// cap; zero (omitted) means every bound device is listed.
	TruncatedDevices int `json:"truncated_devices,omitempty"`
	// BestSource names the leading usable entry of Sources and Basis says
	// why it leads — precomputed so the model does not re-derive the
	// ranking. It is omitted when every source is withdrawn provenance.
	BestSource string              `json:"best_source,omitempty"`
	Basis      string              `json:"basis,omitempty"`
	Sources    []whereaboutsSource `json:"sources"`
}

func handleContactWhereabouts(ctx context.Context, deps CounterpartyToolDeps, name, id string) (string, error) {
	contact, err := resolveWhereaboutsContact(deps.Contacts, name, id)
	if err != nil {
		return "", err
	}

	now := time.Now().UTC()
	var presenceSources []whereaboutsSource
	// Presence is three-state: home, confirmed away (not_home or a
	// named zone), or unknown — the tracker seeds Unknown and HA can
	// report unavailable, and the basis must not claim "not home" on
	// either of those.
	home, presenceUnknown := false, true
	hasHABinding := false

	entity, hasEntity, err := deps.Contacts.HAPersonEntity(contact.ID)
	if err != nil {
		return "", fmt.Errorf("read HA person binding for contact %q: %w", contact.FormattedName, err)
	}
	if hasEntity && entity != "" {
		hasHABinding = true
		if deps.Presence != nil {
			if snap, tracked := deps.Presence(entity); tracked {
				state := strings.ToLower(strings.TrimSpace(snap.State))
				home = state == "home"
				presenceUnknown = state == "" || state == "unknown" || state == "unavailable"
				zone := whereaboutsSource{Source: "ha_person_zone", State: snap.State}
				if !snap.Since.IsZero() {
					zone.Since = promptfmt.FormatDeltaOnly(snap.Since, now)
				}
				if home && snap.Room != "" {
					room := whereaboutsSource{Source: "bermuda_room", Room: snap.Room, RoomVia: snap.RoomSource}
					if !snap.RoomSince.IsZero() {
						room.RoomSince = promptfmt.FormatDeltaOnly(snap.RoomSince, now)
					}
					presenceSources = append(presenceSources, room)
				}
				presenceSources = append(presenceSources, zone)
			}
		}
	}

	// Companion location observations, scoped at the query to this
	// contact's bound accounts. Available and withdrawn entries are
	// partitioned: withdrawn rows must trail even the zone floor.
	type timedSource struct {
		entry      whereaboutsSource
		observedAt time.Time
	}
	var availableDevices, withdrawnDevices []timedSource
	var boundAccounts []string
	if deps.AccountsForContact != nil {
		boundAccounts = deps.AccountsForContact(contact.ID.String())
	}
	if deps.Companions != nil && len(boundAccounts) > 0 {
		observations, err := deps.Companions.LatestObservationsByKind(ctx, "ios.location", boundAccounts)
		if err != nil {
			return "", fmt.Errorf("read companion observations: %w", err)
		}
		var live map[[2]string]bool
		if deps.LiveIdentities != nil {
			live = deps.LiveIdentities()
		}
		for _, obs := range observations {
			entry := whereaboutsSource{
				Source:      "companion_location",
				Account:     obs.Account,
				ClientID:    obs.ClientID,
				DeviceID:    obs.DeviceID,
				Status:      string(obs.Status),
				ObservedAgo: promptfmt.FormatDeltaOnly(obs.ObservedAt, now),
				ReceivedAgo: promptfmt.FormatDeltaOnly(obs.ReceivedAt, now),
			}
			device, ok, err := deps.Companions.Get(ctx, obs.Account, obs.ClientID)
			if err != nil {
				return "", fmt.Errorf("read companion device metadata for %s/%s: %w", obs.Account, obs.ClientID, err)
			}
			if ok {
				entry.Device = device.ClientName
				entry.Platform = device.Platform
			}
			entry.Availability = "offline"
			if live[[2]string{obs.Account, obs.ClientID}] {
				entry.Availability = "online"
			}
			if obs.Status == "available" {
				entry.Location = obs.Payload
				entry.Freshness = "fresh"
				if now.Sub(obs.ObservedAt) > companionLocationStaleAfter {
					entry.Freshness = "stale"
				}
				availableDevices = append(availableDevices, timedSource{entry: entry, observedAt: obs.ObservedAt})
			} else {
				withdrawnDevices = append(withdrawnDevices, timedSource{entry: entry, observedAt: obs.ObservedAt})
			}
		}
		sort.SliceStable(availableDevices, func(i, j int) bool {
			return availableDevices[i].observedAt.After(availableDevices[j].observedAt)
		})
		sort.SliceStable(withdrawnDevices, func(i, j int) bool {
			return withdrawnDevices[i].observedAt.After(withdrawnDevices[j].observedAt)
		})
	}

	// The configuration-gap error is reserved for the genuinely
	// unbound contact. A bound contact whose sources yielded nothing
	// gets a valid empty result whose basis says exactly which binding
	// produced nothing — bound-but-silent is not misconfigured.
	if !hasHABinding && len(boundAccounts) == 0 {
		return "", fmt.Errorf("contact %q has no HA person binding and no bound companion devices — there is no whereabouts source to consult; the operator binds a person entity via the X-THANE-HA-PERSON contact field and companion accounts via companion.providers.<account>.contact", contact.FormattedName)
	}

	result := whereaboutsResult{
		Contact:   contact.FormattedName,
		ContactID: contact.ID.String(),
		TrustZone: contact.TrustZone,
	}

	// Only the leading available fix carries its payload: the result
	// stays bounded across any fleet, and other payloads are one
	// companion_last_known_location call away by device identity.
	for i := range availableDevices {
		if i > 0 {
			availableDevices[i].entry.Location = nil
		}
	}
	available := make([]whereaboutsSource, 0, len(availableDevices))
	for _, ts := range availableDevices {
		available = append(available, ts.entry)
	}
	withdrawn := make([]whereaboutsSource, 0, len(withdrawnDevices))
	for _, ts := range withdrawnDevices {
		withdrawn = append(withdrawn, ts.entry)
	}

	// Presence is a semantic floor, not another fleet entry. Reserve its
	// slots before capping device sources so a large fleet cannot truncate
	// away the HA zone (or miscount that zone as a truncated device).
	totalDevices := len(available) + len(withdrawn)
	deviceBudget := maxWhereaboutsSources - len(presenceSources)
	if deviceBudget < 0 {
		deviceBudget = 0
	}
	if len(available) > deviceBudget {
		available = available[:deviceBudget]
		withdrawn = nil
	} else if remaining := deviceBudget - len(available); len(withdrawn) > remaining {
		withdrawn = withdrawn[:remaining]
	}
	result.TruncatedDevices = totalDevices - len(available) - len(withdrawn)

	// Ranking: room-level truth leads while home; the freshest device
	// fix leads while away; unknown presence never asserts either, so
	// the freshest fix leads with the unknown zone state listed after.
	// Withdrawn entries always trail the zone floor.
	switch {
	case home:
		result.Sources = append(result.Sources, presenceSources...)
		result.Sources = append(result.Sources, available...)
		result.Sources = append(result.Sources, withdrawn...)
		if len(presenceSources) > 0 && presenceSources[0].Source == "bermuda_room" {
			result.Basis = "home per person entity; room-level presence leads as the most specific source while home"
		} else {
			result.Basis = "home per person entity; zone state leads because no room-level presence is available"
		}
	case presenceUnknown && len(presenceSources) > 0:
		result.Sources = append(result.Sources, available...)
		result.Sources = append(result.Sources, presenceSources...)
		result.Sources = append(result.Sources, withdrawn...)
		if len(available) > 0 {
			result.Basis = "presence state is unknown; the freshest device location leads without asserting home or away"
		} else if len(withdrawn) > 0 {
			result.Basis = "presence state is unknown; zone state leads because no available device location exists; withdrawn device entries are not locations"
		} else {
			result.Basis = "no bound companion device has published a location; unknown presence state is the only whereabouts source"
		}
	case len(presenceSources) > 0:
		result.Sources = append(result.Sources, available...)
		result.Sources = append(result.Sources, presenceSources...)
		result.Sources = append(result.Sources, withdrawn...)
		if len(available) > 0 {
			result.Basis = "not home per person entity; the freshest device location leads and zone state is the floor"
		} else if len(withdrawn) > 0 {
			result.Basis = "not home per person entity; zone state leads because no available device location exists; withdrawn device entries are not locations"
		} else {
			result.Basis = "no bound companion device has published a location; person-entity zone state is the only whereabouts source"
		}
	default:
		result.Sources = append(result.Sources, available...)
		result.Sources = append(result.Sources, withdrawn...)
		if len(available) > 0 {
			result.Basis = "no presence tracking for this contact; device locations only"
		} else {
			result.Basis = "no available whereabouts source; bound devices have withdrawn location sharing, so their entries are provenance only"
		}
	}

	if len(result.Sources) == 0 {
		switch {
		case hasHABinding && len(boundAccounts) > 0:
			result.Basis = "bound, but silent: the person entity is not tracked (check person.track) and no bound device has published a location"
		case hasHABinding:
			result.Basis = "bound to a person entity that the presence tracker does not track (check person.track); no companion devices bound"
		default:
			result.Basis = "companion accounts are bound but no device has published a location yet"
		}
		out, err := json.Marshal(result)
		if err != nil {
			return "", fmt.Errorf("marshal whereabouts: %w", err)
		}
		return string(out), nil
	}

	if result.Sources[0].Status != "withdrawn" {
		result.BestSource = result.Sources[0].Source
	}

	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal whereabouts: %w", err)
	}
	return string(out), nil
}

func resolveWhereaboutsContact(store *contacts.Store, name, id string) (*contacts.Contact, error) {
	if id != "" {
		parsed, err := uuid.Parse(id)
		if err != nil {
			return nil, fmt.Errorf("contact_id %q is not a UUID; pass the id from contact context or use name instead", id)
		}
		c, err := store.Get(parsed)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("no contact with id %q; use name to resolve by display name", id)
		}
		if err != nil {
			return nil, fmt.Errorf("look up contact %q: %w", id, err)
		}
		return c, nil
	}
	if name == "" {
		return nil, errors.New("pass name or contact_id — whose whereabouts?")
	}
	c, err := store.ResolveContact(name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("no contact matched %q; try the exact formatted name or a nickname, or search with contact tools first", name)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve contact %q: %w", name, err)
	}
	return c, nil
}
