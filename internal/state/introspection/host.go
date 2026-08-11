package introspection

import (
	"math"
	"runtime"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// collectHostInfo assembles the stdlib-only host snapshot. Disk numbers
// come from the platform probe in disk_unix.go and are omitted (zero)
// when the probe is unsupported or fails — absence, not a fake zero
// disk. The percentage math runs in float64 and both outputs are
// clamped, so an oversized or inconsistent probe result (free > total,
// counts past int64) degrades to a sane value instead of wrapping.
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
			if free > total {
				free = total
			}
			if free > math.MaxInt64 {
				info.DiskFreeBytes = math.MaxInt64
			} else {
				info.DiskFreeBytes = int64(free)
			}
			pct := int(float64(total-free) / float64(total) * 100)
			info.DiskUsedPct = min(max(pct, 0), 100)
		}
	}
	return info
}
