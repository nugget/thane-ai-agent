package companions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// seedObservation ingests one observation through the store's real
// ingestion path so the context view is tested against production
// storage shapes.
func seedObservation(t *testing.T, store *Store, account, clientID, kind string, status companion.ObservationStatus, observedAt, receivedAt time.Time) {
	t.Helper()
	device, ok, err := store.Get(ctx, account, clientID)
	if err != nil {
		t.Fatalf("get seeded device: %v", err)
	}
	if !ok {
		if err := store.RecordConnected(ctx, account, clientID, companion.DeviceMetadata{}, observedAt); err != nil {
			t.Fatalf("seed device: %v", err)
		}
		if device, ok, err = store.Get(ctx, account, clientID); err != nil || !ok {
			t.Fatalf("get seeded device: ok=%v err=%v", ok, err)
		}
	}
	event := companion.ObservationEvent{
		EventID:       "0d1f8a6e-4c2b-4b7e-9f00-3a7d0e2c9b41",
		Kind:          kind,
		SchemaVersion: 1,
		Status:        status,
		ObservedAt:    observedAt,
	}
	if status == companion.ObservationAvailable {
		event.Payload = json.RawMessage(`{"latitude":41.0,"longitude":-87.0}`)
	}
	_, err = store.IngestObservations(ctx, companion.ObservationPrincipal{Account: account, DeviceID: device.DeviceID},
		companion.ObservationBatch{
			ObservationDeviceMetadata: companion.ObservationDeviceMetadata{ClientID: clientID},
			Events:                    []companion.ObservationEvent{event},
		}, receivedAt)
	if err != nil {
		t.Fatalf("seed observation: %v", err)
	}
}

func decodeDeviceContext(t *testing.T, block string) companionDevicesJSON {
	t.Helper()
	start := strings.Index(block, "{")
	if start < 0 {
		t.Fatalf("no JSON in context block: %q", block)
	}
	var out companionDevicesJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(block[start:])), &out); err != nil {
		t.Fatalf("decode context JSON: %v\nblock: %s", err, block)
	}
	return out
}

