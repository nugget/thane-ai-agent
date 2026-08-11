package introspection

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/connwatch"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/checkout"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/platform/memguard"
	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
	"github.com/nugget/thane-ai-agent/internal/platform/telemetry"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

// Annunciator row statuses. Three values, not a gradient: a row is
// healthy, degraded but operating, or failed. Detail carries the why.
const (
	HealthOK       = "ok"
	HealthDegraded = "degraded"
	HealthFailed   = "failed"
)

// queueBacklogStaleAfter is how old a partition's oldest pending item
// may grow before the queue_backlog row degrades: an hour of unclaimed
// work means the consumer is not keeping up or not running.
const queueBacklogStaleAfter = time.Hour

// HealthRow is one annunciator lamp: a subsystem, its status, and a
// precomputed detail sentence — the model never derives health from raw
// numbers.
type HealthRow struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"`
	LastCheck string `json:"last_check,omitempty"` // delta, e.g. "-42s"
}

// HostInfo is the stdlib-only host snapshot: process shape and the disk
// under the data directory. No gopsutil — everything here comes from
// runtime, syscall, and os.
type HostInfo struct {
	UptimeDelta   string `json:"uptime_delta,omitempty"`
	Goroutines    int    `json:"goroutines"`
	GOMAXPROCS    int    `json:"gomaxprocs"`
	DiskFreeBytes int64  `json:"disk_free_bytes,omitempty"`
	DiskUsedPct   int    `json:"disk_used_pct,omitempty"`
}

// LoopCensus is the fleet rollup: totals by state, the degraded loops
// by name (capped with an explicit remainder), and the busiest wakers —
// the first place a wake storm shows.
type LoopCensus struct {
	Total             int            `json:"total"`
	ByState           map[string]int `json:"by_state,omitempty"`
	Degraded          int            `json:"degraded"`
	DegradedLoops     []string       `json:"degraded_loops,omitempty"`
	DegradedTruncated bool           `json:"degraded_truncated,omitempty"`
	// TopWakers ranks loops by trailing-day iteration starts, busiest
	// first. An outlier here against its usual rate is the wake-storm
	// signal; loop_activity decomposes the why.
	TopWakers []LoopWakeRate `json:"top_wakers,omitempty"`
}

// LoopWakeRate is one loop's achieved trailing-day cadence.
type LoopWakeRate struct {
	Name         string `json:"name"`
	WakesLast24h int    `json:"wakes_last_24h"`
}

// maxCensusDegradedNames caps the degraded-loop name list on the census.
const maxCensusDegradedNames = 10

// maxCensusTopWakers caps the busiest-waker ranking.
const maxCensusTopWakers = 5

// QueueDepth is one work-queue partition's live backlog.
type QueueDepth struct {
	Consumer  string `json:"consumer"`
	Pending   int    `json:"pending"`
	OldestAge string `json:"oldest_age,omitempty"` // duration, e.g. "42m"
}

// TelemetryRollup is the 24h operational summary drawn from the
// telemetry collector.
type TelemetryRollup struct {
	Requests24h  int              `json:"requests_24h"`
	Errors24h    int              `json:"errors_24h"`
	LatencyP50Ms int              `json:"latency_p50_ms,omitempty"`
	LatencyP95Ms int              `json:"latency_p95_ms,omitempty"`
	DBSizesBytes map[string]int64 `json:"db_sizes_bytes,omitempty"`
}

