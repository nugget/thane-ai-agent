package unifi

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
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

// Poller periodically queries a DeviceLocator and updates the person
// tracker with room-level presence. It requires two consecutive polls
// showing the same AP before updating a room (debounce), preventing
// transient WiFi roaming from causing room flicker.
type Poller struct {
	cfg PollerConfig

	mu      sync.Mutex
	pending map[string]*pendingRoom // entity_id → pending room change
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

// Start runs the polling loop until ctx is cancelled. It blocks.
func (p *Poller) Start(ctx context.Context) {
	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	// Poll immediately on start.
	p.poll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

func (p *Poller) poll(ctx context.Context) {
	locations, err := p.cfg.Locator.LocateDevices(ctx)
	if err != nil {
		p.cfg.Logger.Warn("unifi poll failed", "error", err)
		return
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

	for entityID, best := range entityBest {
		pend, exists := p.pending[entityID]
		if !exists || pend.room != best.room {
			// New candidate room — start debounce.
			p.pending[entityID] = &pendingRoom{
				room:   best.room,
				source: best.source,
				count:  1,
			}
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
			p.cfg.Logger.Log(ctx, slog.Level(-8), "room update committed", // config.LevelTrace
				"entity_id", entityID,
				"room", best.room,
				"source", best.source,
				"polls", pend.count,
			)
		}
	}

	// Clear pending entries for entities with no device seen this poll.
	for entityID := range p.pending {
		if _, hasBest := entityBest[entityID]; !hasBest {
			delete(p.pending, entityID)
		}
	}
}
