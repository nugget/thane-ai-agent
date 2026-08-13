package introspection

import (
	"math"
	"runtime"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// collectHostInfo assembles the stdlib-only host snapshot. Disk numbers
// come from the platform probe in disk_unix.go; their presence rules
// live on the HostInfo fields — a failed or unsupported probe omits
// both, absence rather than a fake zero disk. The math is clamped, so
// an oversized or inconsistent probe result (free > total, counts past
// int64) degrades to a sane value instead of wrapping.
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
			freeBytes := int64(math.MaxInt64)
			if free <= math.MaxInt64 {
				freeBytes = int64(free)
			}
			pct := diskUsedPct(free, total)
			info.DiskFreeBytes = &freeBytes
			info.DiskUsedPct = &pct
		}
	}
	return info
}

// diskUsedPct converts a probe reading into the snapshot's percentage:
// float64 math, rounded to nearest, clamped to [0, 100]. Zero is a
// real reading — presence on the wire is carried by the HostInfo
// pointer fields, so the value never has to lie to stay visible.
func diskUsedPct(free, total uint64) int {
	if total == 0 {
		return 0
	}
	if free > total {
		free = total
	}
	pct := int(float64(total-free)/float64(total)*100 + 0.5)
	return min(max(pct, 0), 100)
}
