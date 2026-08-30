package contacts

import (
	"sort"
	"strings"
	"time"
)

// RoomObservation is one provider-attributed claim about a tracked person's
// current room. Provider identifies the integration family, Source identifies
// the concrete device or sensor within that provider, and ObservedAt records
// when that source produced the claim.
type RoomObservation struct {
	Room       string    `json:"room"`
	Provider   string    `json:"provider"`
	Source     string    `json:"source,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

type roomObservationKey struct {
	provider string
	source   string
}

type roomResolutionLog struct {
	room             string
	provider         string
	source           string
	conflict         bool
	observationCount int
}

// OnRoomChange registers a callback that fires whenever one tracked room
// observation changes or is withdrawn. A withdrawal carries an empty room but
// retains provider and source so observers can clear only their own projection.
// Observers are called outside the tracker's lock and should be registered
// before room producers start.
func (t *PresenceTracker) OnRoomChange(fn RoomObserver) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.observers = append(t.observers, fn)
}

// ObserveRoom records one provider/source-keyed room observation. Multiple
// sources may coexist. Agreement resolves to one room; disagreement retains
// every observation but clears the resolved room and marks a conflict.
//
// An empty room withdraws this exact provider/source observation. A zero
// ObservedAt is replaced with the current time. Exact semantic refreshes update
// freshness without notifying observers or resetting RoomSince.
func (t *PresenceTracker) ObserveRoom(entityID string, observation RoomObservation) {
	now := time.Now()
	observation = normalizeRoomObservation(observation, now)
	if observation.Room == "" {
		t.WithdrawRoom(entityID, observation.Provider, observation.Source)
		return
	}

	key := roomObservationKey{provider: observation.Provider, source: observation.Source}

	t.mu.Lock()
	p, ok := t.people[entityID]
	if !ok {
		t.mu.Unlock()
		return
	}
	previous, existed := p.roomObservations[key]
	p.roomObservations[key] = observation
	resolvePersonRoom(p, now)
	friendlyName := p.FriendlyName
	resolution := roomResolutionForLog(p)
	observers := append([]RoomObserver(nil), t.observers...)
	t.mu.Unlock()

	if existed && normalizedRoomName(previous.Room) == normalizedRoomName(observation.Room) {
		return
	}
	t.logRoomObservation("observed", entityID, friendlyName, observation, resolution)
	t.notifyRoomObservers(observers, entityID, observation.Room, observation.Provider, observation.Source)
}

// WithdrawRoom removes one exact provider/source room observation. Other
// providers and sources remain available to the resolver. Missing observations
// and untracked entities are no-ops.
func (t *PresenceTracker) WithdrawRoom(entityID, provider, source string) {
	provider, source = normalizeRoomObservationIdentity(provider, source)
	key := roomObservationKey{provider: provider, source: source}

	t.mu.Lock()
	p, ok := t.people[entityID]
	if !ok {
		t.mu.Unlock()
		return
	}
	observation, exists := p.roomObservations[key]
	if !exists {
		t.mu.Unlock()
		return
	}
	delete(p.roomObservations, key)
	resolvePersonRoom(p, time.Now())
	friendlyName := p.FriendlyName
	resolution := roomResolutionForLog(p)
	observers := append([]RoomObserver(nil), t.observers...)
	t.mu.Unlock()

	t.logRoomObservation("withdrawn", entityID, friendlyName, observation, resolution)
	t.notifyRoomObservers(observers, entityID, "", provider, source)
}

// UpdateRoom is the compatibility path for providers, such as the current
// UniFi poller, that publish one current observation per person. A populated
// update replaces every prior observation from that provider atomically. An
// empty update with provider/source withdraws that exact observation; an empty
// update without identity clears every observation for legacy callers.
func (t *PresenceTracker) UpdateRoom(entityID, room, provider, source string) {
	room = strings.TrimSpace(room)
	provider, source = normalizeRoomObservationIdentity(provider, source)
	if room == "" {
		if provider != "" || source != "" {
			t.WithdrawRoom(entityID, provider, source)
			return
		}
		t.withdrawAllRooms(entityID)
		return
	}

	now := time.Now()
	observation := normalizeRoomObservation(RoomObservation{
		Room:       room,
		Provider:   provider,
		Source:     source,
		ObservedAt: now,
	}, now)
	key := roomObservationKey{provider: observation.Provider, source: observation.Source}

	t.mu.Lock()
	p, ok := t.people[entityID]
	if !ok {
		t.mu.Unlock()
		return
	}
	previous, existed := p.roomObservations[key]
	providerObservationCount := 0
	for existingKey := range p.roomObservations {
		if existingKey.provider != observation.Provider {
			continue
		}
		providerObservationCount++
		if existingKey != key {
			delete(p.roomObservations, existingKey)
		}
	}
	p.roomObservations[key] = observation
	resolvePersonRoom(p, now)
	friendlyName := p.FriendlyName
	resolution := roomResolutionForLog(p)
	observers := append([]RoomObserver(nil), t.observers...)
	t.mu.Unlock()

	if existed && providerObservationCount == 1 && normalizedRoomName(previous.Room) == normalizedRoomName(observation.Room) {
		return
	}
	t.logRoomObservation("replaced", entityID, friendlyName, observation, resolution)
	t.notifyRoomObservers(observers, entityID, observation.Room, observation.Provider, observation.Source)
}

func (t *PresenceTracker) withdrawAllRooms(entityID string) {
	t.mu.Lock()
	p, ok := t.people[entityID]
	if !ok || len(p.roomObservations) == 0 {
		t.mu.Unlock()
		return
	}
	withdrawn := sortedRoomObservations(p.roomObservations)
	clear(p.roomObservations)
	resolvePersonRoom(p, time.Now())
	friendlyName := p.FriendlyName
	resolution := roomResolutionForLog(p)
	observers := append([]RoomObserver(nil), t.observers...)
	t.mu.Unlock()

	t.logger.Debug("person room observations cleared",
		"entity_id", entityID,
		"friendly_name", friendlyName,
		"withdrawn_observations", len(withdrawn),
		"resolved_room", resolution.room,
		"room_conflict", resolution.conflict,
	)
	for _, observation := range withdrawn {
		t.notifyRoomObservers(observers, entityID, "", observation.Provider, observation.Source)
	}
}

func (t *PresenceTracker) logRoomObservation(operation, entityID, friendlyName string, observation RoomObservation, resolution roomResolutionLog) {
	t.logger.Debug("person room observation changed",
		"operation", operation,
		"entity_id", entityID,
		"friendly_name", friendlyName,
		"room", observation.Room,
		"room_provider", observation.Provider,
		"room_source", observation.Source,
		"observed_at", observation.ObservedAt,
		"resolved_room", resolution.room,
		"resolved_room_provider", resolution.provider,
		"resolved_room_source", resolution.source,
		"room_conflict", resolution.conflict,
		"room_observations", resolution.observationCount,
	)
}

func roomResolutionForLog(person *Person) roomResolutionLog {
	return roomResolutionLog{
		room:             person.Room,
		provider:         person.RoomProvider,
		source:           person.RoomSource,
		conflict:         person.roomConflict,
		observationCount: len(person.roomObservations),
	}
}

func (t *PresenceTracker) notifyRoomObservers(observers []RoomObserver, entityID, room, provider, source string) {
	for _, fn := range observers {
		fn(entityID, room, provider, source)
	}
}

func normalizeRoomObservation(observation RoomObservation, now time.Time) RoomObservation {
	observation.Room = strings.TrimSpace(observation.Room)
	observation.Provider, observation.Source = normalizeRoomObservationIdentity(observation.Provider, observation.Source)
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = now
	}
	return observation
}

func normalizeRoomObservationIdentity(provider, source string) (string, string) {
	return strings.ToLower(strings.TrimSpace(provider)), strings.TrimSpace(source)
}

func normalizedRoomName(room string) string {
	return strings.ToLower(strings.TrimSpace(room))
}

func resolvePersonRoom(person *Person, now time.Time) {
	observations := sortedRoomObservations(person.roomObservations)
	if len(observations) == 0 {
		clearResolvedRoom(person, false)
		return
	}

	roomKey := normalizedRoomName(observations[0].Room)
	for _, observation := range observations[1:] {
		if normalizedRoomName(observation.Room) != roomKey {
			clearResolvedRoom(person, true)
			return
		}
	}

	room := observations[0].Room
	provider := observations[0].Provider
	source := observations[0].Source
	for _, observation := range observations[1:] {
		if observation.Provider != provider {
			provider = ""
			source = ""
			continue
		}
		if observation.Source != source {
			source = ""
		}
	}

	if normalizedRoomName(person.Room) != roomKey {
		person.RoomSince = now
	}
	person.Room = room
	person.RoomProvider = provider
	person.RoomSource = source
	person.roomConflict = false
}

func clearResolvedRoom(person *Person, conflict bool) {
	person.Room = ""
	person.RoomSince = time.Time{}
	person.RoomProvider = ""
	person.RoomSource = ""
	person.roomConflict = conflict
}

func sortedRoomObservations(observations map[roomObservationKey]RoomObservation) []RoomObservation {
	result := make([]RoomObservation, 0, len(observations))
	for _, observation := range observations {
		result = append(result, observation)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Provider != result[j].Provider {
			return result[i].Provider < result[j].Provider
		}
		if result[i].Source != result[j].Source {
			return result[i].Source < result[j].Source
		}
		return result[i].Room < result[j].Room
	})
	return result
}
