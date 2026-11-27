package introspection

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

func TestQueueSetAsideRow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		counts     []loopqueue.ConsumerSetAside
		err        error
		wantStatus string
		wantDetail []string
	}{
		{name: "none", wantStatus: HealthOK},
		{
			name:       "recent",
			counts:     []loopqueue.ConsumerSetAside{{Consumer: "signal/sender-1a2b", Count: 2, NewestSetAsideAt: time.Now().Add(-3 * time.Hour)}},
			wantStatus: HealthDegraded,
			wantDetail: []string{"2 queued item(s)", "signal/sender-1a2b: 2 (newest 3h ago)", "pattern \"set aside\""},
		},
		{name: "probe error", err: errors.New("disk"), wantStatus: HealthDegraded, wantDetail: []string{"set-aside probe failed: disk"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotSince time.Time
			insp := NewInspector(HealthSources{QueueSetAside: func(_ context.Context, since time.Time) ([]loopqueue.ConsumerSetAside, error) {
				gotSince = since
				return tt.counts, tt.err
			}})
			row := rowByName(t, insp.Health(context.Background()), "queue_set_aside")
			if row.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q (%+v)", row.Status, tt.wantStatus, row)
			}
			for _, want := range tt.wantDetail {
				if !strings.Contains(row.Detail, want) {
					t.Errorf("detail %q lacks %q", row.Detail, want)
				}
			}
			if age := time.Since(gotSince); age < 23*time.Hour || age > 25*time.Hour {
				t.Errorf("probe since = %v ago, want the 24h window", age)
			}
		})
	}
}