// VersionInfo is the precomputed deploy story: what is running, what
// ran before it, when the boundary landed, and how big the jump was —
// so no model ever bookkeeps a version to detect a release. BootsLast24h
// rides along because restarts and deploys explain each other: boots
// piling up WITHOUT a version change is a crash loop, not a deploy.
type VersionInfo struct {
	Running string `json:"running"`
	Commit  string `json:"commit,omitempty"`
	// Previous is the last different version in boot history, and
	// ChangedDelta is when the boot that introduced Running happened.
	// Both empty when boot history shows no boundary (or is absent).
	Previous     string `json:"previous,omitempty"`
	ChangedDelta string `json:"changed_delta,omitempty"`
	// Change classifies the boundary: "patch", "minor", "major", or
	// "dev" when either side is not a semantic version.
	Change       string `json:"change,omitempty"`
	BootsLast24h int    `json:"boots_last_24h,omitempty"`
	// RecentBoots is the raw tail of the boot journal, newest first:
	// five boots minutes apart IS a crash loop, and a version change
	// between adjacent rows IS a deploy — no threshold required to see
	// either.
	RecentBoots []BootView `json:"recent_boots,omitempty"`
}

// BootView is one journaled process start, delta-formatted for the
// model.
type BootView struct {
	AtDelta string `json:"at_delta"`
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
}

// maxRecentBootViews caps the boot tail on the snapshot.
const maxRecentBootViews = 5

// bootStormThreshold is how many boots inside a day degrade the runtime
// lamp: a healthy instance restarts for deploys, not for sport.
const bootStormThreshold = 5

// HealthSnapshot is the whole annunciator panel in one shape, consumed
// identically by the system_health tool and the metacog context panel
// so the two can never drift.
type HealthSnapshot struct {
	Annunciator []HealthRow     `json:"annunciator"`
	Version     VersionInfo     `json:"version"`
	Host        HostInfo        `json:"host"`
	Loops       LoopCensus      `json:"loops"`
	Queues      []QueueDepth    `json:"queues,omitempty"`
	Telemetry   TelemetryRollup `json:"telemetry"`
}

// Degraded lists the annunciator rows that are not ok — the panel's
// summary line and the escalation trigger both read from this.
func (s HealthSnapshot) Degraded() []HealthRow {
	var rows []HealthRow
	for _, row := range s.Annunciator {
		if row.Status != HealthOK {
			rows = append(rows, row)
		}
	}
	return rows
}

// HealthSources are the live feeds the Inspector reads. Every field is
// optional (nil-safe): an unwired source simply contributes no rows, so
// the Inspector works identically in production, tests, and reduced
// configurations.
type HealthSources struct {
	// ConnStatus reports per-service reachability (connwatch).
	ConnStatus func() map[string]connwatch.ServiceStatus
	// MemGuard reports live memory against the guard's limits; ok=false
	// means the guard is not running (disabled by config).
	MemGuard func() (memguard.Reading, bool)
	// BusDropped is the operational event bus's cumulative dropped-
	// delivery count.
	BusDropped func() uint64
	// IndexStats reports log-index loss counters.
	IndexStats func() logging.IndexStats
	// SyncStates reports document-root remote sync state.
	SyncStates func() []checkout.SyncState
	// QueueStats reports live work-queue backlog per partition.
	QueueStats func(ctx context.Context) ([]loopqueue.ConsumerPending, error)
	// LoopStatuses snapshots the loop registry.
	LoopStatuses func() []looppkg.Status
	// Telemetry collects the 24h operational rollup.
	Telemetry *telemetry.Collector
	// BuildVersion and BuildCommit identify the running binary.
	BuildVersion string
	BuildCommit  string
	// BootHistory reads journaled process starts, newest first.
	BootHistory func(ctx context.Context) ([]BootRecord, error)
	// DataDir is the disk-probe target (thane's data directory).
	DataDir string
	// StartedAt is process start, for uptime.
	StartedAt time.Time
}

// Inspector assembles HealthSnapshots from the wired sources. It is the
// single source of truth for internal-operations health; every
// model-facing surface renders from its output.
type Inspector struct {
	src HealthSources
	now func() time.Time
}

// NewInspector builds an Inspector over src.
func NewInspector(src HealthSources) *Inspector {
	return &Inspector{src: src, now: time.Now}
}

