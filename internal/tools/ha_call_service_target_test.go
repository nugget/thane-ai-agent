package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

func targetTestServer(t *testing.T) *fakeHAServer {
	t.Helper()
	fake := newFakeHAServer(t)
	fake.states = []homeassistant.State{
		{EntityID: "light.office_main", State: "on", Attributes: map[string]any{"friendly_name": "Office Main"}},
		{EntityID: "light.office_lamp", State: "on", Attributes: map[string]any{"friendly_name": "Office Lamp"}},
	}
	fake.areas = []map[string]any{
		{"area_id": "office", "name": "Office", "aliases": []string{"study"}},
		{"area_id": "garage", "name": "Garage"},
	}
	fake.labels = []map[string]any{
		{"label_id": "critical_lights", "name": "Critical Lights"},
	}
	return fake
}

func decodeCallResult(t *testing.T, raw string) haCallServiceResult {
	t.Helper()
	var out haCallServiceResult
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal call result: %v\n%s", err, raw)
	}
	return out
}

func TestHACallService_AreaTargetByNameResolvesAndFansOut(t *testing.T) {
	fake := targetTestServer(t)
	fake.serviceChanged = []homeassistant.State{
		{EntityID: "light.office_main", State: "off"},
		{EntityID: "light.office_lamp", State: "off"},
	}
	reg := fake.registry(t)

	raw, err := reg.Execute(context.Background(), "ha_call_service", `{"domain":"light","service":"turn_off","target":{"area_id":"Office"}}`)
	if err != nil {
		t.Fatalf("area target: %v", err)
	}
	res := decodeCallResult(t, raw)
	if res.Called != "light.turn_off" {
		t.Errorf("called = %q", res.Called)
	}
	// The human name resolved to the registry id before the call.
	if res.Target["area_id"] != "office" {
		t.Errorf("target.area_id = %v, want resolved id \"office\"", res.Target["area_id"])
	}
	if res.ChangedCount != 2 || len(res.Changed) != 2 {
		t.Errorf("changed = %d/%v, want the 2-entity fan-out visible", res.ChangedCount, res.Changed)
	}
	// The resolved id (not the name) is what went over the wire.
	if len(fake.servicePayloads) != 1 || fake.servicePayloads[0]["area_id"] != "office" {
		t.Errorf("wire payload = %v, want area_id \"office\"", fake.servicePayloads)
	}
}

func TestHACallService_AreaAliasAndIDPassThrough(t *testing.T) {
	fake := targetTestServer(t)
	reg := fake.registry(t)

	// Alias resolves.
	raw, err := reg.Execute(context.Background(), "ha_call_service", `{"domain":"light","service":"turn_on","target":{"area_id":"study"}}`)
	if err != nil {
		t.Fatalf("alias target: %v", err)
	}
	if res := decodeCallResult(t, raw); res.Target["area_id"] != "office" {
		t.Errorf("alias should resolve to office, got %v", res.Target["area_id"])
	}
	// Exact id passes through untouched.
	raw, err = reg.Execute(context.Background(), "ha_call_service", `{"domain":"light","service":"turn_on","target":{"area_id":"garage"}}`)
	if err != nil {
		t.Fatalf("id target: %v", err)
	}
	if res := decodeCallResult(t, raw); res.Target["area_id"] != "garage" {
		t.Errorf("id should pass through, got %v", res.Target["area_id"])
	}
}

func TestHACallService_UnknownAreaFailsFastWithKnownNames(t *testing.T) {
	fake := targetTestServer(t)
	reg := fake.registry(t)

	_, err := reg.Execute(context.Background(), "ha_call_service", `{"domain":"light","service":"turn_on","target":{"area_id":"Atrium"}}`)
	if err == nil {
		t.Fatal("unknown area must fail fast, not silently no-op")
	}
	if got := err.Error(); !containsAll(got, "Atrium", "Office", "Garage") {
		t.Errorf("error should teach known areas, got %q", got)
	}
	if len(fake.servicePayloads) != 0 {
		t.Errorf("no service call may reach HA on a failed resolution; got %v", fake.servicePayloads)
	}
}

