package introspection

import (
	"runtime"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// collectHostInfo assembles the stdlib-only host snapshot. Disk numbers
// come from the platform probe in disk_unix.go and are omitted (zero)
// when the probe is unsupported or fails — absence, not a fake zero
// disk.
func collectHostInfo(dataDir string, startedAt, now time.Time) HostInfo {
	info := HostInfo{
		Goroutines: runtime.NumGoroutine(),
		GOMAXPROCS: runtime.GOMAXPROCS(0),
	}
	if !startedAt.IsZero() {
		info.UptimeDelta = promptfmt.FormatDeltaOnly(startedAt, now)
	}
	if dataDir != "" {
		if free, total, err := diskFree(dataDir); err == nil && total > 0 {
			info.DiskFreeBytes = int64(free)
			info.DiskUsedPct = int((total - free) * 100 / total)
		}
	}
	return info
}
