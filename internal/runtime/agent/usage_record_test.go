package agent

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
)

// TestRecordUsage_WarnsOncePerUnpricedPaidModel guards the detector for
// a fleet move that outruns the pricing table: an unpriced Anthropic
// model records at $0, so it must say so — once, not on every call —
// while priced models and free local models stay quiet.
func TestRecordUsage_WarnsOncePerUnpricedPaidModel(t *testing.T) {
	const warnMsg = "usage cost not recorded: model has no pricing entry"
	pricing := map[string]config.PricingEntry{
		"claude-sonnet-5": {InputPerMillion: 2.0, OutputPerMillion: 10.0},
	}
	tests := []struct {
		name      string
		models    []string
		wantWarns int
	}{
		{"unpriced anthropic model warns once across repeated calls", []string{"claude-opus-5", "claude-opus-5", "claude-opus-5"}, 1},
		{"each unpriced anthropic model warns once", []string{"claude-opus-5", "claude-fable-5-1", "claude-opus-5"}, 2},
		{"priced anthropic model is silent", []string{"claude-sonnet-5", "claude-sonnet-5"}, 0},
		{"unpriced local model is silent", []string{"qwen3:32b", "qwen3:32b"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := database.OpenMemory()
			if err != nil {
				t.Fatalf("database.OpenMemory: %v", err)
			}
			t.Cleanup(func() { db.Close() })
			store, err := usage.NewStore(db, nil)
			if err != nil {
				t.Fatalf("usage.NewStore: %v", err)
			}

			var logBuf bytes.Buffer
			l := &Loop{
				logger:     slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})),
				usageStore: store,
				pricing:    pricing,
			}
			for _, model := range tt.models {
				l.recordUsage(context.Background(), &Request{}, model, 1000, 100, 0, 0, 0, 0, "conv", "session", "r_test", "")
			}

			if got := strings.Count(logBuf.String(), warnMsg); got != tt.wantWarns {
				t.Errorf("unpriced-model warnings = %d, want %d; log:\n%s", got, tt.wantWarns, logBuf.String())
			}
		})
	}
}
