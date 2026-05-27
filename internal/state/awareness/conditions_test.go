package awareness

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// parseConditionsBody extracts the JSON payload that follows the
// "# Current Conditions\n\n" heading and unmarshals it for assertion.
func parseConditionsBody(t *testing.T, out string) map[string]any {
	t.Helper()

	const heading = "# Current Conditions\n\n"
	if !strings.HasPrefix(out, heading) {
		t.Fatalf("CurrentConditions output missing heading prefix\nGot:\n%s", out)
	}
	body := strings.TrimPrefix(out, heading)

	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("CurrentConditions body not valid JSON: %v\nBody: %s", err, body)
	}
	return got
}

func TestCurrentConditions_JSONPayloadHasRequiredFields(t *testing.T) {
	got := parseConditionsBody(t, CurrentConditions(""))

	required := []string{
		"time", "time_zone_abbrev", "weekday",
		"host", "os", "arch", "environment",
		"version", "commit", "branch", "uptime_seconds",
	}
	for _, field := range required {
		if _, ok := got[field]; !ok {
			t.Errorf("conditions JSON missing %q field\nGot: %v", field, got)
		}
	}

	if _, err := time.Parse(time.RFC3339, got["time"].(string)); err != nil {
		t.Errorf("conditions.time not RFC3339: %v", err)
	}
}

func TestCurrentConditions_WithTimezoneSetsIANAName(t *testing.T) {
	got := parseConditionsBody(t, CurrentConditions("America/Chicago"))

	if tz, _ := got["time_zone"].(string); tz != "America/Chicago" {
		t.Errorf("conditions.time_zone = %q; want America/Chicago", tz)
	}
}

func TestCurrentConditions_InvalidTimezoneOmitsIANA(t *testing.T) {
	const bogus = "Bogus/ZZZZZ_Not_Real_12345"
	if _, err := time.LoadLocation(bogus); err == nil {
		t.Skip("platform resolved bogus timezone; cannot test fallback")
	}

	got := parseConditionsBody(t, CurrentConditions(bogus))

	if _, ok := got["time_zone"]; ok {
		t.Errorf("conditions.time_zone should be omitted for invalid timezone\nGot: %v", got["time_zone"])
	}
	if tz, _ := got["time_zone_abbrev"].(string); tz == "" {
		t.Errorf("conditions.time_zone_abbrev should still be present")
	}
}

func TestCurrentConditions_EmptyTimezoneOmitsIANA(t *testing.T) {
	got := parseConditionsBody(t, CurrentConditions(""))

	if _, ok := got["time_zone"]; ok {
		t.Errorf("conditions.time_zone should be omitted when no timezone is configured")
	}
}

func TestDetectEnvironment(t *testing.T) {
	// On a standard dev machine, this should return "bare metal".
	// In CI containers, it might return "container" — both are valid.
	env := detectEnvironment()
	if env != "bare metal" && env != "container" {
		t.Errorf("detectEnvironment() = %q; want 'bare metal' or 'container'", env)
	}
}

func TestFormatUptime(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{45 * time.Minute, "45m"},
		{2*time.Hour + 15*time.Minute, "2h 15m"},
		{25 * time.Hour, "1d 1h"},
		{48*time.Hour + 30*time.Minute, "2d 0h"},
		{72 * time.Hour, "3d 0h"},
	}

	for _, tt := range tests {
		t.Run(tt.duration.String(), func(t *testing.T) {
			got := formatUptime(tt.duration)
			if got != tt.want {
				t.Errorf("formatUptime(%v) = %q, want %q", tt.duration, got, tt.want)
			}
		})
	}
}
