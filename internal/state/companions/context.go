package companions

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// maxContextDevices caps the rendered device list. Far above any real
// household fleet; truncation is reported explicitly rather than
// silently dropping devices.
const maxContextDevices = 32

// ContactBinding is the resolved counterparty attribution for a
// companion account: whose devices these are, and the trust zone that
// authority derives from (#1450). Resolution happens at read time —
// the binding lives in operator-custodied config and the contact
// record, never copied onto device rows, so a zone change propagates
// instantly and there is no second store of authority to drift.
type ContactBinding struct {
	ContactID string
	Name      string
	TrustZone string
}

// ContactResolver maps a companion account to its bound contact, when
// one is configured. Implementations must fail closed: an unknown,
// unbound, or deleted contact resolves to nothing, degrading the
// device to account-only attribution.
type ContactResolver func(ctx context.Context, account string) (ContactBinding, bool)

// ContextProvider renders the joined companion-device view: every
// paired device from the durable inventory merged with the live
// provider registry. A known companion, its current reachability, and
// its latest observations are different states with different
// lifetimes (#1437) — so an iPhone whose socket closed when it locked
// stays visible here as an offline device with honest freshness,
// instead of vanishing the way the connected-only view made it.
//
// Live callable tools are listed only for online devices. Latest
// observations are listed by kind and freshness only — payloads
// (precise location included) never ride ambient context; they are
// fetched through explicit tag-gated tools.
//
// It implements [agent.TagContextProvider] structurally and is
// registered on the companion capability tag.
type ContextProvider struct {
	store *Store
	// live returns the current provider-registry snapshot. Nil-safe so
	// the durable view degrades gracefully if wiring changes.
	live func() []companion.ProviderInfo
	// now is a clock seam for deterministic tests.
	now    func() time.Time
	logger *slog.Logger
	// contacts resolves account → counterparty attribution; nil means
	// the binding layer is not wired and rows stay account-only.
	contacts ContactResolver
}

// SetContactResolver wires counterparty attribution (#1450) into the
// device view.
func (p *ContextProvider) SetContactResolver(resolver ContactResolver) {
	p.contacts = resolver
}

// NewContextProvider creates the joined companion-device context
// provider. live is typically Registry.List.
func NewContextProvider(store *Store, live func() []companion.ProviderInfo, logger *slog.Logger) *ContextProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return &ContextProvider{store: store, live: live, now: time.Now, logger: logger}
}

// TagContextBucket places the device view in live state: it reflects
// current runtime connectivity and must not thrash the cached prompt
// prefix.
func (p *ContextProvider) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketLiveState
}

type companionDevicesJSON struct {
	Devices []deviceContextJSON `json:"devices"`
	// TruncatedDevices counts devices dropped by the render cap; zero
	// (omitted) means the list is complete.
	TruncatedDevices int `json:"truncated_devices,omitempty"`
	// InventoryError reports that the durable inventory could not be
	// read this turn: only live-connection rows are shown, and offline
	// devices are temporarily invisible rather than gone.
	InventoryError bool `json:"inventory_error,omitempty"`
}

type deviceContextJSON struct {
	Account    string `json:"account"`
	ClientName string `json:"client_name,omitempty"`
	// DeviceID is the server-assigned identity that survives credential
	// rotation (#1444) — the stable handle for referring to a device
	// across turns. ClientID is the device's current claim, used by
	// today's tool routing.
	DeviceID string `json:"device_id,omitempty"`
	ClientID string `json:"client_id,omitempty"`
	Platform string `json:"platform,omitempty"`

	// Contact is the bound counterparty's display name and
	// ContactTrustZone the trust zone device authority derives from;
	// absent when the account is unbound (#1450).
	Contact          string `json:"contact,omitempty"`
	ContactTrustZone string `json:"contact_trust_zone,omitempty"`

	// Availability is "online" (a live connection is open; Tools are
	// callable now) or "offline" (paired but not currently connected).
	Availability string `json:"availability"`

	// ConnectedAgo is how long the current live connection has been
	// open; present only while online.
	ConnectedAgo string `json:"connected_ago,omitempty"`
	// Tools are the live callable tools this device offers right now;
	// present only while online.
	Tools []string `json:"tools,omitempty"`

	LastSeenAgo         string `json:"last_seen_ago,omitempty"`
	LastConnectedAgo    string `json:"last_connected_ago,omitempty"`
	LastDisconnectedAgo string `json:"last_disconnected_ago,omitempty"`

	// Observations lists the latest stored observation per kind with
	// freshness only — never payload data.
	Observations []observationContextJSON `json:"observations,omitempty"`
}

