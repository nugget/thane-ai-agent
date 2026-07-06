package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

func seedKitchen(f *fakeHAServer) {
	f.states = []homeassistant.State{
		{EntityID: "light.kitchen", State: "on", Attributes: map[string]any{"friendly_name": "Kitchen"}},
		{EntityID: "light.kitchen_counter", State: "off", Attributes: map[string]any{"friendly_name": "Kitchen Counter"}},
	}
}

func TestHandleCallService_UnknownEntitySuggestsAndDoesNotCall(t *testing.T) {
	fake := newFakeHAServer(t)
	seedKitchen(fake)
	reg := fake.registry(t)

	out, err := reg.handleCallService(context.Background(), map[string]any{
		"domain":    "light",
		"service":   "turn_on",
		"entity_id": "light.kitchen_countr", // typo
	})
	if err != nil {
		t.Fatalf("handleCallService: %v", err)
	}

	got := decodeNotFound(t, out)
	if got.Found || got.Reason != "not_found" {
		t.Errorf("expected not_found envelope, got %+v", got)
	}
	if len(got.Candidates) == 0 {
		t.Errorf("expected candidates for a near-miss entity_id")
	}
	if len(fake.serviceCalls) != 0 {
		t.Errorf("service must NOT be called for an unknown entity_id; got %v", fake.serviceCalls)
	}
}

func TestHandleCallService_KnownEntityCalls(t *testing.T) {
	fake := newFakeHAServer(t)
	seedKitchen(fake)
	reg := fake.registry(t)

	out, err := reg.handleCallService(context.Background(), map[string]any{
		"domain":    "light",
		"service":   "turn_on",
		"entity_id": "light.kitchen",
	})
	if err != nil {
		t.Fatalf("handleCallService: %v", err)
	}
	if len(fake.serviceCalls) != 1 {
		t.Errorf("expected exactly one forwarded service call, got %v", fake.serviceCalls)
	}
	var res haCallServiceResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal call result: %v\n%s", err, out)
	}
	if res.Called != "light.turn_on" || res.EntityID != "light.kitchen" {
		t.Errorf("call result identity = %q/%q, want light.turn_on/light.kitchen", res.Called, res.EntityID)
	}
}

func TestHandleGetState_UnknownEntitySuggests(t *testing.T) {
	fake := newFakeHAServer(t)
	seedKitchen(fake)
	reg := fake.registry(t)

	out, err := reg.handleGetState(context.Background(), map[string]any{
		"entity_id": "light.kitchen_countr", // typo
	})
	if err != nil {
		t.Fatalf("handleGetState: %v", err)
	}
	got := decodeNotFound(t, out)
	if got.Found || got.Reason != "not_found" {
		t.Errorf("expected not_found envelope, got %+v", got)
	}
	if got.RequestedEntityID != "light.kitchen_countr" {
		t.Errorf("RequestedEntityID = %q", got.RequestedEntityID)
	}
}

func TestHandleControlDevice_NoMatchReportsNotActed(t *testing.T) {
	fake := newFakeHAServer(t)
	seedKitchen(fake)
	reg := fake.registry(t)

	out, err := reg.handleControlDevice(context.Background(), map[string]any{
		"description": "nonexistent widget zzzqqq",
		"action":      "turn_on",
	})
	if err != nil {
		t.Fatalf("handleControlDevice: %v", err)
	}

	var got ControlDeviceNoMatchResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal control no-match: %v\n%s", err, out)
	}
	if got.Acted {
		t.Errorf("Acted = true, want false")
	}
	if got.Reason != "no_match" {
		t.Errorf("Reason = %q, want no_match", got.Reason)
	}
	if len(fake.serviceCalls) != 0 {
		t.Errorf("no service call should be made on no-match; got %v", fake.serviceCalls)
	}
}
