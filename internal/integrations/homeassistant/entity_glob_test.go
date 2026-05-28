package homeassistant

import "testing"

func TestMatchEntityGlob(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pattern string
		entity  string
		want    bool
	}{
		{"within domain", "binary_sensor.*door*", "binary_sensor.front_door", true},
		{"within domain no match", "binary_sensor.*door*", "binary_sensor.kitchen_window", false},
		{"cross-domain suffix", "*_temperature", "sensor.office_temperature", true},
		{"cross-domain suffix climate", "*_temperature", "climate.living_room_temperature", true},
		{"domain prefix wildcard", "light.office_*", "light.office_lamp", true},
		{"domain prefix wildcard miss", "light.office_*", "light.hall", false},
		{"star spans the dot", "*office*", "light.office_lamp", true},
		{"exact", "light.hall", "light.hall", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MatchEntityGlob(tt.pattern, tt.entity)
			if err != nil {
				t.Fatalf("MatchEntityGlob error: %v", err)
			}
			if got != tt.want {
				t.Errorf("MatchEntityGlob(%q, %q) = %v, want %v", tt.pattern, tt.entity, got, tt.want)
			}
		})
	}
}

func TestMatchEntityGlob_MalformedReturnsError(t *testing.T) {
	t.Parallel()
	if _, err := MatchEntityGlob("light.[bad", "light.x"); err == nil {
		t.Fatal("expected error for malformed pattern")
	}
}

func TestValidateEntityGlob(t *testing.T) {
	t.Parallel()
	if err := ValidateEntityGlob("binary_sensor.*door*"); err != nil {
		t.Errorf("valid pattern reported error: %v", err)
	}
	if err := ValidateEntityGlob("light.[bad"); err == nil {
		t.Error("malformed pattern should report an error")
	}
}
