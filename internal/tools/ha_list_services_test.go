package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

func servicesFixture() []homeassistant.ServiceDomain {
	return []homeassistant.ServiceDomain{
		{
			Domain: "light",
			Services: map[string]homeassistant.ServiceDescription{
				"turn_on": {
					Name:        "Turn on",
					Description: "Turns on one or more lights.",
					Target:      map[string]any{"entity": map[string]any{"domain": "light"}},
					Fields: map[string]homeassistant.ServiceField{
						"brightness_pct": {Name: "Brightness", Description: "Percent brightness.", Example: 80},
						"transition":     {Description: "Fade duration in seconds."},
					},
				},
				"turn_off": {Name: "Turn off", Target: map[string]any{}},
			},
		},
		{
			Domain: "notify",
			Services: map[string]homeassistant.ServiceDescription{
				"send_message": {
					Name:   "Send message",
					Fields: map[string]homeassistant.ServiceField{"message": {Required: true}},
				},
			},
		},
	}
}

func TestHAListServices_IndexIsCompactAndSorted(t *testing.T) {
	fake := newFakeHAServer(t)
	fake.services = servicesFixture()
	reg := fake.registry(t)

	raw, err := reg.Execute(context.Background(), "ha_list_services", `{}`)
	if err != nil {
		t.Fatalf("ha_list_services: %v", err)
	}
	var idx haServicesIndex
	if err := json.Unmarshal([]byte(raw), &idx); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	if idx.DomainCount != 2 || len(idx.Domains) != 2 {
		t.Fatalf("domain count = %d/%d entries, want 2/2", idx.DomainCount, len(idx.Domains))
	}
	// Sorted: light before notify; service names sorted within a domain.
	if idx.Domains[0].Domain != "light" || idx.Domains[1].Domain != "notify" {
		t.Errorf("domain order = %q,%q, want light,notify", idx.Domains[0].Domain, idx.Domains[1].Domain)
	}
	if idx.Domains[0].Services[0] != "turn_off" || idx.Domains[0].Services[1] != "turn_on" {
		t.Errorf("light services = %v, want sorted [turn_off turn_on]", idx.Domains[0].Services)
	}
	// The index is names-only: no field schemas leak into the compact shape.
	if strings.Contains(raw, "brightness_pct") {
		t.Errorf("index leaked field detail:\n%s", raw)
	}
}

func TestHAListServices_DomainDetailFieldsAndTarget(t *testing.T) {
	fake := newFakeHAServer(t)
	fake.services = servicesFixture()
	reg := fake.registry(t)

	raw, err := reg.Execute(context.Background(), "ha_list_services", `{"domain":"light"}`)
	if err != nil {
		t.Fatalf("ha_list_services domain: %v", err)
	}
	var det haServicesDetail
	if err := json.Unmarshal([]byte(raw), &det); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	if det.Domain != "light" || det.Count != 2 {
		t.Fatalf("domain/count = %q/%d, want light/2", det.Domain, det.Count)
	}
	// Sorted by name: turn_off first, then turn_on.
	on := det.Services[1]
	if on.Service != "light.turn_on" {
		t.Fatalf("services[1] = %q, want light.turn_on", on.Service)
	}
	if !on.AcceptsTarget {
		t.Error("light.turn_on should report accepts_target")
	}
	if len(on.Fields) != 2 || on.Fields[0].Field != "brightness_pct" {
		t.Errorf("fields = %+v, want sorted with brightness_pct first", on.Fields)
	}
}

func TestHAListServices_CombinedServiceForm(t *testing.T) {
	fake := newFakeHAServer(t)
	fake.services = servicesFixture()
	reg := fake.registry(t)

	raw, err := reg.Execute(context.Background(), "ha_list_services", `{"service":"notify.send_message"}`)
	if err != nil {
		t.Fatalf("combined form: %v", err)
	}
	var det haServicesDetail
	if err := json.Unmarshal([]byte(raw), &det); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if det.Count != 1 || det.Services[0].Service != "notify.send_message" {
		t.Fatalf("got %+v, want exactly notify.send_message", det.Services)
	}
	if det.Services[0].AcceptsTarget {
		t.Error("notify.send_message has no target block; accepts_target must be false")
	}
	if len(det.Services[0].Fields) != 1 || !det.Services[0].Fields[0].Required {
		t.Errorf("message field should be present and required: %+v", det.Services[0].Fields)
	}
}

func TestHAListServices_Errors(t *testing.T) {
	fake := newFakeHAServer(t)
	fake.services = servicesFixture()
	reg := fake.registry(t)

	cases := map[string]string{
		"unknown domain":          `{"domain":"vacuum"}`,
		"unknown service":         `{"domain":"light","service":"strobe"}`,
		"service without domain":  `{"service":"turn_on"}`,
		"domain/service mismatch": `{"domain":"notify","service":"light.turn_on"}`,
	}
	for name, args := range cases {
		if _, err := reg.Execute(context.Background(), "ha_list_services", args); err == nil {
			t.Errorf("%s: expected error, got success", name)
		}
	}
}
