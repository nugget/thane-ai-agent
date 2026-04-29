package awareness

import (
	"context"
	"sync"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

// HARegistryClient is the slice of Home Assistant registry APIs the
// unavailability enrichment and area_activity tool need beyond
// per-entity state. Defined as an interface so concrete clients can
// be swapped in tests without dragging in a real WebSocket connection.
type HARegistryClient interface {
	GetEntityRegistry(ctx context.Context) ([]homeassistant.EntityRegistryEntry, error)
	GetDeviceRegistry(ctx context.Context) ([]homeassistant.DeviceRegistryEntry, error)
	GetStates(ctx context.Context) ([]homeassistant.State, error)
	GetConfigEntries(ctx context.Context) ([]homeassistant.ConfigEntry, error)
}

// renderRegistries lazily fetches Home Assistant registries within a
// single render call and shares the result across multiple entity
// renders in that turn. State persists only for the lifetime of one
// instance — there is no caching across calls. Each TagContext call
// gets a fresh instance, and the data is discarded when it returns.
//
// Callers ask for the data they need and the bundle fetches on first
// use only. An entity render that doesn't need any registry data
// triggers no calls at all.
type renderRegistries struct {
	ctx    context.Context
	client HARegistryClient

	entitiesOnce     sync.Once
	entitiesByID     map[string]*homeassistant.EntityRegistryEntry
	entitiesByDevice map[string][]*homeassistant.EntityRegistryEntry
	entitiesErr      error

	devicesOnce sync.Once
	devicesByID map[string]*homeassistant.DeviceRegistryEntry
	devicesErr  error

	statesOnce sync.Once
	statesByID map[string]*homeassistant.State
	statesErr  error

	integrationsOnce    sync.Once
	integrationByDomain map[string]*homeassistant.ConfigEntry
	integrationsErr     error
}

// newRenderRegistries returns a registries bundle. Returns nil when
// client is nil so callers without a registry client skip enrichment
// cleanly.
func newRenderRegistries(ctx context.Context, client HARegistryClient) *renderRegistries {
	if client == nil {
		return nil
	}
	return &renderRegistries{ctx: ctx, client: client}
}

func (r *renderRegistries) entities() (map[string]*homeassistant.EntityRegistryEntry, error) {
	r.entitiesOnce.Do(func() {
		entries, err := r.client.GetEntityRegistry(r.ctx)
		if err != nil {
			r.entitiesErr = err
			return
		}
		r.entitiesByID = make(map[string]*homeassistant.EntityRegistryEntry, len(entries))
		r.entitiesByDevice = make(map[string][]*homeassistant.EntityRegistryEntry)
		for i := range entries {
			e := &entries[i]
			r.entitiesByID[e.EntityID] = e
			if e.DeviceID != "" {
				r.entitiesByDevice[e.DeviceID] = append(r.entitiesByDevice[e.DeviceID], e)
			}
		}
	})
	return r.entitiesByID, r.entitiesErr
}

func (r *renderRegistries) devices() (map[string]*homeassistant.DeviceRegistryEntry, error) {
	r.devicesOnce.Do(func() {
		devices, err := r.client.GetDeviceRegistry(r.ctx)
		if err != nil {
			r.devicesErr = err
			return
		}
		r.devicesByID = make(map[string]*homeassistant.DeviceRegistryEntry, len(devices))
		for i := range devices {
			d := &devices[i]
			r.devicesByID[d.ID] = d
		}
	})
	return r.devicesByID, r.devicesErr
}

func (r *renderRegistries) states() (map[string]*homeassistant.State, error) {
	r.statesOnce.Do(func() {
		states, err := r.client.GetStates(r.ctx)
		if err != nil {
			r.statesErr = err
			return
		}
		r.statesByID = make(map[string]*homeassistant.State, len(states))
		for i := range states {
			s := &states[i]
			r.statesByID[s.EntityID] = s
		}
	})
	return r.statesByID, r.statesErr
}

func (r *renderRegistries) integrations() (map[string]*homeassistant.ConfigEntry, error) {
	r.integrationsOnce.Do(func() {
		entries, err := r.client.GetConfigEntries(r.ctx)
		if err != nil {
			r.integrationsErr = err
			return
		}
		// Multiple config entries can share a domain (e.g. two Hue
		// bridges). Surface the first non-loaded one if any exists,
		// since a single broken instance is usually the actionable
		// signal. If all entries for a domain are loaded, keep the
		// first as the representative.
		r.integrationByDomain = make(map[string]*homeassistant.ConfigEntry, len(entries))
		for i := range entries {
			e := &entries[i]
			existing, has := r.integrationByDomain[e.Domain]
			if !has || (existing.State == "loaded" && e.State != "loaded") {
				r.integrationByDomain[e.Domain] = e
			}
		}
	})
	return r.integrationByDomain, r.integrationsErr
}

// siblingsByDevice returns sibling entity registry entries for the
// given device, excluding the entity_id passed in. Reads-through to
// the entities map so the lazy fetch happens automatically.
func (r *renderRegistries) siblingsByDevice(deviceID, excludeEntityID string) []*homeassistant.EntityRegistryEntry {
	if _, err := r.entities(); err != nil {
		return nil
	}
	all := r.entitiesByDevice[deviceID]
	if len(all) == 0 {
		return nil
	}
	out := make([]*homeassistant.EntityRegistryEntry, 0, len(all))
	for _, e := range all {
		if e.EntityID == excludeEntityID {
			continue
		}
		out = append(out, e)
	}
	return out
}
