package tools

import (
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/homeassistant"
)

func TestFormatEntityState(t *testing.T) {
	tests := []struct {
		name       string
		state      *homeassistant.State
		wantParts  []string
		wantAbsent []string
	}{
		{
			name: "light with brightness",
			state: &homeassistant.State{
				EntityID: "light.office",
				State:    "on",
				Attributes: map[string]any{
					"friendly_name": "Office Light",
					"brightness":    float64(255),
				},
			},
			wantParts: []string{
				"Entity: light.office",
				"State: on",
				"Name: Office Light",
				"Brightness: 100%",
			},
		},
		{
			name: "sensor with unit",
			state: &homeassistant.State{
				EntityID: "sensor.temperature",
				State:    "22.5",
				Attributes: map[string]any{
					"friendly_name":       "Living Room Temp",
					"unit_of_measurement": "°C",
					"temperature":         float64(22.5),
				},
			},
			wantParts: []string{
				"Entity: sensor.temperature",
				"State: 22.5",
				"Unit: °C",
				"Temperature: 22.5",
			},
		},
		{
			name: "minimal state no attributes",
			state: &homeassistant.State{
				EntityID:   "switch.pump",
				State:      "off",
				Attributes: map[string]any{},
			},
			wantParts: []string{
				"Entity: switch.pump",
				"State: off",
			},
			wantAbsent: []string{
				"Name:",
				"Brightness:",
				"Temperature:",
				"Unit:",
			},
		},
		{
			name: "partial brightness",
			state: &homeassistant.State{
				EntityID: "light.lamp",
				State:    "on",
				Attributes: map[string]any{
					"brightness": float64(127.5),
				},
			},
			wantParts: []string{
				"Brightness: 50%",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatEntityState(tc.state)
			for _, want := range tc.wantParts {
				if !strings.Contains(got, want) {
					t.Errorf("FormatEntityState() missing %q:\n%s", want, got)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("FormatEntityState() should not contain %q:\n%s", absent, got)
				}
			}
		})
	}
}
