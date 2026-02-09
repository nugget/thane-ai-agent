package tools

import (
	"testing"

	"github.com/nugget/thane-ai-agent/internal/homeassistant"
)

func TestInferDomainFromDescription(t *testing.T) {
	tests := []struct {
		description string
		want        string
	}{
		{"office light", "light"},
		{"LED strip", "light"},
		{"ceiling lamp", "light"},
		{"kitchen fan", "fan"},
		{"exhaust fan", "fan"},
		{"front door lock", "lock"},
		{"garage door", "cover"},
		{"window blinds", "cover"},
		{"living room thermostat", "climate"},
		{"temperature sensor", "sensor"},
		{"random device", ""}, // No match
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			got := inferDomainFromDescription(tt.description)
			if got != tt.want {
				t.Errorf("inferDomainFromDescription(%q) = %q, want %q", tt.description, got, tt.want)
			}
		})
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"light.office_lamp", []string{"light", "office", "lamp"}},
		{"ap-hor-office", []string{"ap", "hor", "office"}},
		{"simple", []string{"simple"}},
		{"a b c", []string{}}, // Single chars filtered
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := tokenize(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("tokenize(%q) = %v, want %v", tt.input, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("tokenize(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestTokenMatchScore(t *testing.T) {
	tests := []struct {
		name   string
		query  []string
		target []string
		minExp float64 // Minimum expected score
	}{
		{"exact match", []string{"office", "light"}, []string{"office", "light"}, 1.0},
		{"partial match", []string{"office"}, []string{"office", "lamp"}, 1.0},
		{"substring", []string{"off"}, []string{"office"}, 0.7},
		{"no match", []string{"bedroom"}, []string{"office", "light"}, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokenMatchScore(tt.query, tt.target)
			if got < tt.minExp {
				t.Errorf("tokenMatchScore(%v, %v) = %v, want >= %v", tt.query, tt.target, got, tt.minExp)
			}
		})
	}
}

func TestFuzzyMatchEntityInfos(t *testing.T) {
	entities := []homeassistant.EntityInfo{
		{EntityID: "light.office_lamp", FriendlyName: "Office Lamp"},
		{EntityID: "light.ap_hor_office_led", FriendlyName: "AP HOR Office LED"},
		{EntityID: "light.bedroom_ceiling", FriendlyName: "Bedroom Ceiling Light"},
	}

	tests := []struct {
		description string
		wantFirst   string // Expected first match entity_id
	}{
		{"office lamp", "light.office_lamp"},
		{"office LED", "light.ap_hor_office_led"},
		{"bedroom", "light.bedroom_ceiling"},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			matches := fuzzyMatchEntityInfos(tt.description, entities)
			if len(matches) == 0 {
				t.Errorf("fuzzyMatchEntityInfos(%q) returned no matches", tt.description)
				return
			}
			if matches[0].EntityID != tt.wantFirst {
				t.Errorf("fuzzyMatchEntityInfos(%q) first match = %q, want %q",
					tt.description, matches[0].EntityID, tt.wantFirst)
			}
		})
	}
}

func TestToJSON(t *testing.T) {
	result := FindEntityResult{
		Found:    true,
		EntityID: "light.test",
	}
	got := toJSON(result)
	if got == "" || got == `{"error":"json encoding failed"}` {
		t.Errorf("toJSON failed unexpectedly: %s", got)
	}
}