type observationContextJSON struct {
	Kind string `json:"kind"`
	// Status is "available" or "withdrawn" (sharing was revoked; no
	// payload is retrievable).
	Status string `json:"status"`
	// ObservedAgo is device-claimed observation age; ReceivedAgo is
	// when the server received it. The two differ when a device drains
	// an old outbox.
	ObservedAgo string `json:"observed_ago"`
	ReceivedAgo string `json:"received_ago"`
}

// TagContext returns the joined companion-device block for tag-gated
// injection. Implements [agent.TagContextProvider].
func (p *ContextProvider) TagContext(ctx context.Context, _ agentctx.ContextRequest) (string, error) {
	if p.store == nil {
		return "", nil
	}
	now := p.now().UTC()

	// A store failure must not blank live truth: the registry rows come
	// from memory and can always render. Degrade to a live-only view
	// with an explicit inventory_error marker instead of dropping the
	// whole block (the error paths are where the failure lives).
	devices, listErr := p.store.List(ctx)
	var observations []companion.LatestObservation
	if listErr == nil {
		observations, listErr = p.store.ListLatestObservations(ctx)
	}
	inventoryFailed := listErr != nil
	if inventoryFailed {
		// The model sees inventory_error; the operator needs the cause.
		p.logger.Warn("companion device inventory read failed; rendering live-only view",
			"error", listErr,
		)
		devices, observations = nil, nil
	}
	obsByDevice := make(map[string][]observationContextJSON)
	for _, obs := range observations {
		obsByDevice[obs.DeviceID] = append(obsByDevice[obs.DeviceID], observationContextJSON{
			Kind:        obs.Kind,
			Status:      string(obs.Status),
			ObservedAgo: promptfmt.FormatDeltaOnly(obs.ObservedAt, now),
			ReceivedAgo: promptfmt.FormatDeltaOnly(obs.ReceivedAt, now),
		})
	}
	for _, list := range obsByDevice {
		sort.Slice(list, func(i, j int) bool { return list[i].Kind < list[j].Kind })
	}

	var liveInfos []companion.ProviderInfo
	if p.live != nil {
		liveInfos = p.live()
	}
	// Index live providers by durable identity. Providers without a
	// client_id have no durable row to join; they still render as
	// online devices below.
	liveByIdentity := make(map[[2]string][]companion.ProviderInfo)
	var liveUnjoined []companion.ProviderInfo
	for _, info := range liveInfos {
		if info.ClientID == "" {
			liveUnjoined = append(liveUnjoined, info)
			continue
		}
		key := [2]string{info.Account, info.ClientID}
		liveByIdentity[key] = append(liveByIdentity[key], info)
	}

	rows := make([]deviceContextJSON, 0, len(devices)+len(liveUnjoined))
	for _, d := range devices {
		if d.State != DeviceStateActive {
			continue
		}
		row := deviceContextJSON{
			Account:      d.Account,
			ClientName:   d.ClientName,
			DeviceID:     d.DeviceID,
			ClientID:     d.ClientID,
			Platform:     d.Platform,
			Availability: "offline",
			Observations: obsByDevice[d.DeviceID],
		}
		if binding, ok := p.resolveContact(ctx, d.Account); ok {
			row.Contact = binding.Name
			row.ContactTrustZone = binding.TrustZone
		}
		if !d.LastSeenAt.IsZero() {
			row.LastSeenAgo = promptfmt.FormatDeltaOnly(d.LastSeenAt, now)
		}
		if !d.LastConnectedAt.IsZero() {
			row.LastConnectedAgo = promptfmt.FormatDeltaOnly(d.LastConnectedAt, now)
		}
		if !d.LastDisconnectedAt.IsZero() {
			row.LastDisconnectedAgo = promptfmt.FormatDeltaOnly(d.LastDisconnectedAt, now)
		}

		key := [2]string{d.Account, d.ClientID}
		if matches := liveByIdentity[key]; len(matches) > 0 {
			delete(liveByIdentity, key)
			row.Availability = "online"
			row.Tools = liveToolNames(matches)
			row.ConnectedAgo = promptfmt.FormatDeltaOnly(earliestConnect(matches), now)
			if row.ClientName == "" {
				row.ClientName = matches[0].ClientName
			}
		}
		rows = append(rows, row)
	}

	// Live connections with no durable row yet — identities whose async
	// inventory write has not landed. Same-identity overlapping sockets
	// collapse to one row exactly as the joined path merges them: one
	// device, unioned tools, longest-open connection age.
	for _, matches := range liveByIdentity {
		first := matches[0]
		row := deviceContextJSON{
			Account:      first.Account,
			ClientName:   first.ClientName,
			ClientID:     first.ClientID,
			Platform:     first.Platform,
			Availability: "online",
			ConnectedAgo: promptfmt.FormatDeltaOnly(earliestConnect(matches), now),
			Tools:        liveToolNames(matches),
		}
		for _, m := range matches[1:] {
			if row.ClientName == "" {
				row.ClientName = m.ClientName
			}
			if row.Platform == "" {
				row.Platform = m.Platform
			}
		}
		if binding, ok := p.resolveContact(ctx, first.Account); ok {
			row.Contact = binding.Name
			row.ContactTrustZone = binding.TrustZone
		}
		rows = append(rows, row)
	}
	// Identity-less clients are each genuinely distinct — there is no
	// identity to merge on — so they render per connection.
	for _, info := range liveUnjoined {
		row := deviceContextJSON{
			Account:      info.Account,
			ClientName:   info.ClientName,
			Availability: "online",
			ConnectedAgo: promptfmt.FormatDeltaOnly(info.ConnectedAt, now),
			Tools:        liveToolNames([]companion.ProviderInfo{info}),
		}
		if binding, ok := p.resolveContact(ctx, info.Account); ok {
			row.Contact = binding.Name
			row.ContactTrustZone = binding.TrustZone
		}
		rows = append(rows, row)
	}

	// Deterministic order across turns so the model can compare without
	// relearning the shape.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Account != rows[j].Account {
			return rows[i].Account < rows[j].Account
		}
		if rows[i].ClientID != rows[j].ClientID {
			return rows[i].ClientID < rows[j].ClientID
		}
		return rows[i].ClientName < rows[j].ClientName
	})

	out := companionDevicesJSON{Devices: rows, InventoryError: inventoryFailed}
	if len(rows) > maxContextDevices {
		out.TruncatedDevices = len(rows) - maxContextDevices
		out.Devices = rows[:maxContextDevices]
	}

	data, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("marshal companion device context: %w", err)
	}
	return "### Companion Devices\n\n" +
		"Paired devices persist while offline; tools are callable only on online devices. " +
		"Observations are the latest report per kind — status \"withdrawn\" means sharing was revoked and that data is not retrievable. " +
		"Fetch a stored location with `companion_last_known_location`; other kinds have no reader yet.\n" +
		string(data) + "\n", nil
}

// earliestConnect returns the oldest ConnectedAt across overlapping
// connections — the device's longest continuous presence.
func earliestConnect(infos []companion.ProviderInfo) time.Time {
	earliest := infos[0].ConnectedAt
	for _, m := range infos[1:] {
		if m.ConnectedAt.Before(earliest) {
			earliest = m.ConnectedAt
		}
	}
	return earliest
}

// liveToolNames unions the tool names advertised across the given live
// providers, sorted and deduplicated.
func liveToolNames(infos []companion.ProviderInfo) []string {
	seen := make(map[string]bool)
	var names []string
	for _, info := range infos {
		for _, cap := range info.Capabilities {
			for _, def := range cap.Tools {
				if def.Name == "" || seen[def.Name] {
					continue
				}
				seen[def.Name] = true
				names = append(names, def.Name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// resolveContact applies the optional counterparty resolver.
func (p *ContextProvider) resolveContact(ctx context.Context, account string) (ContactBinding, bool) {
	if p.contacts == nil {
		return ContactBinding{}, false
	}
	return p.contacts(ctx, account)
}
