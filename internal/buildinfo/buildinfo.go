// Package buildinfo holds version and build metadata stamped at compile time via ldflags.
package buildinfo

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

// These variables are set at build time via -ldflags.
var (
	Version   = "dev"
	GitCommit = "unknown"
	GitBranch = "unknown"
	BuildTime = "unknown"
	Changelog = "" // commits since last release tag, semicolon-separated
)

// startTime records when the process started.
var startTime = time.Now()

// BuildInfo returns compile-time and platform metadata. This is the
// static information appropriate for "thane version" output.
func BuildInfo() map[string]string {
	return map[string]string{
		"version":    Version,
		"git_commit": GitCommit,
		"git_branch": GitBranch,
		"build_time": BuildTime,
		"go_version": runtime.Version(),
		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
	}
}

// RuntimeInfo returns build metadata plus runtime state (uptime, etc.).
// Use this for health endpoints and status pages.
func RuntimeInfo() map[string]string {
	info := BuildInfo()
	info["uptime"] = Uptime().String()
	return info
}

// Uptime returns the duration since process start.
func Uptime() time.Duration {
	return time.Since(startTime).Truncate(time.Second)
}

// String returns a one-line summary for logging.
func String() string {
	return fmt.Sprintf("Thane %s (%s@%s) built %s", Version, GitCommit, GitBranch, BuildTime)
}

// ContextString returns a compact multi-line summary for system prompt injection.
// Includes version, build status, and changelog when available.
func ContextString() string {
	// Determine release status
	status := "dev"
	if strings.Contains(Version, "-") {
		// e.g. v0.3.1-2-gf8923d2 or v0.3.1-2-gf8923d2-dirty
		if strings.HasSuffix(Version, "-dirty") {
			status = "dev, dirty"
		} else {
			status = "dev"
		}
	} else if Version != "dev" {
		status = "release"
	}

	// Truncate build time to minute precision
	buildShort := BuildTime
	if t, err := time.Parse(time.RFC3339, BuildTime); err == nil {
		buildShort = t.Format("2006-01-02T15:04Z")
	} else if t, err := time.Parse("2006-01-02T15:04:05Z", BuildTime); err == nil {
		buildShort = t.Format("2006-01-02T15:04Z")
	}

	line := fmt.Sprintf("%s (%s, %s) | %s@%s | built %s",
		Version, status, runtime.GOARCH, GitCommit, GitBranch, buildShort)

	if Changelog != "" {
		line += "\nChanges since last release: " + Changelog
	}

	return line
}

// UserAgent returns an HTTP User-Agent string suitable for outgoing
// requests. Format follows the convention: ProductName/Version (+URL).
func UserAgent() string {
	return fmt.Sprintf("Thane/%s (+https://github.com/nugget/thane-ai-agent)", Version)
}
