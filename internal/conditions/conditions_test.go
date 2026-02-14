package conditions

import (
	"strings"
	"testing"
	"time"
)

func TestCurrentConditions_ContainsRequiredSections(t *testing.T) {
	result := CurrentConditions("")

	required := []string{
		"# Current Conditions",
		"**Time:**",
		"**Host:**",
		"**Thane:**",
		"**Uptime:**",
	}

	for _, section := range required {
		if !strings.Contains(result, section) {
			t.Errorf("CurrentConditions() missing %q\nGot:\n%s", section, result)
		}
	}
}

func TestCurrentConditions_WithTimezone(t *testing.T) {
	result := CurrentConditions("America/Chicago")

	if !strings.Contains(result, "America/Chicago") {
		t.Errorf("CurrentConditions(America/Chicago) should include timezone name\nGot:\n%s", result)
	}
}

func TestCurrentConditions_InvalidTimezone(t *testing.T) {
	// Use a timezone name that time.LoadLocation rejects on all platforms.
	const bogus = "Bogus/ZZZZZ_Not_Real_12345"
	if _, err := time.LoadLocation(bogus); err == nil {
		t.Skip("platform resolved bogus timezone; cannot test fallback")
	}

	result := CurrentConditions(bogus)

	if !strings.Contains(result, "**Time:**") {
		t.Errorf("CurrentConditions with invalid timezone should still include time\nGot:\n%s", result)
	}
	// Should NOT contain the invalid timezone name.
	if strings.Contains(result, bogus) {
		t.Errorf("CurrentConditions with invalid timezone should not include invalid name\nGot:\n%s", result)
	}
}

func TestCurrentConditions_EmptyTimezone(t *testing.T) {
	// Should use local timezone.
	result := CurrentConditions("")
	if !strings.Contains(result, "**Time:**") {
		t.Errorf("CurrentConditions('') should still include time\nGot:\n%s", result)
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
