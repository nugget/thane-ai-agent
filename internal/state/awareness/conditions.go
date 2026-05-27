// Package awareness provides system prompt context providers that give the
// agent real-time environmental awareness — current conditions, state
// changes, and watched entities. This information affects every decision
// (e.g., "should I wake the user at 3am?").
//
// The section is placed early in the system prompt (after persona and
// inject files, before talents) because models attend more strongly to
// content near the beginning. The heading level (H1) signals importance.
package awareness

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/buildinfo"
)

// conditionsPayload is the JSON projection emitted under the
// "# Current Conditions" heading. Field names are stable across turns so
// the model can compare snapshots; pre-computed fields (weekday,
// uptime_seconds, environment) remove arithmetic the model would
// otherwise have to do itself.
type conditionsPayload struct {
	Time           string `json:"time"`
	TimeZone       string `json:"time_zone,omitempty"`
	TimeZoneAbbrev string `json:"time_zone_abbrev"`
	Weekday        string `json:"weekday"`
	Host           string `json:"host"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	Environment    string `json:"environment"`
	Version        string `json:"version"`
	Commit         string `json:"commit"`
	Branch         string `json:"branch"`
	UptimeSeconds  int64  `json:"uptime_seconds"`
}

// CurrentConditions returns a formatted "# Current Conditions" section
// for injection into the system prompt. The body is a compact JSON object
// (typed runtime state per docs/model-facing-context.md). The timezone
// parameter should be an IANA timezone name (e.g., "America/Chicago"). If
// empty or invalid, the system's local timezone is used and the IANA name
// is omitted from the output.
func CurrentConditions(timezone string) string {
	loc := time.Now().Location()
	tzResolved := false
	if timezone != "" {
		if parsed, err := time.LoadLocation(timezone); err == nil {
			loc = parsed
			tzResolved = true
		}
	}
	now := time.Now().In(loc)
	zoneAbbrev, _ := now.Zone()

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}

	payload := conditionsPayload{
		Time:           now.Format(time.RFC3339),
		TimeZoneAbbrev: zoneAbbrev,
		Weekday:        now.Weekday().String(),
		Host:           hostname,
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		Environment:    detectEnvironment(),
		Version:        buildinfo.Version,
		Commit:         buildinfo.GitCommit,
		Branch:         buildinfo.GitBranch,
		UptimeSeconds:  int64(buildinfo.Uptime().Truncate(time.Second).Seconds()),
	}
	if tzResolved {
		payload.TimeZone = timezone
	}

	var sb strings.Builder
	sb.WriteString("# Current Conditions\n\n")
	sb.WriteString(promptfmt.MarshalCompact(payload))
	return sb.String()
}

// detectEnvironment returns "container" or "bare metal" based on
// heuristics appropriate for the current OS.
func detectEnvironment() string {
	// Check for Docker / container indicators on Linux.
	if runtime.GOOS == "linux" {
		// /.dockerenv is created by Docker.
		if _, err := os.Stat("/.dockerenv"); err == nil {
			return "container"
		}
		// Check cgroup for container runtimes (docker, lxc, kubepods).
		if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
			content := string(data)
			if strings.Contains(content, "docker") ||
				strings.Contains(content, "lxc") ||
				strings.Contains(content, "kubepods") {
				return "container"
			}
		}
		// Check for container environment variables.
		if os.Getenv("container") != "" || os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
			return "container"
		}
	}
	return "bare metal"
}

// formatUptime formats a duration as a human-readable uptime string.
// Examples: "4h 23m", "2d 5h", "45m", "30s". Used only by
// FormatContextUsage for the operator-facing context line, where a
// compact human-readable string is preferred over a delta. CurrentConditions
// emits uptime as a raw seconds integer inside the JSON payload.
func formatUptime(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}

	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}
