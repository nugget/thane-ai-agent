package companion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

func TestContextProvider_Bucket(t *testing.T) {
	p := NewContextProvider(NewRegistry(nil))
	if got := p.TagContextBucket(); got != agentctx.ContextBucketLiveState {
		t.Errorf("bucket: got %v, want live state", got)
	}
}

func TestContextProvider_Empty(t *testing.T) {
	p := NewContextProvider(NewRegistry(nil))
	out, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	want := "### Companion Devices\n\n{\"companions\":[]}\n"
	if out != want {
		t.Errorf("empty registry: got %q, want headed explicit empty array", out)
	}
}

func TestContextProvider_ListsConnected(t *testing.T) {
	r := NewRegistry(nil)
	p := &Provider{
		ID:          "p1",
		Account:     "aimee",
		ClientName:  "pocket",
		ClientID:    "uuid-a",
		Platform:    "macos",
		AppVersion:  "1.2.3",
		ConnectedAt: time.Now().Add(-5 * time.Minute),
		done:        make(chan struct{}),
	}
	r.Add(p)
	if err := r.RegisterCapabilities("p1", []Capability{{
		Name: "macos.contacts",
		Tools: []ToolDefinition{
			{Name: "macos_search_contacts", Method: "search_contacts"},
		},
	}}); err != nil {
		t.Fatalf("RegisterCapabilities: %v", err)
	}

	out, err := NewContextProvider(r).TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}

	payload := strings.TrimSpace(strings.TrimPrefix(out, "### Companion Devices\n\n"))
	var ctx companionContextJSON
	if err := json.Unmarshal([]byte(payload), &ctx); err != nil {
		t.Fatalf("decode context: %v (%s)", err, out)
	}
	if len(ctx.Companions) != 1 {
		t.Fatalf("companions: got %d, want 1", len(ctx.Companions))
	}
	c := ctx.Companions[0]
	if c.Account != "aimee" || c.ClientName != "pocket" {
		t.Errorf("identity: got account=%q client=%q", c.Account, c.ClientName)
	}
	if len(c.LiveTools) != 1 || c.LiveTools[0] != "macos_search_contacts" {
		t.Errorf("tools: got %v", c.LiveTools)
	}
	if c.ConnectedAgo == "" {
		t.Error("connected_ago should be populated with a delta")
	}
	if c.Availability != "online" || c.Platform != "macos" || c.AppVersion != "1.2.3" {
		t.Errorf("live metadata: %+v", c)
	}
}

func TestContextProvider_ListsOfflineDeviceWithoutSensitivePayload(t *testing.T) {
	store := newTestObservationStore(t)
	at := time.Now().Add(-10 * time.Minute)
	batch := ObservationBatch{
		DeviceMetadata: DeviceMetadata{ClientID: "iphone-1", ClientName: "Pocket", Platform: "ios"},
		Events: []ObservationEvent{{
			EventID: "11111111-1111-4111-8111-111111111111", Kind: "ios.location",
			SchemaVersion: 1, Status: ObservationAvailable, ObservedAt: at,
			Payload: json.RawMessage(`{"latitude":41.0,"longitude":-87.0}`),
		}},
	}
	if _, err := store.Ingest(context.Background(), "nugget", batch, at.Add(time.Minute)); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	out, err := NewContextProvider(NewRegistry(nil), store).TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if strings.Contains(out, "latitude") || strings.Contains(out, "longitude") {
		t.Fatalf("context exposed observation payload: %s", out)
	}
	payload := strings.TrimSpace(strings.TrimPrefix(out, "### Companion Devices\n\n"))
	var decoded companionContextJSON
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("decode context: %v", err)
	}
	if len(decoded.Companions) != 1 {
		t.Fatalf("companions: %+v", decoded.Companions)
	}
	device := decoded.Companions[0]
	if device.Availability != "offline" || device.Platform != "ios" || len(device.LatestObservations) != 1 {
		t.Fatalf("offline device: %+v", device)
	}
	if device.LatestObservations[0].Kind != "ios.location" || device.LatestObservations[0].ObservedAgo == "" {
		t.Fatalf("observation metadata: %+v", device.LatestObservations)
	}
}