func TestHACallService_ArgumentValidation(t *testing.T) {
	fake := targetTestServer(t)
	reg := fake.registry(t)

	cases := map[string]string{
		"neither entity nor target": `{"domain":"light","service":"turn_on"}`,
		"both entity and target":    `{"domain":"light","service":"turn_on","entity_id":"light.office_main","target":{"area_id":"office"}}`,
		"unknown target key":        `{"domain":"light","service":"turn_on","target":{"room_id":"office"}}`,
		"empty target":              `{"domain":"light","service":"turn_on","target":{}}`,
	}
	for name, args := range cases {
		if _, err := reg.Execute(context.Background(), "ha_call_service", args); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestHACallService_ZeroChangesCarriesNote(t *testing.T) {
	fake := targetTestServer(t)
	fake.serviceChanged = []homeassistant.State{}
	reg := fake.registry(t)

	raw, err := reg.Execute(context.Background(), "ha_call_service", `{"domain":"light","service":"turn_on","target":{"label_id":"Critical Lights"}}`)
	if err != nil {
		t.Fatalf("label target: %v", err)
	}
	res := decodeCallResult(t, raw)
	if res.Target["label_id"] != "critical_lights" {
		t.Errorf("label name should resolve, got %v", res.Target["label_id"])
	}
	if res.ChangedCount != 0 || res.Note == "" {
		t.Errorf("zero-change fan-out must carry the explanatory note: %+v", res)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

func TestHACallService_DataCannotOverrideAddressing(t *testing.T) {
	// data.entity_id would silently clobber the verified addressing and
	// desync the reported result from the wire (PR #1189 review).
	fake := targetTestServer(t)
	reg := fake.registry(t)

	_, err := reg.Execute(context.Background(), "ha_call_service",
		`{"domain":"light","service":"turn_on","entity_id":"light.office_main","data":{"entity_id":"light.nonexistent","brightness_pct":50}}`)
	if err == nil {
		t.Fatal("addressing keys in data must be rejected")
	}
	if !strings.Contains(err.Error(), "data.entity_id is addressing") {
		t.Errorf("error should teach where addressing belongs, got %q", err.Error())
	}
	if len(fake.servicePayloads) != 0 {
		t.Errorf("no call may reach HA; got %v", fake.servicePayloads)
	}
}

func TestHACallService_NonObjectTargetTeaches(t *testing.T) {
	fake := targetTestServer(t)
	reg := fake.registry(t)

	_, err := reg.Execute(context.Background(), "ha_call_service",
		`{"domain":"light","service":"turn_on","target":"office"}`)
	if err == nil {
		t.Fatal("string target must error, not fall through to the generic message")
	}
	if !strings.Contains(err.Error(), "target must be an object") {
		t.Errorf("error should name the real problem, got %q", err.Error())
	}
}

func TestHACallService_TargetEntityTypoIsRecoverable(t *testing.T) {
	// Consistency with the single-entity path: an unknown target entity
	// returns the did-you-mean envelope as a RESULT, not a tool error.
	fake := targetTestServer(t)
	reg := fake.registry(t)

	out, err := reg.Execute(context.Background(), "ha_call_service",
		`{"domain":"light","service":"turn_on","target":{"entity_id":"light.office_mian"}}`)
	if err != nil {
		t.Fatalf("typo'd target entity must be recoverable, got error: %v", err)
	}
	var envelope struct {
		Found      bool `json:"found"`
		Candidates []struct {
			EntityID string `json:"entity_id"`
		} `json:"candidates"`
	}
	if uerr := json.Unmarshal([]byte(out), &envelope); uerr != nil {
		t.Fatalf("expected not-found envelope, got: %s", out)
	}
	if envelope.Found || len(envelope.Candidates) == 0 {
		t.Errorf("envelope should carry did-you-mean candidates: %s", out)
	}
	if len(fake.servicePayloads) != 0 {
		t.Errorf("no call may reach HA on a typo; got %v", fake.servicePayloads)
	}
}