func TestContextProviderJoinsDurableAndLive(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()

	// device-1: paired, currently offline, with one available and one
	// withdrawn observation.
	if err := store.RecordConnected(ctx, "alice", "device-1", companion.DeviceMetadata{ClientName: "Alice's iPhone", Platform: "ios"}, now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("connect device-1: %v", err)
	}
	seedObservation(t, store, "alice", "device-1", "ios.location", companion.ObservationAvailable, now.Add(-30*time.Minute), now.Add(-20*time.Minute))
	seedObservation(t, store, "alice", "device-1", "ios.system-context", companion.ObservationWithdrawn, now.Add(-15*time.Minute), now.Add(-10*time.Minute))
	if err := store.RecordDisconnected(ctx, "alice", "device-1", now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("disconnect device-1: %v", err)
	}

	// device-2: paired and currently online with live tools.
	if err := store.RecordConnected(ctx, "alice", "device-2", companion.DeviceMetadata{ClientName: "Alice's Mac", Platform: "macos"}, now.Add(-1*time.Hour)); err != nil {
		t.Fatalf("connect device-2: %v", err)
	}

	live := func() []companion.ProviderInfo {
		return []companion.ProviderInfo{
			{
				Account:     "alice",
				ClientID:    "device-2",
				ClientName:  "Alice's Mac",
				Platform:    "macos",
				ConnectedAt: now.Add(-1 * time.Hour),
				Capabilities: []companion.Capability{{
					Name:  "calendar",
					Tools: []companion.ToolDefinition{{Name: "macos_calendar_events", Method: "events"}},
				}},
			},
			// Identity-less legacy client: online, must not vanish.
			{
				Account:     "alice",
				ClientName:  "Legacy Mac",
				ConnectedAt: now.Add(-5 * time.Minute),
			},
		}
	}

	p := NewContextProvider(store, live, nil)
	if p.TagContextBucket() != agentctx.ContextBucketLiveState {
		t.Fatalf("bucket = %v, want live state", p.TagContextBucket())
	}
	block, err := p.TagContext(ctx, agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}

	if !strings.HasPrefix(block, "### Companion Devices") {
		t.Errorf("missing heading: %q", block[:60])
	}
	// Payloads must never ride ambient context.
	if strings.Contains(block, "latitude") || strings.Contains(block, "41.0") {
		t.Fatalf("observation payload leaked into context: %s", block)
	}

	out := decodeDeviceContext(t, block)
	if len(out.Devices) != 3 {
		t.Fatalf("got %d devices, want 3 (offline, online, identity-less): %s", len(out.Devices), block)
	}

	// Deterministic order: (account, client_id, client_name) — the
	// identity-less row has empty client_id and sorts first.
	legacy, d1, d2 := out.Devices[0], out.Devices[1], out.Devices[2]

	if legacy.ClientName != "Legacy Mac" || legacy.Availability != "online" || legacy.ClientID != "" {
		t.Errorf("identity-less row = %+v", legacy)
	}
	if legacy.LastSeenAgo != "" {
		t.Errorf("identity-less row carries durable freshness: %+v", legacy)
	}

	if d1.ClientID != "device-1" || d1.Availability != "offline" {
		t.Errorf("device-1 = %+v", d1)
	}
	if len(d1.Tools) != 0 || d1.ConnectedAgo != "" {
		t.Errorf("offline device claims live surface: %+v", d1)
	}
	if d1.LastSeenAgo == "" || d1.LastDisconnectedAgo == "" || d1.LastConnectedAgo == "" {
		t.Errorf("offline device missing freshness: %+v", d1)
	}
	if len(d1.Observations) != 2 {
		t.Fatalf("device-1 observations = %+v", d1.Observations)
	}
	if d1.Observations[0].Kind != "ios.location" || d1.Observations[0].Status != "available" {
		t.Errorf("observation[0] = %+v", d1.Observations[0])
	}
	if d1.Observations[1].Kind != "ios.system-context" || d1.Observations[1].Status != "withdrawn" {
		t.Errorf("observation[1] = %+v", d1.Observations[1])
	}
	if d1.Observations[0].ObservedAgo == "" || d1.Observations[0].ReceivedAgo == "" {
		t.Errorf("observation missing freshness: %+v", d1.Observations[0])
	}

	if d2.ClientID != "device-2" || d2.Availability != "online" {
		t.Errorf("device-2 = %+v", d2)
	}
	if len(d2.Tools) != 1 || d2.Tools[0] != "macos_calendar_events" {
		t.Errorf("device-2 tools = %v", d2.Tools)
	}
	if d2.ConnectedAgo == "" {
		t.Errorf("online device missing connected_ago: %+v", d2)
	}
}

