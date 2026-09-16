package signal

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/attachments"
)

type attributedVisionClient struct{}

func (attributedVisionClient) Chat(context.Context, string, []llm.Message, []map[string]any) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{
		Message: llm.Message{Role: "assistant", Content: "A wooded trail."}, InputTokens: 100, OutputTokens: 10,
	}, nil
}

func (c attributedVisionClient) ChatStream(ctx context.Context, model string, messages []llm.Message, tools []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	return c.Chat(ctx, model, messages, tools)
}

func (attributedVisionClient) Ping(context.Context) error { return nil }

func TestSignalTurnVisionUsageAttribution(t *testing.T) {
	const sender = "+15551234567"
	const conversation = "signal-15551234567"
	for _, tt := range []struct {
		name           string
		conversationID string
		wantSessionID  string
	}{
		{name: "pre-turn synthetic conversation", conversationID: "loop-sender-wake-1"},
		{name: "mid-turn channel conversation", conversationID: conversation, wantSessionID: "existing-session-id"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			db, err := database.OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			store, err := attachments.NewStore(db, t.TempDir(), logger)
			if err != nil {
				t.Fatal(err)
			}
			ledger, err := usage.NewStore(db, logger)
			if err != nil {
				t.Fatal(err)
			}
			client := usage.TrackingClient(attributedVisionClient{}, func(ctx context.Context, record usage.Record) {
				if err := ledger.Record(ctx, record); err != nil {
					t.Errorf("record vision usage: %v", err)
				}
			})
			analyzer := attachments.NewAnalyzer(store, attachments.AnalyzerConfig{Client: client, Model: "vision", Logger: logger})
			source := t.TempDir()
			if err := os.WriteFile(filepath.Join(source, "image-id"), []byte("image bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			bridge := NewBridge(BridgeConfig{
				Client: &Client{}, Runner: &testRunner{}, Logger: logger,
				Attachments: AttachmentConfig{SourceDir: source}, AttachmentStore: store, VisionAnalyzer: analyzer,
			})
			payload, err := json.Marshal(Envelope{
				Source: sender, Timestamp: time.Now().UnixMilli(),
				DataMessage: &DataMessage{Message: "Describe this image.", Attachments: []Attachment{
					{ID: "image-id", Filename: "trail.jpg", ContentType: "image/jpeg"},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			attribution := llm.Attribution{
				ConversationID: tt.conversationID, SessionID: "existing-session-id", RequestID: "responsible-request-id",
				LoopID: "sender-loop-id", LoopName: "signal-sender", ParentLoopID: "signal-parent-id",
			}
			ctx := llm.WithAttribution(context.Background(), attribution)
			// The loop prepares this channel turn before replacing its synthetic
			// conversation with the conversation returned in turn.Request.
			turn, err := bridge.buildSignalTurn(ctx, sender, loop.TurnInput{MailboxItems: []loop.MailboxItem{{Payload: payload}}})
			if err != nil || turn == nil {
				t.Fatalf("build signal turn = %+v, %v", turn, err)
			}
			if turn.Request.ConversationID != conversation {
				t.Fatalf("turn conversation = %q", turn.Request.ConversationID)
			}
			var got llm.Attribution
			if err := db.QueryRow(`SELECT conversation_id, session_id, request_id, loop_id, loop_name, parent_loop_id
				FROM usage_records`).Scan(&got.ConversationID, &got.SessionID, &got.RequestID, &got.LoopID, &got.LoopName, &got.ParentLoopID); err != nil {
				t.Fatal(err)
			}
			want := attribution
			want.ConversationID = conversation
			want.SessionID = tt.wantSessionID
			if got != want {
				t.Errorf("persisted pre-turn vision attribution = %+v, want %+v", got, want)
			}
			if got := llm.AttributionFromContext(ctx); got != attribution {
				t.Errorf("parent context changed: %+v", got)
			}
		})
	}
}
