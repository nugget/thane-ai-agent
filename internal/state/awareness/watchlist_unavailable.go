package awareness

import (
	"encoding/json"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// enrichUnavailable augments an unavailable entity's JSON payload
// with diagnostic context the model needs to attribute the failure:
// the device that owns the entity, whether sibling entities on that
// device are still alive, the upstream gateway/hub if any, and the
// state of the integration that brought the entity into HA. Returns
// the input unchanged for available entities, when registries is
// nil, or when JSON unmarshal fails — every failure mode degrades
// silently to the already-correct unavailable payload.
func enrichUnavailable(base string, current *homeassistant.State, registries *renderRegistries) string {
	if current == nil || !isSentinelState(current.State) || registries == nil {
		return base
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(base), &payload); err != nil {
		return base
	}

	entities, err := registries.entities()
	if err != nil {
		return base
	}
	entry, ok := entities[current.EntityID]
	if !ok || entry == nil {
		return base
	}

	if entry.DeviceID != "" {
		applyDeviceContext(payload, entry, current.EntityID, registries)
	}
	if entry.Platform != "" {
		applyIntegrationContext(payload, entry.Platform, registries)
	}

	return promptfmt.MarshalCompact(payload)
}

// applyDeviceContext sets the device, device_alive, and gateway
// fields on the payload using device registry data and a fresh
// states pull. Each section degrades independently — one missing
// piece does not block the others.
func applyDeviceContext(payload map[string]any, entry *homeassistant.EntityRegistryEntry, entityID string, registries *renderRegistries) {
	devices, err := registries.devices()
	if err != nil {
		return
	}
	device, ok := devices[entry.DeviceID]
	if !ok || device == nil {
		return
	}

	if info := compactDeviceInfo(device); len(info) > 0 {
		payload["device"] = info
	}

	if alive, known := siblingsAliveOnDevice(entry.DeviceID, entityID, registries); known {
		payload["device_alive"] = alive
	}

	if device.ViaDeviceID != "" {
		if gateway := buildGatewayContext(device, devices, registries); gateway != nil {
			payload["gateway"] = gateway
		}
	}
}

// applyIntegrationContext sets the integration field on the payload
// when the integration's state is known. The entity registry's
// Platform field carries the integration domain (e.g. "zwave_js").
func applyIntegrationContext(payload map[string]any, platform string, registries *renderRegistries) {
	integrations, err := registries.integrations()
	if err != nil {
		return
	}
	entry, ok := integrations[platform]
	if !ok || entry == nil {
		return
	}
	info := map[string]any{
		"name":  platform,
		"state": entry.State,
	}
	if entry.Reason != "" {
		info["reason"] = entry.Reason
	}
	payload["integration"] = info
}

// compactDeviceInfo returns the model-relevant fields of a device row
// in a small object. Empty strings are omitted so a sparse device
// row does not leak empty keys into the payload.
func compactDeviceInfo(d *homeassistant.DeviceRegistryEntry) map[string]any {
	info := map[string]any{}
	name := d.NameByUser
	if name == "" {
		name = d.Name
	}
	if name != "" {
		info["name"] = name
	}
	if d.Manufacturer != "" {
		info["manufacturer"] = d.Manufacturer
	}
	if d.Model != "" {
		info["model"] = d.Model
	}
	if d.SWVersion != "" {
		info["sw_version"] = d.SWVersion
	}
	return info
}

// siblingsAliveOnDevice reports whether any sibling entity on the
// same device is currently in a non-sentinel state. The known return
// is false when sibling state cannot be determined (no siblings, or
// states fetch failed) — the caller should omit the field rather
// than report a false negative.
func siblingsAliveOnDevice(deviceID, currentEntityID string, registries *renderRegistries) (alive, known bool) {
	siblings := registries.siblingsByDevice(deviceID, currentEntityID)
	if len(siblings) == 0 {
		return false, false
	}
	states, err := registries.states()
	if err != nil {
		return false, false
	}
	sawSiblingState := false
	for _, s := range siblings {
		state, ok := states[s.EntityID]
		if !ok {
			continue
		}
		sawSiblingState = true
		if !isSentinelState(state.State) {
			return true, true
		}
	}
	if !sawSiblingState {
		return false, false
	}
	return false, true
}

// buildGatewayContext walks the via_device_id chain to the topmost
// device and reports its identity plus a derived "online" boolean
// based on whether any of its own entities are alive. When the
// gateway has no entities to check, online is omitted.
func buildGatewayContext(start *homeassistant.DeviceRegistryEntry, devices map[string]*homeassistant.DeviceRegistryEntry, registries *renderRegistries) map[string]any {
	visited := map[string]bool{start.ID: true}
	current := start
	for current.ViaDeviceID != "" {
		if visited[current.ViaDeviceID] {
			break // cycle protection
		}
		parent, ok := devices[current.ViaDeviceID]
		if !ok || parent == nil {
			break
		}
		visited[parent.ID] = true
		current = parent
	}
	if current.ID == start.ID {
		return nil
	}

	gateway := compactDeviceInfo(current)
	if len(gateway) == 0 {
		gateway = map[string]any{}
	}

	if states, err := registries.states(); err == nil {
		gatewayEntities := registries.entitiesByDevice[current.ID]
		if len(gatewayEntities) > 0 {
			anyAlive := false
			anyChecked := false
			for _, e := range gatewayEntities {
				state, ok := states[e.EntityID]
				if !ok {
					continue
				}
				anyChecked = true
				if !isSentinelState(state.State) {
					anyAlive = true
					break
				}
			}
			if anyChecked {
				gateway["online"] = anyAlive
			}
		}
	}

	if len(gateway) == 0 {
		return nil
	}
	return gateway
}
