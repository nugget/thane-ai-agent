package introspection

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/phasetrace"
)

// queueSetAsideWindow is how long after a work-queue item is set aside
// the queue_set_aside row stays degraded. The rows themselves are kept;
// the window bounds only the lamp.
const queueSetAsideWindow = 24 * time.Hour

// queueSetAsideRow reports items a consumer set aside after every
// delivery of them failed. Setting an item aside clears the queue
// backlog it held and the loop error streak it caused, so without this
// row nothing would show that an item was never handled.
func (i *Inspector) queueSetAsideRow(ctx context.Context, now time.Time) (HealthRow, bool) {
	if i.src.QueueSetAside == nil {
		return HealthRow{}, false
	}
	row := HealthRow{Name: "queue_set_aside", Status: HealthOK}
	done := phasetrace.Phase(ctx, "health:queue_set_aside")
	counts, err := i.src.QueueSetAside(ctx, now.Add(-queueSetAsideWindow))
	done()
	if err != nil {
		row.Status = HealthDegraded
		row.Detail = fmt.Sprintf("set-aside probe failed: %v", err)
		return row, true
	}
	if len(counts) == 0 {
		return row, true
	}
	total := 0
	parts := make([]string, 0, len(counts))
	for _, c := range counts {
		total += c.Count
		parts = append(parts, fmt.Sprintf("%s: %d (newest %s ago)", c.Consumer, c.Count,
			promptfmt.FormatDuration(now.Sub(c.NewestSetAsideAt).Round(time.Minute))))
	}
	row.Status = HealthDegraded
	row.Detail = fmt.Sprintf("%d queued item(s) were set aside in the last %s after every delivery failed to complete a turn, "+
		"so nothing answered them and nothing will retry them: %s. "+
		"logs_query with loop_name set to the partition and pattern \"set aside\" returns each item_id and, when the process survived "+
		"its failures, the last error; a failure that killed the process left its error in the logs just before a recent boot. "+
		"Only the operator can replay or discard them.",
		total, promptfmt.FormatDuration(queueSetAsideWindow), strings.Join(parts, "; "))
	return row, true
}
