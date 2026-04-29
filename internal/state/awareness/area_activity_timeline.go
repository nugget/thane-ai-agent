package awareness

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// buildAreaTimeline fetches the logbook for the member entity_ids
// and projects a compact event stream. Filters out noisy numeric
// sensor transitions (a temperature reading drifting up and down is
// not an "event") while keeping discrete-state and alarm-class
// transitions. State strings flow through semanticState so the
// timeline vocabulary matches the bucketed entity output (a door
// reads "open"/"closed" in both places, never "on"/"off" in one and
// "open"/"closed" in the other). Newest first; truncated count is
// returned separately so the caller can mark it explicitly in the
// payload rather than dropping silently.
func buildAreaTimeline(
	ctx context.Context,
	client AreaActivityClient,
	members []areaMember,
	cutoff, now time.Time,
) ([]map[string]any, int, error) {
	if len(members) == 0 {
		return nil, 0, nil
	}
	entityIDs := make([]string, 0, len(members))
	classByEntity := make(map[string]string, len(members))
	for _, m := range members {
		entityIDs = append(entityIDs, m.entry.EntityID)
		classByEntity[m.entry.EntityID] = registryDeviceClass(m.entry, nil)
	}

	events, err := client.GetLogbookEvents(ctx, cutoff, now, entityIDs)
	if err != nil {
		return nil, 0, err
	}

	type tlEntry struct {
		when   time.Time
		entity string
		state  string
	}

	kept := make([]tlEntry, 0, len(events))
	for _, ev := range events {
		when := ev.WhenTime()
		if when.IsZero() || isSentinelState(ev.State) {
			continue
		}
		deviceClass := classByEntity[ev.EntityID]
		if !timelineEventInteresting(ev, deviceClass) {
			continue
		}
		domain := ev.Domain
		if domain == "" {
			domain = entityDomain(ev.EntityID)
		}
		kept = append(kept, tlEntry{
			when:   when,
			entity: ev.EntityID,
			state:  semanticState(domain, deviceClass, ev.State),
		})
	}

	// Newest first by actual timestamp — string comparison on delta
	// strings does not give chronological order.
	sort.SliceStable(kept, func(i, j int) bool {
		return kept[i].when.After(kept[j].when)
	})
	truncated := 0
	if len(kept) > areaActivityMaxTimelineEvents {
		truncated = len(kept) - areaActivityMaxTimelineEvents
		kept = kept[:areaActivityMaxTimelineEvents]
	}

	out := make([]map[string]any, 0, len(kept))
	for _, e := range kept {
		out = append(out, map[string]any{
			"t":      promptfmt.FormatDeltaOnly(e.when, now),
			"entity": e.entity,
			"state":  e.state,
		})
	}
	return out, truncated, nil
}

// timelineEventInteresting filters numeric-sensor noise. Discrete
// state transitions (light on/off, door open/closed, motion clear/
// detected) are always kept. Numeric sensor transitions are only
// kept for alarm-class device_classes.
func timelineEventInteresting(ev homeassistant.LogbookEntry, deviceClass string) bool {
	if ev.Domain != "sensor" {
		return true
	}
	if _, err := strconv.ParseFloat(ev.State, 64); err != nil {
		return true // non-numeric sensor transitions are interesting
	}
	return alarmSecurityClasses[deviceClass]
}
