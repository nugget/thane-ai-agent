package signal

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/attachments"
)

// panickingVisionAnalyzer stands in for any defect below turn
// preparation that panics instead of returning an error.
type panickingVisionAnalyzer struct{}

func (panickingVisionAnalyzer) Analyze(context.Context, *attachments.Record) (string, error) {
	panic("vision analyzer exploded")
}

// TestBuildSignalTurnRecoversPanics checks that a panic while preparing
// a Signal turn comes back as an error, so the sender loop keeps its
// mailbox items for a retry instead of the process ending, and that the
// panic and its stack are logged.
func TestBuildSignalTurnRecoversPanics(t *testing.T) {
	const sender = "+15551234567"
	tests := []struct {
		name      string
		analyzer  VisionAnalyzer
		wantPanic string // empty: the turn builds normally
	}{
		{
			name:     "no analyzer",
			analyzer: nil,
		},
		{
			// The shape that crashed production: vision disabled left a
			// nil *attachments.Analyzer inside a non-nil interface.
			name:      "nil analyzer pointer inside the interface",
			analyzer:  (*attachments.Analyzer)(nil),
			wantPanic: "nil pointer dereference",
		},
		{
			name:      "analyzer that panics",
			analyzer:  panickingVisionAnalyzer{},
			wantPanic: "vision analyzer exploded",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source")
			if err := os.MkdirAll(source, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "image-id"), []byte("image bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			store := newTestAttachmentStore(t, filepath.Join(dir, "attachments.db"), filepath.Join(dir, "store"))
			var logs syncBuffer
			bridge := NewBridge(BridgeConfig{
				Client:          &Client{},
				Runner:          &testRunner{},
				Logger:          slog.New(slog.NewTextHandler(&logs, nil)),
				Attachments:     AttachmentConfig{SourceDir: source},
				AttachmentStore: store,
				VisionAnalyzer:  tt.analyzer,
			})
			payload, err := json.Marshal(Envelope{
				Source: sender, Timestamp: time.Now().UnixMilli(),
				DataMessage: &DataMessage{Attachments: []Attachment{
					{ID: "image-id", Filename: "photo.jpg", ContentType: "image/jpeg"},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}

			turn, err := bridge.buildSignalTurn(context.Background(), sender, loop.TurnInput{
				MailboxItems: []loop.MailboxItem{{Payload: payload}},
			})

			if tt.wantPanic == "" {
				if err != nil || turn == nil {
					t.Fatalf("buildSignalTurn = %+v, %v; want a turn", turn, err)
				}
				if strings.Contains(logs.String(), "panicked") {
					t.Errorf("logged a panic for a clean turn:\n%s", logs.String())
				}
				return
			}
			if err == nil || turn != nil {
				t.Fatalf("buildSignalTurn = %+v, %v; want no turn and an error", turn, err)
			}
			if !strings.Contains(err.Error(), "signal turn preparation panicked") || !strings.Contains(err.Error(), tt.wantPanic) {
				t.Errorf("error = %q, want it to say turn preparation panicked with %q", err, tt.wantPanic)
			}
			got := logs.String()
			for _, want := range []string{"level=ERROR", `msg="signal turn preparation panicked"`, "sender=" + sender, tt.wantPanic, "stack=", "buildSignalTurn"} {
				if !strings.Contains(got, want) {
					t.Errorf("panic log missing %q:\n%s", want, got)
				}
			}
		})
	}
}
