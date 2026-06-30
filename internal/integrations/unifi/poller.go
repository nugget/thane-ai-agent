package unifi

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// RoomUpdater is the interface the person tracker must satisfy for the
// poller to push room updates. Keeps the unifi package decoupled from
// the person package.
type RoomUpdater interface {
	UpdateRoom(entityID, room, source string)
}

// PollerConfig configures the UniFi room presence poller.
type PollerConfig struct {
	// Locator provides device locations from the network controller.
	Locator DeviceLocator

	// Updater receives room updates for tracked persons.
	Updater RoomUpdater

	// PollInterval is how often to query for device locations.
	PollInterval time.Duration

	// DeviceOwners maps normalized (lowercase) MAC addresses to entity
	// IDs. Built from config at startup.
	DeviceOwners map[string]string // mac → entity_id

	// APRooms maps AP names to human-readable room names.
	APRooms map[string]string // ap_name → room

	// FailureThreshold is the number of CONSECUTIVE failed polls tolerated
	// before Poll surfaces the error to the loop (which raises the operator
	// alarm). A transient gateway hiccup — the UniFi controller 5xx-ing on the
	// station-list endpoint — is absorbed silently; only a sustained outage
	// across this many polls is treated as genuinely broken and worth alarming.
	// Defaults to defaultFailureThreshold when <= 0.
	FailureThreshold int

	// Logger for structured logging.
	Logger *slog.Logger
}

// pendingRoom tracks a candidate room change that hasn't been confirmed
// by a second consecutive poll yet (debounce).
type pendingRoom struct {
	room   string
	source string // AP name
	count  int    // consecutive polls with this room
}

// debounceThreshold is the number of consecutive polls required before
// a room change is committed. A value of 2 means the room must be seen
// on two successive polls before the tracker is updated, preventing
// transient WiFi roaming from causing room flicker.
const debounceThreshold = 2

// defaultFailureThreshold is the number of consecutive failed polls tolerated
// before a poll failure is surfaced to the loop as an alarm. At the 30s default
// poll interval that is ~1.5 minutes of sustained failure — long enough to ride
// out a flaky gateway's transient 5xx, short enough to flag a real outage.
const defaultFailureThreshold = 3

// Poller periodically queries a DeviceLocator and updates the person
// tracker with room-level presence. It requires two consecutive polls
// showing the same AP before updating a room (debounce), preventing
// transient WiFi roaming from causing room flicker.
type Poller struct {
	cfg PollerConfig

	mu                  sync.Mutex
	pending             map[string]*pendingRoom // entity_id → pending room change
	consecutiveFailures int                     // streak of failed polls; gates the alarm
}

// failureThreshold is the configured consecutive-failure budget, or the default.
func (p *Poller) failureThreshold() int {
	if p.cfg.FailureThreshold > 0 {
		return p.cfg.FailureThreshold
	}
	return defaultFailureThreshold
}

// NewPoller creates a UniFi room presence poller.
func NewPoller(cfg PollerConfig) *Poller {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Poller{
		cfg:     cfg,
		pending: make(map[string]*pendingRoom),
	}
}

// Poll executes a single poll cycle: query the controller for device
// locations, apply debounce, and push room updates to the tracker.
// Returns an error if the device locator fails; all other paths succeed.
func (p *Poller) Poll(ctx context.Context) error {
	summary := loop.IterationSummary(ctx)

	locations, err := p.cfg.Locator.LocateDevices(ctx)
	if err != nil {
		return p.tolerateFailure(err, summary)
	}

	// Build MAC → DeviceLocation index, keeping only tracked MACs.
	macIndex := make(map[string]DeviceLocation, len(p.cfg.DeviceOwners))
	for _, loc := range locations {
		normalizedMAC := strings.ToLower(loc.MAC)
		if _, tracked := p.cfg.DeviceOwners[normalizedMAC]; tracked {
			existing, exists := macIndex[normalizedMAC]
			// Keep the most recently seen entry if duplicate.
			if !exists || loc.LastSeen > existing.LastSeen {
				macIndex[normalizedMAC] = loc
			}
		}
	}

	// For each tracked entity, find the best device (most recently
	// seen) and resolve its room from the AP name.
	type entityRoom struct {
		room     string
		source   string
		lastSeen int64
	}
	entityBest := make(map[string]*entityRoom)

	for mac, entityID := range p.cfg.DeviceOwners {
		loc, found := macIndex[mac]
		if !found {
			continue
		}

		room, knownAP := p.cfg.APRooms[loc.APName]
		if !knownAP {
			continue
		}

		existing := entityBest[entityID]
		if existing == nil || loc.LastSeen > existing.lastSeen {
			entityBest[entityID] = &entityRoom{
				room:     room,
				source:   loc.APName,
				lastSeen: loc.LastSeen,
			}
		}
	}

	// Apply debounce and update rooms.
	p.mu.Lock()
	defer p.mu.Unlock()
	p.consecutiveFailures = 0 // a successful locate clears the alarm gate

	var roomsUpdated, pendingCount int
	for entityID, best := range entityBest {
		pend, exists := p.pending[entityID]
		if !exists || pend.room != best.room {
			// New candidate room — start debounce.
			p.pending[entityID] = &pendingRoom{
				room:   best.room,
				source: best.source,
				count:  1,
			}
			pendingCount++
			p.cfg.Logger.Debug("room change candidate",
				"entity_id", entityID,
				"candidate_room", best.room,
				"source", best.source,
			)
			continue
		}

		// Same candidate as last poll — increment.
		pend.count++
		if pend.count >= debounceThreshold {
			p.cfg.Updater.UpdateRoom(entityID, best.room, best.source)
			roomsUpdated++
			p.cfg.Logger.Log(ctx, slog.Level(-8), "room update committed", // config.LevelTrace
				"entity_id", entityID,
				"room", best.room,
				"source", best.source,
				"polls", pend.count,
			)
		} else {
			pendingCount++
		}
	}

	// Clear pending entries for entities with no device seen this poll.
	for entityID := range p.pending {
		if _, hasBest := entityBest[entityID]; !hasBest {
			delete(p.pending, entityID)
		}
	}

	// Report metrics to the loop dashboard.
	if summary != nil {
		summary["devices_located"] = len(macIndex)
		summary["rooms_updated"] = roomsUpdated
		summary["pending_changes"] = pendingCount
	}

	return nil
}

// tolerateFailure records a failed poll and decides whether to surface it. A
// single transient gateway error (the UniFi controller 5xx-ing on /stat/sta) is
// absorbed silently — Poll returns nil so the iteration succeeds and no operator
// alarm flaps — and the degradation is reported to the loop dashboard instead.
// Only once failures pile up to FailureThreshold consecutive polls, a genuine
// sustained outage, is the error returned to the loop. A later successful poll
// resets the streak (see Poll).
func (p *Poller) tolerateFailure(err error, summary map[string]any) error {
	p.mu.Lock()
	p.consecutiveFailures++
	n := p.consecutiveFailures
	p.mu.Unlock()

	threshold := p.failureThreshold()
	if n >= threshold {
		return fmt.Errorf("UniFi controller failing for %d consecutive polls: %w", n, err)
	}

	p.cfg.Logger.Warn("UniFi poll failed; tolerating as transient",
		"consecutive_failures", n,
		"threshold", threshold,
		"error", err,
	)
	if summary != nil {
		summary["poll_status"] = fmt.Sprintf("transient failure %d/%d", n, threshold)
		summary["consecutive_failures"] = n
	}
	return nil
}
