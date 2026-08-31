package contacts

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

// BermudaRoomProvider is the stable provider name used for room observations
// sourced from Home Assistant's Bermuda integration.
const BermudaRoomProvider = "bermuda"

const maxLinkedDeviceTrackersPerPerson = 64

var entityRegistryRetryDelays = [...]time.Duration{
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
}

// StateGetter abstracts the Home Assistant state and entity-registry reads the
// presence tracker needs. The registry platform is the authority for deciding
// whether a linked device tracker may contribute Bermuda room evidence.
type StateGetter interface {
	GetState(ctx context.Context, entityID string) (*homeassistant.State, error)
	GetEntityRegistry(ctx context.Context) ([]homeassistant.EntityRegistryEntry, error)
}

type presenceFetchResult struct {
	entityID string
	state    *homeassistant.State
	err      error
}

type roomWithdrawal struct {
	entityID    string
	observation RoomObservation
}

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

// IngestEntityIDs returns the tracked person entities followed by the
// deduplicated HA device trackers currently linked from those people.
func (t *PresenceTracker) IngestEntityIDs() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.ingestEntityIDsLocked()
}

// Initialize refreshes tracked people, their linked HA device trackers, and
// registry-verified Bermuda room observations. Person-state failures leave the
// affected person unchanged; registry or tracker failures retain previously
// known room evidence and are returned as a partial initialization error.
// Network I/O never holds the tracker lock.
func (t *PresenceTracker) Initialize(ctx context.Context, ha StateGetter) error {
	personResults := fetchPresenceStates(ctx, ha, t.EntityIDs())
	var firstErr error
	linked := make(map[string]bool)
	for _, entityID := range t.linkedTrackerEntityIDs() {
		linked[entityID] = true
	}
	for _, result := range personResults {
		if result.err != nil {
			t.logger.Warn("failed to fetch person state", "entity_id", result.entityID, "error", result.err)
			if firstErr == nil {
				firstErr = fmt.Errorf("fetch %s: %w", result.entityID, result.err)
			}
			continue
		}
		trackers, err := linkedDeviceTrackerIDs(result.state.Attributes)
		if err != nil {
			t.logger.Warn("person device tracker links are malformed; retaining previous links",
				"entity_id", result.entityID, "error", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("parse %s device trackers: %w", result.entityID, err)
			}
			continue
		}
		for _, entityID := range trackers {
			linked[entityID] = true
		}
	}

	if len(linked) > 0 {
		entries, err := getEntityRegistryWithRetry(ctx, ha, entityRegistryRetryDelays[:])
		if err != nil {
			t.logger.Warn("failed to identify linked HA device tracker platforms; retaining previous provider map", "error", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("fetch HA entity registry for room providers: %w", err)
			}
		} else {
			platforms := make(map[string]string, len(entries))
			for _, entry := range entries {
				platforms[entry.EntityID] = strings.ToLower(strings.TrimSpace(entry.Platform))
			}
			t.replaceTrackerPlatforms(platforms)
		}
	}

	for _, result := range personResults {
		if result.err == nil {
			t.applyPersonState(result.entityID, result.state, true)
		}
	}
	t.pruneInvalidBermudaObservations()

	bermudaIDs := t.linkedTrackersForPlatform(BermudaRoomProvider)
	for _, result := range fetchPresenceStates(ctx, ha, bermudaIDs) {
		if result.err != nil {
			t.logger.Warn("failed to fetch linked Bermuda tracker state", "entity_id", result.entityID, "error", result.err)
			if firstErr == nil {
				firstErr = fmt.Errorf("fetch %s: %w", result.entityID, result.err)
			}
			continue
		}
		t.applyBermudaState(result.entityID, result.state)
	}

	return firstErr
}