// Health assembles the annunciator panel. It never fails: a source that
// errors becomes a degraded row carrying the error text, because "the
// probe broke" is itself a health finding.
func (i *Inspector) Health(ctx context.Context) HealthSnapshot {
	now := i.now()
	snap := HealthSnapshot{}

	// External connections, one lamp per watched service, name-sorted.
	if i.src.ConnStatus != nil {
		statuses := i.src.ConnStatus()
		names := make([]string, 0, len(statuses))
		for name := range statuses {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			st := statuses[name]
			row := HealthRow{Name: "conn:" + name, Status: HealthOK}
			if !st.LastCheck.IsZero() {
				row.LastCheck = promptfmt.FormatDeltaOnly(st.LastCheck, now)
			}
			if !st.Ready {
				row.Status = HealthFailed
				row.Detail = st.LastError
				if row.Detail == "" {
					row.Detail = "not ready"
				}
			}
			snap.Annunciator = append(snap.Annunciator, row)
		}
	}

	// Memory guard: ok below soft, degraded past soft, failed once the
	// hard limit tripped. Reported as disabled (still ok) when unwired —
	// an off guard is a config choice, not an incident.
	if i.src.MemGuard != nil {
		if reading, running := i.src.MemGuard(); running {
			row := HealthRow{
				Name:   "memory_guard",
				Status: HealthOK,
				Detail: fmt.Sprintf("%dMB in use of %dMB soft / %dMB hard", reading.CurrentMB, reading.SoftMB, reading.HardMB),
			}
			switch {
			case reading.Tripped:
				row.Status = HealthFailed
				row.Detail = fmt.Sprintf("hard limit tripped at %dMB; restart pending or imminent", reading.HardMB)
			case reading.SoftMB > 0 && reading.CurrentMB >= reading.SoftMB:
				row.Status = HealthDegraded
				row.Detail = fmt.Sprintf("%dMB in use exceeds the %dMB soft limit (hard limit %dMB)", reading.CurrentMB, reading.SoftMB, reading.HardMB)
			}
			snap.Annunciator = append(snap.Annunciator, row)
		} else {
			snap.Annunciator = append(snap.Annunciator, HealthRow{
				Name: "memory_guard", Status: HealthOK, Detail: "disabled by config",
			})
		}
	}

	// Event bus: any dropped delivery since boot degrades the row —
	// something was slow enough to lose observability data.
	if i.src.BusDropped != nil {
		row := HealthRow{Name: "event_bus", Status: HealthOK}
		if dropped := i.src.BusDropped(); dropped > 0 {
			row.Status = HealthDegraded
			row.Detail = fmt.Sprintf("%d subscriber deliveries dropped since boot", dropped)
		}
		snap.Annunciator = append(snap.Annunciator, row)
	}

	// Log index: dropped records or write errors mean logs_query
	// completeness degraded.
	if i.src.IndexStats != nil {
		stats := i.src.IndexStats()
		row := HealthRow{Name: "log_index", Status: HealthOK}
		if stats.DroppedRecords > 0 || stats.WriteErrors > 0 {
			row.Status = HealthDegraded
			row.Detail = fmt.Sprintf("%d records dropped, %d write errors since boot", stats.DroppedRecords, stats.WriteErrors)
		}
		snap.Annunciator = append(snap.Annunciator, row)
	}

	// Document-root sync: clean/fast-forwarded/pushed are healthy;
	// diverged/blocked/remote_behind degrade; an errored pass fails.
	if i.src.SyncStates != nil {
		for _, st := range i.src.SyncStates() {
			row := HealthRow{Name: "doc_sync:" + st.Name, Status: HealthOK}
			if !st.LastSyncAt.IsZero() {
				row.LastCheck = promptfmt.FormatDeltaOnly(st.LastSyncAt, now)
			}
			switch {
			case !st.OK:
				row.Status = HealthFailed
				row.Detail = st.Detail
			case st.Outcome == provenance.SyncDiverged, st.Outcome == provenance.SyncBlocked, st.Outcome == provenance.SyncRemoteBehind:
				row.Status = HealthDegraded
				row.Detail = fmt.Sprintf("%s: %s", st.Outcome, st.Detail)
			}
			snap.Annunciator = append(snap.Annunciator, row)
		}
	}

	// Work queue backlog: a partition whose oldest pending item has aged
	// past the threshold degrades the row — the consumer is not keeping up.
	if i.src.QueueStats != nil {
		row := HealthRow{Name: "queue_backlog", Status: HealthOK}
		stats, err := i.src.QueueStats(ctx)
		if err != nil {
			row.Status = HealthDegraded
			row.Detail = fmt.Sprintf("backlog probe failed: %v", err)
		} else {
			var stale []string
			for _, cp := range stats {
				depth := QueueDepth{Consumer: cp.Consumer, Pending: cp.Pending}
				if !cp.OldestEnqueuedAt.IsZero() {
					age := now.Sub(cp.OldestEnqueuedAt)
					depth.OldestAge = promptfmt.FormatDuration(age.Round(time.Second))
					if age > queueBacklogStaleAfter {
						stale = append(stale, fmt.Sprintf("%s (oldest %s)", cp.Consumer, depth.OldestAge))
					}
				}
				snap.Queues = append(snap.Queues, depth)
			}
			if len(stale) > 0 {
				row.Status = HealthDegraded
				row.Detail = fmt.Sprintf("pending work older than %s in: %v", promptfmt.FormatDuration(queueBacklogStaleAfter), stale)
			}
		}
		snap.Annunciator = append(snap.Annunciator, row)
	}

	// Loop fleet: the census plus one lamp that degrades when any loop
	// is errored.
	if i.src.LoopStatuses != nil {
		statuses := i.src.LoopStatuses()
		snap.Loops = buildLoopCensus(statuses)
		row := HealthRow{Name: "loops", Status: HealthOK,
			Detail: fmt.Sprintf("%d loops in the fleet, none degraded", snap.Loops.Total)}
		if snap.Loops.Degraded > 0 {
			row.Status = HealthDegraded
			row.Detail = fmt.Sprintf("%d of %d loops degraded: %v", snap.Loops.Degraded, snap.Loops.Total, snap.Loops.DegradedLoops)
			if snap.Loops.DegradedTruncated {
				row.Detail += fmt.Sprintf(" (+%d more)", snap.Loops.Degraded-len(snap.Loops.DegradedLoops))
			}
		}
		snap.Annunciator = append(snap.Annunciator, row)
	}

	snap.Version = i.collectVersionInfo(ctx, now)
	if snap.Version.BootsLast24h > bootStormThreshold {
		snap.Annunciator = append(snap.Annunciator, HealthRow{
			Name:   "runtime",
			Status: HealthDegraded,
			Detail: fmt.Sprintf("%d boots in the last 24h suggests a crash loop (running %s)", snap.Version.BootsLast24h, snap.Version.Running),
		})
	} else if i.src.BuildVersion != "" {
		snap.Annunciator = append(snap.Annunciator, HealthRow{
			Name: "runtime", Status: HealthOK,
			Detail: fmt.Sprintf("running %s", snap.Version.Running),
		})
	}

	snap.Host = collectHostInfo(i.src.DataDir, i.src.StartedAt, now)
	if i.src.Telemetry != nil {
		metrics := i.src.Telemetry.Collect(ctx)
		snap.Telemetry = TelemetryRollup{
			Requests24h:  metrics.Requests24h,
			Errors24h:    metrics.Errors24h,
			LatencyP50Ms: int(metrics.LatencyP50Ms),
			LatencyP95Ms: int(metrics.LatencyP95Ms),
			DBSizesBytes: metrics.DBSizes,
		}
	}
	return snap
}

