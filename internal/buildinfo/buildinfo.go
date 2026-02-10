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

// Info returns all build and runtime info as a map.
func Info() map[string]string {
	return map[string]string{
		"version":    Version,
		"git_commit": GitCommit,
		"git_branch": GitBranch,
		"build_time": BuildTime,
		"go_version": runtime.Version(),
		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
		"uptime":     Uptime().String(),
	}
}

// Uptime returns the duration since process start.
func Uptime() time.Duration {
	return time.Since(startTime).Truncate(time.Second)
}

// String returns a one-line summary for logging.
func String() string {
	return fmt.Sprintf("Thane %s (%s@%s) built %s", Version, GitCommit, GitBranch, BuildTime)
}
