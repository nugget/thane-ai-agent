// Package buildinfo holds version and build metadata stamped at compile time via ldflags.
package buildinfo

import (
	"fmt"
	"runtime"
	"time"
)

// These variables are set at build time via -ldflags.
var (
	Version   = "dev"
	GitCommit = "unknown"
	GitBranch = "unknown"
	BuildTime = "unknown"
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

// UserAgent returns an HTTP User-Agent string suitable for outgoing
// requests. Format follows the convention: ProductName/Version (+URL).
func UserAgent() string {
	return fmt.Sprintf("Thane/%s (+https://github.com/nugget/thane-ai-agent)", Version)
}