// collectVersionInfo assembles the deploy story from build identity and
// the journaled boot history: walk newest-first past the boots that
// share the running version — the oldest of those is the boundary boot —
// until the first differing version, which is what ran before.
func (i *Inspector) collectVersionInfo(ctx context.Context, now time.Time) VersionInfo {
	info := VersionInfo{Running: i.src.BuildVersion, Commit: i.src.BuildCommit}
	if i.src.BootHistory == nil {
		return info
	}
	boots, err := i.src.BootHistory(ctx)
	if err != nil || len(boots) == 0 {
		return info
	}
	var boundary time.Time
	for _, boot := range boots {
		if !boot.At.Before(now.Add(-24 * time.Hour)) {
			info.BootsLast24h++
		}
		if len(info.RecentBoots) < maxRecentBootViews {
			view := BootView{AtDelta: promptfmt.FormatDeltaOnly(boot.At, now), Version: boot.Version}
			if len(boot.Commit) > 7 {
				view.Commit = boot.Commit[:7]
			} else {
				view.Commit = boot.Commit
			}
			info.RecentBoots = append(info.RecentBoots, view)
		}
		if info.Previous == "" {
			if boot.Version == info.Running {
				boundary = boot.At
			} else {
				info.Previous = boot.Version
				if !boundary.IsZero() {
					info.ChangedDelta = promptfmt.FormatDeltaOnly(boundary, now)
				}
				info.Change = classifyVersionChange(info.Previous, info.Running)
			}
		}
	}
	return info
}