func getEntityRegistryWithRetry(ctx context.Context, ha StateGetter, delays []time.Duration) ([]homeassistant.EntityRegistryEntry, error) {
	for attempt := 0; ; attempt++ {
		entries, err := ha.GetEntityRegistry(ctx)
		if err == nil {
			return entries, nil
		}
		if attempt >= len(delays) {
			return nil, err
		}
		delay := delays[attempt]
		if delay <= 0 {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// HandleHAStateChange consumes the complete, filtered Home Assistant event.
// Person attribute-only updates refresh linked tracker ownership; linked
// registry-verified Bermuda trackers update or withdraw room observations.
func (t *PresenceTracker) HandleHAStateChange(change homeassistant.StateChangedData) {
	if change.NewState == nil {
		return
	}
	entityID := strings.TrimSpace(change.EntityID)
	if entityID == "" {
		entityID = strings.TrimSpace(change.NewState.EntityID)
	}
	if entityID == "" {
		return
	}

	t.mu.RLock()
	_, isPerson := t.people[entityID]
	t.mu.RUnlock()
	if isPerson {
		t.applyPersonState(entityID, change.NewState, true)
		return
	}
	t.applyBermudaState(entityID, change.NewState)
}

// HandleStateChange retains the compact compatibility surface used by direct
// callers and tests. Full HA watcher wiring uses [HandleHAStateChange] so
// attribute-only updates and source timestamps are preserved.
func (t *PresenceTracker) HandleStateChange(entityID, _, newState, _ string) {
	t.applyPersonState(entityID, &homeassistant.State{
		EntityID:    entityID,
		State:       newState,
		LastChanged: time.Now(),
	}, false)
}

func fetchPresenceStates(ctx context.Context, ha StateGetter, entityIDs []string) []presenceFetchResult {
	results := make([]presenceFetchResult, 0, len(entityIDs))
	for _, entityID := range entityIDs {
		state, err := ha.GetState(ctx, entityID)
		if err == nil && state == nil {
			err = fmt.Errorf("empty state response")
		}
		results = append(results, presenceFetchResult{entityID: entityID, state: state, err: err})
	}
	return results
}

func (t *PresenceTracker) applyPersonState(entityID string, state *homeassistant.State, updateLinks bool) {
	if state == nil {
		return
	}
	var linked []string
	if updateLinks {
		var err error
		linked, err = linkedDeviceTrackerIDs(state.Attributes)
		if err != nil {
			t.logger.Warn("person device tracker links are malformed; retaining previous links",
				"entity_id", entityID, "error", err)
			updateLinks = false
		}
	}

	t.mu.Lock()
	person, ok := t.people[entityID]
	if !ok {
		t.mu.Unlock()
		return
	}
	oldState := person.State
	stateChanged := oldState != state.State
	person.State = state.State
	if stateChanged {
		person.Since = state.LastChanged
		if person.Since.IsZero() {
			person.Since = time.Now()
		}
	}
	if name, ok := state.Attributes["friendly_name"].(string); ok && strings.TrimSpace(name) != "" {
		person.FriendlyName = strings.TrimSpace(name)
	}
	friendlyName := person.FriendlyName

	ingestChanged := false
	if updateLinks && !slices.Equal(t.linkedByPerson[entityID], linked) {
		t.linkedByPerson[entityID] = linked
		t.rebuildTrackerOwnersLocked()
		ingestChanged = true
	}

	var withdrawn []RoomObservation
	if strings.EqualFold(state.State, "not_home") {
		withdrawn = sortedRoomObservations(person.roomObservations)
		clear(person.roomObservations)
		clearResolvedRoom(person, false)
	} else {
		withdrawn = t.prunePersonBermudaObservationsLocked(entityID, person)
	}
	observers := append([]RoomObserver(nil), t.observers...)
	ingestObserver := t.ingestObserver
	var ingestEntityIDs []string
	if ingestChanged {
		ingestEntityIDs = t.ingestEntityIDsLocked()
	}
	t.mu.Unlock()

	if stateChanged {
		t.logger.Debug("person state changed",
			"entity_id", entityID,
			"friendly_name", friendlyName,
			"old_state", oldState,
			"new_state", state.State,
		)
	}
	for _, observation := range withdrawn {
		t.notifyRoomObservers(observers, entityID, "", observation.Provider, observation.Source)
	}
	if ingestChanged && ingestObserver != nil {
		ingestObserver(ingestEntityIDs)
	}
}

func (t *PresenceTracker) applyBermudaState(entityID string, state *homeassistant.State) {
	if state == nil {
		return
	}
	t.mu.RLock()
	platform := t.trackerPlatform[entityID]
	owners := append([]string(nil), t.ownersByTracker[entityID]...)
	t.mu.RUnlock()
	if platform != BermudaRoomProvider || len(owners) == 0 {
		return
	}

	room, roomOK := stringAttribute(state.Attributes, "area")
	via, viaOK := stringAttribute(state.Attributes, "scanner")
	if !roomOK {
		t.logger.Warn("Bermuda tracker area has an unexpected type; withdrawing room observation",
			"entity_id", entityID,
		)
		room = ""
	}
	if !viaOK {
		t.logger.Warn("Bermuda tracker scanner has an unexpected type; retaining room without scanner evidence",
			"entity_id", entityID,
		)
		via = ""
	}
	room = strings.TrimSpace(room)
	if !strings.EqualFold(state.State, "home") {
		room = ""
	}
	observedAt := state.LastUpdated
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	for _, owner := range owners {
		if room == "" {
			t.WithdrawRoom(owner, BermudaRoomProvider, entityID)
			continue
		}
		t.ObserveRoom(owner, RoomObservation{
			Room:       room,
			Provider:   BermudaRoomProvider,
			Source:     entityID,
			Via:        via,
			ObservedAt: observedAt,
		})
	}
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

func (t *PresenceTracker) pruneInvalidBermudaObservations() {
	t.mu.Lock()
	var withdrawn []roomWithdrawal
	seen := make(map[string]bool, len(t.order))
	for _, entityID := range t.order {
		if seen[entityID] {
			continue
		}
		seen[entityID] = true
		person := t.people[entityID]
		for _, observation := range t.prunePersonBermudaObservationsLocked(entityID, person) {
			withdrawn = append(withdrawn, roomWithdrawal{entityID: entityID, observation: observation})
		}
	}
	observers := append([]RoomObserver(nil), t.observers...)
	t.mu.Unlock()
	for _, withdrawal := range withdrawn {
		t.notifyRoomObservers(observers, withdrawal.entityID, "", withdrawal.observation.Provider, withdrawal.observation.Source)
	}
}

func (t *PresenceTracker) prunePersonBermudaObservationsLocked(entityID string, person *Person) []RoomObservation {
	linked := make(map[string]bool, len(t.linkedByPerson[entityID]))
	for _, trackerID := range t.linkedByPerson[entityID] {
		linked[trackerID] = true
	}
	var withdrawn []RoomObservation
	for key, observation := range person.roomObservations {
		if key.provider != BermudaRoomProvider {
			continue
		}
		if linked[key.source] && t.trackerPlatform[key.source] == BermudaRoomProvider {
			continue
		}
		withdrawn = append(withdrawn, observation)
		delete(person.roomObservations, key)
	}
	if len(withdrawn) > 0 {
		sort.Slice(withdrawn, func(i, j int) bool {
			return withdrawn[i].Source < withdrawn[j].Source
		})
		resolvePersonRoom(person, time.Now())
	}
	return withdrawn
}

func linkedDeviceTrackerIDs(attributes map[string]any) ([]string, error) {
	raw, ok := attributes["device_trackers"]
	if !ok || raw == nil {
		return nil, nil
	}
	var values []any
	switch typed := raw.(type) {
	case []any:
		values = typed
	case []string:
		values = make([]any, len(typed))
		for i := range typed {
			values[i] = typed[i]
		}
	default:
		return nil, fmt.Errorf("device_trackers is %T, want an array", raw)
	}
	if len(values) > maxLinkedDeviceTrackersPerPerson {
		return nil, fmt.Errorf("device_trackers has %d entries, limit is %d", len(values), maxLinkedDeviceTrackersPerPerson)
	}
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for i, value := range values {
		entityID, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("device_trackers[%d] is %T, want a string", i, value)
		}
		entityID = strings.TrimSpace(entityID)
		if !strings.HasPrefix(entityID, "device_tracker.") || seen[entityID] {
			continue
		}
		seen[entityID] = true
		result = append(result, entityID)
	}
	sort.Strings(result)
	return result, nil
}

func stringAttribute(attributes map[string]any, key string) (string, bool) {
	value, ok := attributes[key]
	if !ok || value == nil {
		return "", true
	}
	text, ok := value.(string)
	return strings.TrimSpace(text), ok
}
