package tools

import (
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// TestLoopStatusHealthUnpublishedWrite: a loop whose wake ended without
// landing its durable write reads degraded on loop_status even though no
// error counter moved, with a line naming the tool, the rejection count,
// and the time. The introspection census pins the same literal line, so
// the two health rollups are held to one wording.
func TestLoopStatusHealthUnpublishedWrite(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	statuses := []looppkg.Status{
		{Name: "quiet", State: looppkg.StateSleeping},
		{Name: "utah_trip_curator", State: looppkg.StateWaiting, UnpublishedWrites: []looppkg.UnpublishedWrite{{
			Tool:     "publish_output_utah_trip_curator",
			Failures: 18,
			At:       now.Add(-2 * time.Hour),
		}}},
	}
	h := loopStatusHealth(statuses, now)

	if h["degraded"] != 1 {
		t.Errorf("degraded = %v, want 1", h["degraded"])
	}
	want := "utah_trip_curator (wake at -2h ended without publishing publish_output_utah_trip_curator after 18 rejections)"
	dl, _ := h["degraded_loops"].([]string)
	if len(dl) != 1 || dl[0] != want {
		t.Errorf("degraded_loops = %q, want [%q]", dl, want)
	}
}