// classifyVersionChange names the size of a version jump: patch, minor,
// major — or "dev" when either side is not a semantic version, which is
// itself information (an untagged build shipped).
func classifyVersionChange(prev, curr string) string {
	prevParts, prevOK := semverParts(prev)
	currParts, currOK := semverParts(curr)
	if !prevOK || !currOK {
		return "dev"
	}
	switch {
	case prevParts[0] != currParts[0]:
		return "major"
	case prevParts[1] != currParts[1]:
		return "minor"
	case prevParts[2] != currParts[2]:
		return "patch"
	default:
		return ""
	}
}

func semverParts(v string) ([3]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		v = v[:idx]
	}
	fields := strings.Split(v, ".")
	if len(fields) != 3 {
		return [3]int{}, false
	}
	var parts [3]int
	for idx, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil {
			return [3]int{}, false
		}
		parts[idx] = n
	}
	return parts, true
}

// buildLoopCensus rolls the registry snapshot into totals. Degraded
// matches loop_status's own definition: consecutive errors or an error
// state.
func buildLoopCensus(statuses []looppkg.Status) LoopCensus {
	census := LoopCensus{Total: len(statuses)}
	for _, st := range statuses {
		state := string(st.State)
		if state == "" {
			state = "running"
		}
		if census.ByState == nil {
			census.ByState = make(map[string]int)
		}
		census.ByState[state]++
		if st.ConsecutiveErrors > 0 || st.State == looppkg.StateError {
			census.Degraded++
			if len(census.DegradedLoops) < maxCensusDegradedNames {
				census.DegradedLoops = append(census.DegradedLoops, st.Name)
			} else {
				census.DegradedTruncated = true
			}
		}
		if st.WakesLast24h > 0 {
			census.TopWakers = append(census.TopWakers, LoopWakeRate{Name: st.Name, WakesLast24h: st.WakesLast24h})
		}
	}
	sort.SliceStable(census.TopWakers, func(i, j int) bool {
		if census.TopWakers[i].WakesLast24h != census.TopWakers[j].WakesLast24h {
			return census.TopWakers[i].WakesLast24h > census.TopWakers[j].WakesLast24h
		}
		return census.TopWakers[i].Name < census.TopWakers[j].Name
	})
	if len(census.TopWakers) > maxCensusTopWakers {
		census.TopWakers = census.TopWakers[:maxCensusTopWakers]
	}
	return census
}