// TestContextProviderOfflineOnly pins the point of the joined view: a
// device whose connection dropped remains fully visible, and zero live
// providers renders a normal (not empty, not erroring) block.
func TestContextProviderOfflineOnly(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	if err := store.RecordConnected(ctx, "alice", "device-1", companion.DeviceMetadata{ClientName: "Alice's iPhone"}, now.Add(-time.Hour)); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := store.RecordDisconnected(ctx, "alice", "device-1", now.Add(-30*time.Minute)); err != nil {
		t.Fatalf("disconnect: %v", err)
	}

	p := NewContextProvider(store, func() []companion.ProviderInfo { return nil }, nil)
	block, err := p.TagContext(ctx, agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	out := decodeDeviceContext(t, block)
	if len(out.Devices) != 1 || out.Devices[0].Availability != "offline" {
		t.Fatalf("offline device lost from context: %s", block)
	}
}

// TestContextProviderEmpty renders a stable empty schema when nothing
// is paired or connected.
func TestContextProviderEmpty(t *testing.T) {
	store := newTestStore(t)
	p := NewContextProvider(store, nil, nil)
	block, err := p.TagContext(ctx, agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if !strings.Contains(block, `"devices":[]`) {
		t.Errorf("empty state should render an explicit empty list: %q", block)
	}
}

// TestContextProviderUnrecordedLiveDevice pins the async-race path: a
// live provider whose inventory write has not landed yet must still
// render as an online device.
func TestContextProviderUnrecordedLiveDevice(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	p := NewContextProvider(store, func() []companion.ProviderInfo {
		return []companion.ProviderInfo{{
			Account: "alice", ClientID: "device-9", ClientName: "Fresh iPhone", ConnectedAt: now.Add(-time.Second),
		}}
	}, nil)
	out := decodeDeviceContext(t, mustContext(t, p))
	if len(out.Devices) != 1 {
		t.Fatalf("unrecorded live device lost: %+v", out)
	}
	d := out.Devices[0]
	if d.Availability != "online" || d.ClientID != "device-9" {
		t.Errorf("row = %+v", d)
	}
	if d.DeviceID != "" || d.LastSeenAgo != "" {
		t.Errorf("unrecorded device claims durable fields: %+v", d)
	}
}

// TestContextProviderOverlappingConnections pins the reconnect race —
// the package's canonical hazard: two live connections for one durable
// identity render as ONE row, with tools unioned and the connection
// age taken from the longest-open socket.
func TestContextProviderOverlappingConnections(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	if err := store.RecordConnected(ctx, "alice", "device-1", companion.DeviceMetadata{}, now.Add(-3*time.Hour)); err != nil {
		t.Fatalf("connect: %v", err)
	}
	old := now.Add(-3 * time.Hour)
	fresh := now.Add(-1 * time.Minute)
	p := NewContextProvider(store, func() []companion.ProviderInfo {
		return []companion.ProviderInfo{
			{Account: "alice", ClientID: "device-1", ConnectedAt: fresh,
				Capabilities: []companion.Capability{{Name: "c", Tools: []companion.ToolDefinition{{Name: "tool_from_new_socket", Method: "m"}}}}},
			{Account: "alice", ClientID: "device-1", ConnectedAt: old,
				Capabilities: []companion.Capability{{Name: "c", Tools: []companion.ToolDefinition{{Name: "tool_from_old_socket", Method: "m"}}}}},
		}
	}, nil)
	p.now = func() time.Time { return now }
	out := decodeDeviceContext(t, mustContext(t, p))
	if len(out.Devices) != 1 {
		t.Fatalf("overlapping connections rendered %d rows, want 1: %+v", len(out.Devices), out.Devices)
	}
	d := out.Devices[0]
	if len(d.Tools) != 2 || d.Tools[0] != "tool_from_new_socket" || d.Tools[1] != "tool_from_old_socket" {
		t.Errorf("tool union = %v", d.Tools)
	}
	if want := promptfmt.FormatDeltaOnly(old, now); d.ConnectedAgo != want {
		t.Errorf("ConnectedAgo = %q, want longest-open %q", d.ConnectedAgo, want)
	}
}

// TestContextProviderTruncation pins the cap, the count, and that
// truncation happens after the deterministic sort.
func TestContextProviderTruncation(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	p := NewContextProvider(store, func() []companion.ProviderInfo {
		infos := make([]companion.ProviderInfo, maxContextDevices+1)
		for i := range infos {
			infos[i] = companion.ProviderInfo{
				Account: "alice", ClientName: fmt.Sprintf("mac-%03d", i), ConnectedAt: now,
			}
		}
		return infos
	}, nil)
	out := decodeDeviceContext(t, mustContext(t, p))
	if len(out.Devices) != maxContextDevices {
		t.Fatalf("rendered %d devices, want cap %d", len(out.Devices), maxContextDevices)
	}
	if out.TruncatedDevices != 1 {
		t.Errorf("TruncatedDevices = %d, want 1", out.TruncatedDevices)
	}
	if out.Devices[0].ClientName != "mac-000" {
		t.Errorf("truncation happened before sort: first = %q", out.Devices[0].ClientName)
	}
}

// TestContextProviderDoesNotRenderStoredCapabilities pins the
// online-only tools invariant against its tempting future violation:
// the durable last-advertised manifest must never surface as callable
// tools for an offline device.
func TestContextProviderDoesNotRenderStoredCapabilities(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	if err := store.RecordConnected(ctx, "alice", "device-1", companion.DeviceMetadata{}, now.Add(-time.Hour)); err != nil {
		t.Fatalf("connect: %v", err)
	}
	manifest := []byte(`[{"name":"cap","tools":[{"name":"stored_manifest_tool"}]}]`)
	if err := store.RecordCapabilities(ctx, "alice", "device-1", manifest, now.Add(-time.Hour)); err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if err := store.RecordDisconnected(ctx, "alice", "device-1", now.Add(-30*time.Minute)); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	p := NewContextProvider(store, nil, nil)
	block := mustContext(t, p)
	if strings.Contains(block, "stored_manifest_tool") {
		t.Fatalf("stored capability manifest leaked into offline device context: %s", block)
	}
}

// TestContextProviderFreshnessSources pins each *_ago field to its own
// timestamp with a fixed clock, so a crossed wire between sources or a
// reversed delta cannot survive.
func TestContextProviderFreshnessSources(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 8, 30, 20, 0, 0, 0, time.UTC)
	connectAt := now.Add(-4 * time.Hour)
	observedAt := now.Add(-50 * time.Minute)
	receivedAt := now.Add(-40 * time.Minute)
	disconnectAt := now.Add(-90 * time.Minute)

	if err := store.RecordConnected(ctx, "alice", "device-1", companion.DeviceMetadata{}, connectAt); err != nil {
		t.Fatalf("connect: %v", err)
	}
	seedObservation(t, store, "alice", "device-1", "ios.location", companion.ObservationAvailable, observedAt, receivedAt)
	if err := store.RecordDisconnected(ctx, "alice", "device-1", disconnectAt); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	device, _, err := store.Get(ctx, "alice", "device-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	p := NewContextProvider(store, nil, nil)
	p.now = func() time.Time { return now }
	out := decodeDeviceContext(t, mustContext(t, p))
	d := out.Devices[0]

	if d.DeviceID != device.DeviceID {
		t.Errorf("device_id = %q, want stable handle %q", d.DeviceID, device.DeviceID)
	}
	if want := promptfmt.FormatDeltaOnly(connectAt, now); d.LastConnectedAgo != want {
		t.Errorf("LastConnectedAgo = %q, want %q", d.LastConnectedAgo, want)
	}
	if want := promptfmt.FormatDeltaOnly(disconnectAt, now); d.LastDisconnectedAgo != want {
		t.Errorf("LastDisconnectedAgo = %q, want %q", d.LastDisconnectedAgo, want)
	}
	// last_seen is the newest evidence of liveness: the observation
	// receipt (-40m) postdates the disconnect stamp (-90m), so the
	// MAX-guarded stamp holds the receipt time.
	if want := promptfmt.FormatDeltaOnly(receivedAt, now); d.LastSeenAgo != want {
		t.Errorf("LastSeenAgo = %q, want %q", d.LastSeenAgo, want)
	}
	obs := d.Observations[0]
	if want := promptfmt.FormatDeltaOnly(observedAt, now); obs.ObservedAgo != want {
		t.Errorf("ObservedAgo = %q, want %q", obs.ObservedAgo, want)
	}
	if want := promptfmt.FormatDeltaOnly(receivedAt, now); obs.ReceivedAgo != want {
		t.Errorf("ReceivedAgo = %q, want %q", obs.ReceivedAgo, want)
	}
}

// TestContextProviderFiltersInactiveDevices pins the model-facing half
// of the future forget/retire flow: non-active rows stay out of
// ambient context.
func TestContextProviderFiltersInactiveDevices(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	if err := store.RecordConnected(ctx, "alice", "device-1", companion.DeviceMetadata{ClientName: "Retired Phone"}, now.Add(-time.Hour)); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE companion_devices SET state = 'retired' WHERE client_id = 'device-1'`); err != nil {
		t.Fatalf("retire: %v", err)
	}
	p := NewContextProvider(store, nil, nil)
	out := decodeDeviceContext(t, mustContext(t, p))
	if len(out.Devices) != 0 {
		t.Fatalf("retired device rendered into ambient context: %+v", out.Devices)
	}
}

// TestContextProviderInventoryErrorFallsBackToLive pins the degraded
// mode: a failing store must not blank live truth, and the failure is
// declared rather than presented as "no devices paired".
func TestContextProviderInventoryErrorFallsBackToLive(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store, err := NewStore(db, nil)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	db.Close()

	now := time.Now().UTC()
	p := NewContextProvider(store, func() []companion.ProviderInfo {
		return []companion.ProviderInfo{{Account: "alice", ClientID: "device-1", ClientName: "Alice's Mac", ConnectedAt: now}}
	}, nil)
	block := mustContext(t, p)
	out := decodeDeviceContext(t, block)
	if !out.InventoryError {
		t.Errorf("inventory failure not declared: %s", block)
	}
	if len(out.Devices) != 1 || out.Devices[0].Availability != "online" {
		t.Fatalf("live truth blanked by store failure: %s", block)
	}
}

// mustContext renders the block or fails the test.
func mustContext(t *testing.T, p *ContextProvider) string {
	t.Helper()
	block, err := p.TagContext(ctx, agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	return block
}

// TestContextProviderContactAttribution pins the counterparty layer's
// first presentation surface (#1450): a bound account's devices carry
// the contact name and the trust zone authority derives from — on
// durable and live-only rows alike — and resolution fails closed to
// account-only attribution.
func TestContextProviderContactAttribution(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	if err := store.RecordConnected(ctx, "alice", "device-1", companion.DeviceMetadata{}, now.Add(-time.Hour)); err != nil {
		t.Fatalf("connect: %v", err)
	}

	p := NewContextProvider(store, func() []companion.ProviderInfo {
		return []companion.ProviderInfo{
			{Account: "alice", ClientID: "device-new", ConnectedAt: now}, // live, no durable row
			{Account: "bob", ClientID: "device-b", ConnectedAt: now},     // unbound account
		}
	}, nil)
	p.SetContactResolver(func(_ context.Context, account string) (ContactBinding, bool) {
		if account == "alice" {
			return ContactBinding{ContactID: "c-1", Name: "Alice Operator", TrustZone: "admin"}, true
		}
		return ContactBinding{}, false
	})

	out := decodeDeviceContext(t, mustContext(t, p))
	if len(out.Devices) != 3 {
		t.Fatalf("got %d devices, want 3", len(out.Devices))
	}
	for _, d := range out.Devices {
		switch d.Account {
		case "alice":
			if d.Contact != "Alice Operator" || d.ContactTrustZone != "admin" {
				t.Errorf("alice device %q attribution = %q/%q", d.ClientID, d.Contact, d.ContactTrustZone)
			}
		case "bob":
			if d.Contact != "" || d.ContactTrustZone != "" {
				t.Errorf("unbound account carries attribution: %+v", d)
			}
		}
	}
}

// TestContextProviderUnrecordedOverlapCollapses pins the merge rule on
// the not-yet-recorded path too: overlapping same-identity sockets with
// no durable row render as one device, exactly like the joined path.
func TestContextProviderUnrecordedOverlapCollapses(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	old := now.Add(-2 * time.Hour)
	p := NewContextProvider(store, func() []companion.ProviderInfo {
		return []companion.ProviderInfo{
			{Account: "alice", ClientID: "device-x", ConnectedAt: now,
				Capabilities: []companion.Capability{{Name: "c", Tools: []companion.ToolDefinition{{Name: "tool_new", Method: "m"}}}}},
			{Account: "alice", ClientID: "device-x", ClientName: "Alice's iPhone", ConnectedAt: old,
				Capabilities: []companion.Capability{{Name: "c", Tools: []companion.ToolDefinition{{Name: "tool_old", Method: "m"}}}}},
		}
	}, nil)
	p.now = func() time.Time { return now }
	out := decodeDeviceContext(t, mustContext(t, p))
	if len(out.Devices) != 1 {
		t.Fatalf("overlap rendered %d rows, want 1: %+v", len(out.Devices), out.Devices)
	}
	d := out.Devices[0]
	if len(d.Tools) != 2 || d.Tools[0] != "tool_new" || d.Tools[1] != "tool_old" {
		t.Errorf("tool union = %v", d.Tools)
	}
	if want := promptfmt.FormatDeltaOnly(old, now); d.ConnectedAgo != want {
		t.Errorf("ConnectedAgo = %q, want longest-open %q", d.ConnectedAgo, want)
	}
	if d.ClientName != "Alice's iPhone" {
		t.Errorf("first non-empty client name not adopted: %q", d.ClientName)
	}
}
