package tools

import (
	"context"
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

// CounterpartyToolDeps are the join sources the fused counterparty
// view composes (#1450). Every optional source degrades to absence.
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

	// Device fields (companion_location sources).
	Device       string          `json:"device,omitempty"`
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
	// BestSource names the leading entry of Sources and Basis says why
	// it leads — precomputed so the model does not re-derive the
	// ranking.
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
	var presenceSources, deviceSources []whereaboutsSource
	home := false

	// HA person presence, through the contact's binding.
	if deps.Presence != nil {
		if entity, ok, err := deps.Contacts.HAPersonEntity(contact.ID); err == nil && ok && entity != "" {
			if snap, tracked := deps.Presence(entity); tracked {
				home = strings.EqualFold(snap.State, "home")
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

	// Companion location observations from every bound device.
	type timedSource struct {
		entry      whereaboutsSource
		observedAt time.Time
		available  bool
	}
	var timedDevices []timedSource
	if deps.Companions != nil && deps.AccountsForContact != nil {
		owned := make(map[string]bool)
		for _, acct := range deps.AccountsForContact(contact.ID.String()) {
			owned[acct] = true
		}
		if len(owned) > 0 {
			observations, err := deps.Companions.ListLatestObservations(ctx)
			if err != nil {
				return "", fmt.Errorf("read companion observations: %w", err)
			}
			var live map[[2]string]bool
			if deps.LiveIdentities != nil {
				live = deps.LiveIdentities()
			}
			for _, obs := range observations {
				if !owned[obs.Account] || obs.Kind != "ios.location" {
					continue
				}
				entry := whereaboutsSource{
					Source:      "companion_location",
					Status:      string(obs.Status),
					ObservedAgo: promptfmt.FormatDeltaOnly(obs.ObservedAt, now),
					ReceivedAgo: promptfmt.FormatDeltaOnly(obs.ReceivedAt, now),
				}
				if device, ok, err := deps.Companions.Get(ctx, obs.Account, obs.ClientID); err == nil && ok {
					entry.Device = device.ClientName
					entry.Platform = device.Platform
				}
				entry.Availability = "offline"
				if live[[2]string{obs.Account, obs.ClientID}] {
					entry.Availability = "online"
				}
				available := obs.Status == "available"
				if available {
					entry.Location = obs.Payload
					entry.Freshness = "fresh"
					if now.Sub(obs.ObservedAt) > companionLocationStaleAfter {
						entry.Freshness = "stale"
					}
				}
				timedDevices = append(timedDevices, timedSource{entry: entry, observedAt: obs.ObservedAt, available: available})
			}
			// Freshest observation first; withdrawn entries sink.
			sort.SliceStable(timedDevices, func(i, j int) bool {
				if timedDevices[i].available != timedDevices[j].available {
					return timedDevices[i].available
				}
				return timedDevices[i].observedAt.After(timedDevices[j].observedAt)
			})
			for _, ts := range timedDevices {
				deviceSources = append(deviceSources, ts.entry)
			}
		}
	}

	if len(presenceSources) == 0 && len(deviceSources) == 0 {
		return "", fmt.Errorf("contact %q has no HA person binding and no bound companion devices — there is no whereabouts source to consult; the operator binds a person entity via the X-THANE-HA-PERSON contact field and companion accounts via companion.providers.<account>.contact", contact.FormattedName)
	}

	// Ranking: while home, room-level BLE is the most specific truth
	// and presence leads; away, the freshest device fix beats the zone
	// floor.
	result := whereaboutsResult{
		Contact:   contact.FormattedName,
		ContactID: contact.ID.String(),
		TrustZone: contact.TrustZone,
	}
	if home {
		result.Sources = append(presenceSources, deviceSources...)
		result.Basis = "home per person entity; room-level presence is the most specific source while home"
	} else {
		result.Sources = append(deviceSources, presenceSources...)
		result.Basis = "not home per person entity; the freshest device location leads and zone state is the floor"
	}
	if len(presenceSources) == 0 {
		result.Basis = "no presence tracking for this contact; device locations only"
	}
	if len(deviceSources) == 0 {
		result.Basis = "no bound companion devices; presence only"
	}
	result.BestSource = result.Sources[0].Source

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
		if err != nil {
			return nil, fmt.Errorf("no contact with id %q; use name to resolve by display name", id)
		}
		return c, nil
	}
	if name == "" {
		return nil, errors.New("pass name or contact_id — whose whereabouts?")
	}
	c, err := store.ResolveContact(name)
	if err != nil {
		return nil, fmt.Errorf("no contact matched %q; try the exact formatted name or a nickname, or search with contact tools first", name)
	}
	return c, nil
}
