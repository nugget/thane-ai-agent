// Package conditions generates the "Current Conditions" section of the
// system prompt. This gives the agent real-time awareness of when and
// where it's running — information that affects every decision (e.g.,
// "should I wake the user at 3am?").
//
// The section is placed early in the system prompt (after persona and
// inject files, before talents) because models attend more strongly to
// content near the beginning. The heading level (H1) signals importance.
package conditions

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/buildinfo"
)

// CurrentConditions returns a formatted "# Current Conditions" section
// for injection into the system prompt. The timezone parameter should be
// an IANA timezone name (e.g., "America/Chicago"). If empty or invalid,
// the system's local timezone is used.
func CurrentConditions(timezone string) string {
	var sb strings.Builder

	sb.WriteString("# Current Conditions\n\n")

	// Time — use configured timezone, fall back to system local.
	loc := time.Now().Location()
	tzResolved := false
	if timezone != "" {
		if parsed, err := time.LoadLocation(timezone); err == nil {
			loc = parsed
			tzResolved = true
		}
	}
	now := time.Now().In(loc)
	zoneName, _ := now.Zone()

	// Format: Saturday, February 14, 2026 at 15:45 CST (America/Chicago)
	sb.WriteString("**Time:** ")
	sb.WriteString(now.Format("Monday, January 2, 2006 at 15:04 "))
	sb.WriteString(zoneName)
	// Include IANA name when we successfully resolved a configured timezone.
	if tzResolved && timezone != zoneName {
		sb.WriteString(" (")
		sb.WriteString(timezone)
		sb.WriteString(")")
	}
	sb.WriteString("\n")

	// Host — hostname, OS/arch, bare metal vs container.
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	env := detectEnvironment()
	sb.WriteString(fmt.Sprintf("**Host:** %s (%s/%s, %s)\n", hostname, runtime.GOOS, runtime.GOARCH, env))

	// Thane — version and commit.
	sb.WriteString(fmt.Sprintf("**Thane:** %s (%s@%s)\n", buildinfo.Version, buildinfo.GitCommit, buildinfo.GitBranch))

	// Uptime.
	uptime := buildinfo.Uptime()
	sb.WriteString(fmt.Sprintf("**Uptime:** %s", formatUptime(uptime)))

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
// Examples: "4h 23m", "2d 5h", "45m", "30s".
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
