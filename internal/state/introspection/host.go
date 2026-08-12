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
			if free > math.MaxInt64 {
				info.DiskFreeBytes = math.MaxInt64
			} else {
				info.DiskFreeBytes = int64(free)
			}
			info.DiskUsedPct = diskUsedPct(free, total)
		}
	}
	return info
}

// diskUsedPct converts a probe reading into the snapshot's percentage:
// float64 math, clamped to [0, 100], and floored at 1 whenever any
// space is used — a large mostly-empty disk must never round its usage
// down to zero, because a zero percentage is omitted from the wire and
// omission is reserved for "the probe failed". A zero total (no probe
// result at all) yields 0, the omitted value.
func diskUsedPct(free, total uint64) int {
	if total == 0 {
		return 0
	}
	if free > total {
		free = total
	}
	pct := int(float64(total-free) / float64(total) * 100)
	pct = min(max(pct, 0), 100)
	if pct == 0 && total > free {
		pct = 1
	}
	return pct
}
