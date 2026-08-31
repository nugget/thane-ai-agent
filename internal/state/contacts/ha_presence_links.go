package contacts

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// OnIngestEntitiesChange registers the callback that mirrors the presence
// tracker's person entities and HA-linked device trackers into the ingestion
// registry. Registration immediately publishes the current set. The callback
// is invoked outside the tracker's lock.
func (t *PresenceTracker) OnIngestEntitiesChange(callback func([]string)) {
	t.mu.Lock()
	t.ingestObserver = callback
	entityIDs := t.ingestEntityIDsLocked()
	t.mu.Unlock()
	if callback != nil {
		callback(entityIDs)
	}
}

// OnLinkedTrackersChange registers the callback used to hydrate newly linked
// device trackers after their ingestion filter has expanded. Unlike
// [PresenceTracker.OnIngestEntitiesChange], registration does not publish an
// initial value; initialization already hydrates the complete linked set. The
// callback is invoked outside the tracker's lock.
func (t *PresenceTracker) OnLinkedTrackersChange(callback func([]string)) {
	t.mu.Lock()
	t.linkedObserver = callback
	t.mu.Unlock()
}

// IngestEntityIDs returns the tracked person entities followed by the
// deduplicated HA device trackers currently linked from those people.
func (t *PresenceTracker) IngestEntityIDs() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.ingestEntityIDsLocked()
}

// RefreshLinkedTrackers refreshes the provider registry and current states for
// an explicitly newly linked set. It closes the event-ordering gap where a
// stable tracker changed before the ingestion filter expanded and would not
// otherwise emit another state change.
func (t *PresenceTracker) RefreshLinkedTrackers(ctx context.Context, ha StateGetter, entityIDs []string) error {
	if len(entityIDs) == 0 {
		return nil
	}
	ha.InvalidateRegistryCache()
	entries, err := getEntityRegistryWithRetry(ctx, ha, entityRegistryRetryDelays[:])
	if err != nil {
		return fmt.Errorf("refresh HA entity registry for newly linked room providers: %w", err)
	}
	platforms := make(map[string]string, len(entries))
	for _, entry := range entries {
		platforms[entry.EntityID] = strings.ToLower(strings.TrimSpace(entry.Platform))
	}
	t.replaceTrackerPlatforms(platforms)
	t.pruneInvalidBermudaObservations()

	var firstErr error
	for _, result := range fetchPresenceStates(ctx, ha, t.filterLinkedTrackersForPlatform(entityIDs, BermudaRoomProvider)) {
		if result.err != nil {
			t.logger.Warn("failed to fetch newly linked Bermuda tracker state", "entity_id", result.entityID, "error", result.err)
			if firstErr == nil {
				firstErr = fmt.Errorf("fetch %s: %w", result.entityID, result.err)
			}
			continue
		}
		t.applyBermudaState(result.entityID, result.state)
	}
	return firstErr
}

func (t *PresenceTracker) replaceTrackerPlatforms(platforms map[string]string) {
	t.mu.Lock()
	t.trackerPlatform = platforms
	t.mu.Unlock()
}

func (t *PresenceTracker) linkedTrackersForPlatform(platform string) []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var result []string
	for entityID := range t.ownersByTracker {
		if t.trackerPlatform[entityID] == platform {
			result = append(result, entityID)
		}
	}
	sort.Strings(result)
	return result
}

func (t *PresenceTracker) filterLinkedTrackersForPlatform(entityIDs []string, platform string) []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	seen := make(map[string]bool, len(entityIDs))
	result := make([]string, 0, len(entityIDs))
	for _, entityID := range entityIDs {
		entityID = strings.TrimSpace(entityID)
		if entityID == "" || seen[entityID] || len(t.ownersByTracker[entityID]) == 0 || t.trackerPlatform[entityID] != platform {
			continue
		}
		seen[entityID] = true
		result = append(result, entityID)
	}
	sort.Strings(result)
	return result
}

func (t *PresenceTracker) linkedTrackerEntityIDs() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]string, 0, len(t.ownersByTracker))
	for entityID := range t.ownersByTracker {
		result = append(result, entityID)
	}
	sort.Strings(result)
	return result
}

func (t *PresenceTracker) rebuildTrackerOwnersLocked() {
	owners := make(map[string][]string)
	seenPeople := make(map[string]bool, len(t.order))
	for _, personID := range t.order {
		if seenPeople[personID] {
			continue
		}
		seenPeople[personID] = true
		for _, trackerID := range t.linkedByPerson[personID] {
			owners[trackerID] = append(owners[trackerID], personID)
		}
	}
	t.ownersByTracker = owners
}

func (t *PresenceTracker) ingestEntityIDsLocked() []string {
	result := make([]string, 0, len(t.order)+len(t.ownersByTracker))
	seen := make(map[string]bool, cap(result))
	for _, entityID := range t.order {
		if !seen[entityID] {
			result = append(result, entityID)
			seen[entityID] = true
		}
	}
	linked := make([]string, 0, len(t.ownersByTracker))
	for entityID := range t.ownersByTracker {
		if !seen[entityID] {
			linked = append(linked, entityID)
		}
	}
	sort.Strings(linked)
	return append(result, linked...)
}

func addedEntityIDs(previous, current []string) []string {
	seen := make(map[string]bool, len(previous))
	for _, entityID := range previous {
		seen[entityID] = true
	}
	var added []string
	for _, entityID := range current {
		if !seen[entityID] {
			added = append(added, entityID)
		}
	}
	return added
}
