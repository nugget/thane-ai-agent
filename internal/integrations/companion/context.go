package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// ContextProvider injects companion reachability and durable observation
// metadata into the system prompt. Exact observation payloads remain behind
// explicit tools so sensitive values such as precise location do not become
// ambient prompt context.
type ContextProvider struct {
	registry *Registry
	store    *ObservationStore
}

// NewContextProvider creates a companion live-state context provider. The
// optional store preserves device identity and observation freshness while a
// mobile companion is suspended or offline.
func NewContextProvider(registry *Registry, stores ...*ObservationStore) *ContextProvider {
	p := &ContextProvider{registry: registry}
	if len(stores) > 0 {
		p.store = stores[0]
	}
	return p
}

// TagContextBucket places the companion view in live state: reachability and
// observation freshness change independently of the cached prompt prefix.
func (p *ContextProvider) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketLiveState
}

type companionContextJSON struct {
	Companions []companionDeviceJSON `json:"companions"`
}

type companionDeviceJSON struct {
	Account             string                     `json:"account"`
	ClientName          string                     `json:"client_name,omitempty"`
	ClientID            string                     `json:"client_id,omitempty"`
	Platform            string                     `json:"platform,omitempty"`
	AppVersion          string                     `json:"app_version,omitempty"`
	OSVersion           string                     `json:"os_version,omitempty"`
	Availability        string                     `json:"availability"`
	ConnectedAgo        string                     `json:"connected_ago,omitempty"`
	LastSeenAgo         string                     `json:"last_seen_ago,omitempty"`
	LastConnectedAgo    string                     `json:"last_connected_ago,omitempty"`
	LastDisconnectedAgo string                     `json:"last_disconnected_ago,omitempty"`
	LiveTools           []string                   `json:"live_tools,omitempty"`
	LatestObservations  []companionObservationJSON `json:"latest_observations,omitempty"`
}

type companionObservationJSON struct {
	Kind          string            `json:"kind"`
	Status        ObservationStatus `json:"status"`
	SchemaVersion int               `json:"schema_version"`
	ObservedAgo   string            `json:"observed_ago"`
	ReceivedAgo   string            `json:"received_ago"`
}

type companionDeviceKey struct {
	account        string
	deviceIdentity string
}

// TagContext returns the companion-device block for tag-gated injection.
// Implements [agent.TagContextProvider].
func (p *ContextProvider) TagContext(ctx context.Context, _ agentctx.ContextRequest) (string, error) {
	now := time.Now()
	devices := make(map[companionDeviceKey]*companionDeviceJSON)

	if p.store != nil {
		records, err := p.store.ListDevices(ctx)
		if err != nil {
			return "", fmt.Errorf("load durable companion context: %w", err)
		}
		for _, record := range records {
			key := companionDeviceKey{account: record.Account, deviceIdentity: record.DeviceIdentity}
			device := &companionDeviceJSON{
				Account:      record.Account,
				ClientName:   record.ClientName,
				ClientID:     record.ClientID,
				Platform:     record.Platform,
				AppVersion:   record.AppVersion,
				OSVersion:    record.OSVersion,
				Availability: "offline",
				LastSeenAgo:  promptfmt.FormatDeltaOnly(record.LastSeenAt, now),
			}
			if record.LastDisconnectedAt != nil {
				device.LastDisconnectedAgo = promptfmt.FormatDeltaOnly(*record.LastDisconnectedAt, now)
			}
			if record.LastConnectedAt != nil {
				device.LastConnectedAgo = promptfmt.FormatDeltaOnly(*record.LastConnectedAt, now)
			}
			devices[key] = device
		}

		observations, err := p.store.ListLatest(ctx)
		if err != nil {
			return "", fmt.Errorf("load companion observation context: %w", err)
		}
		for _, observation := range observations {
			key := companionDeviceKey{account: observation.Account, deviceIdentity: observation.DeviceIdentity}
			device := devices[key]
			if device == nil {
				device = &companionDeviceJSON{
					Account: observation.Account, ClientID: observation.ClientID,
					Availability: "offline",
				}
				devices[key] = device
			}
			device.LatestObservations = append(device.LatestObservations, companionObservationJSON{
				Kind:          observation.Kind,
				Status:        observation.Status,
				SchemaVersion: observation.SchemaVersion,
				ObservedAgo:   promptfmt.FormatDeltaOnly(observation.ObservedAt, now),
				ReceivedAgo:   promptfmt.FormatDeltaOnly(observation.ReceivedAt, now),
			})
		}
	}

	if p.registry != nil {
		for _, info := range p.registry.List() {
			deviceIdentity := info.DeviceIdentity
			if deviceIdentity == "" {
				deviceIdentity = info.ClientID
			}
			key := companionDeviceKey{account: info.Account, deviceIdentity: deviceIdentity}
			device := devices[key]
			if device == nil {
				device = &companionDeviceJSON{Account: info.Account, ClientID: info.ClientID}
				devices[key] = device
			}
			device.Availability = "online"
			device.ConnectedAgo = promptfmt.FormatDeltaOnly(info.ConnectedAt, now)
			preferNonempty(&device.ClientName, info.ClientName)
			preferNonempty(&device.Platform, info.Platform)
			preferNonempty(&device.AppVersion, info.AppVersion)
			preferNonempty(&device.OSVersion, info.OSVersion)
			for _, capability := range info.Capabilities {
				for _, definition := range capability.Tools {
					device.LiveTools = append(device.LiveTools, definition.Name)
				}
			}
			sort.Strings(device.LiveTools)
		}
	}

	companions := make([]companionDeviceJSON, 0, len(devices))
	for _, device := range devices {
		sort.Slice(device.LatestObservations, func(i, j int) bool {
			return device.LatestObservations[i].Kind < device.LatestObservations[j].Kind
		})
		companions = append(companions, *device)
	}
	sort.Slice(companions, func(i, j int) bool {
		if companions[i].Account != companions[j].Account {
			return companions[i].Account < companions[j].Account
		}
		return companions[i].ClientID < companions[j].ClientID
	})

	data, err := json.Marshal(companionContextJSON{Companions: companions})
	if err != nil {
		return "", fmt.Errorf("marshal companion context: %w", err)
	}
	return "### Companion Devices\n\n" + string(data) + "\n", nil
}

func preferNonempty(target *string, value string) {
	if value != "" {
		*target = value
	}
}
