package introspection

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/events"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// unpublishedLine is the exact line loop_status's health rollup emits for
// a loop whose only problem is an unpublished write; the tools package
// pins the same literal, so the two rollups are held to one wording.
const unpublishedLine = "utah_trip_curator (wake at -2h ended without publishing publish_output_utah_trip_curator after 18 rejections)"

func unpublishedFleet(now time.Time) []looppkg.Status {
	return []looppkg.Status{
		{Name: "alpha", State: looppkg.StateSleeping},
		{Name: "utah_trip_curator", State: looppkg.StateWaiting, UnpublishedWrites: []looppkg.UnpublishedWrite{{
			Tool:      "publish_output_utah_trip_curator",
			Failures:  18,
			LastError: "status_line is 190 characters and the limit is 160",
			At:        now.Add(-2 * time.Hour),
		}}},
		{Name: "broken", State: looppkg.StateError, ConsecutiveErrors: 2},
	}
}

// TestLoopCensusUnpublishedWrite: a loop holding an unpublished write is
// degraded in the census exactly as loop_status's rollup reads it, with
// the same line naming the tool, the rejection count, and the time.
func TestLoopCensusUnpublishedWrite(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	statuses := unpublishedFleet(now)
	census := buildLoopCensus(statuses, now)

	if census.Degraded != 2 {
		t.Errorf("degraded = %d, want 2 (unpublished + errored)", census.Degraded)
	}
	if strings.Join(census.DegradedLoops, ",") != "utah_trip_curator,broken" {
		t.Errorf("degraded_loops = %v", census.DegradedLoops)
	}
	if len(census.UnpublishedWrites) != 1 || census.UnpublishedWrites[0] != unpublishedLine {
		t.Fatalf("unpublished_writes = %q, want [%q]", census.UnpublishedWrites, unpublishedLine)
	}
	// Lockstep: loop_status renders "name (DegradedReason)", and for this
	// loop that is the census line verbatim.
	if lockstep := fmt.Sprintf("%s (%s)", statuses[1].Name, looppkg.DegradedReason(statuses[1], now)); lockstep != unpublishedLine {
		t.Errorf("loop_status line %q diverges from census line %q", lockstep, unpublishedLine)
	}
}

// TestHealthLoopsRowNamesUnpublishedWrite: the loops lamp — what the
// metacog panel and system_health lead with — degrades and names the
// unpublished write, not only the loop.
func TestHealthLoopsRowNamesUnpublishedWrite(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	inspector := NewInspector(HealthSources{
		LoopStatuses: func() []looppkg.Status { return unpublishedFleet(now)[:2] },
	})
	inspector.now = func() time.Time { return now }
	snap := inspector.Health(context.Background())

	var row *HealthRow
	for i := range snap.Annunciator {
		if snap.Annunciator[i].Name == "loops" {
			row = &snap.Annunciator[i]
		}
	}
	if row == nil {
		t.Fatal("no loops annunciator row")
	}
	if row.Status != HealthDegraded {
		t.Errorf("loops row status = %v, want degraded", row.Status)
	}
	if !strings.Contains(row.Detail, unpublishedLine) {
		t.Errorf("loops row detail = %q, want it to name %q", row.Detail, unpublishedLine)
	}
}

// TestProjectLoopEventKeepsWriteOutcomeScalars: the journal keeps the
// wake's durable-write outcome, the only record of an unpublished wake
// that survives a restart.
func TestProjectLoopEventKeepsWriteOutcomeScalars(t *testing.T) {
	row, ok := projectLoopEvent(events.Event{
		Kind: events.KindLoopIterationComplete,
		Data: map[string]any{
			"loop_id":            "loop-1",
			"loop_name":          "utah_trip_curator",
			"write_rejections":   18,
			"unpublished_writes": 1,
			"tools_used":         map[string]int{"publish_output_utah_trip_curator": 16},
		},
	})
	if !ok {
		t.Fatal("iteration_complete must be persisted")
	}
	if row.Detail["write_rejections"] != 18 || row.Detail["unpublished_writes"] != 1 {
		t.Errorf("detail = %v, want write_rejections=18 unpublished_writes=1", row.Detail)
	}
	if _, kept := row.Detail["tools_used"]; kept {
		t.Error("bulk tools_used must stay out of the journal")
	}
}
