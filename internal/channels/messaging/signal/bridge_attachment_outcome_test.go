package signal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/attachments"
)

// countingVisionAnalyzer returns a fixed outcome and counts calls.
type countingVisionAnalyzer struct {
	description string
	err         error
	calls       int
}

func (c *countingVisionAnalyzer) Analyze(context.Context, *attachments.Record) (string, error) {
	c.calls++
	return c.description, c.err
}

// sentinelVisionCallError is a vision call failure shaped like the
// production one: a provider 404 whose body and a path in it must never
// reach the model.
func sentinelVisionCallError(status int) error {
	provider := fmt.Errorf(`API error %d: {"error":"SENTINEL-BODY at /SENTINEL/path"}`, status)
	return llm.WithErrorClass(attachments.ErrVisionCall, fmt.Errorf("vision: analyze rec-1: %w", provider))
}

var attachmentIDField = regexp.MustCompile(`, attachment_id=[0-9a-f-]{36},`)

// TestAttachmentLinesAlwaysSayWhatCameWithTheMessage checks that every
// attachment line is followed by exactly one outcome line: the vision
// description, or a line saying no description came and why. A bare
// attachment line is what let a model describe a photo it never saw.
func TestAttachmentLinesAlwaysSayWhatCameWithTheMessage(t *testing.T) {
	const notSeen = "[Image not seen: no description came with this message because "
	tests := []struct {
		name        string
		contentType string
		analyzer    *countingVisionAnalyzer // nil: vision disabled
		noStore     bool                    // legacy path, no attachment store
		maxSize     int64
		noSourceDir bool
		missingFile bool
		wantStored  bool   // the line carries attachment_id
		wantNote    string // text the attachment line itself must carry
		wantOutcome string
		wantCalls   int
	}{
		{
			name: "described", contentType: "image/jpeg",
			analyzer:   &countingVisionAnalyzer{description: "A dog on a porch."},
			wantStored: true, wantOutcome: "[Vision: A dog on a porch.]", wantCalls: 1,
		},
		{
			name: "vision disabled", contentType: "image/jpeg",
			wantStored: true, wantOutcome: notSeen + "vision analysis was not enabled.]",
		},
		{
			name: "vision model does not exist", contentType: "image/jpeg",
			analyzer:    &countingVisionAnalyzer{err: sentinelVisionCallError(404)},
			wantStored:  true,
			wantOutcome: notSeen + "the vision model call failed (the provider reported that the requested model does not exist).]",
			wantCalls:   1,
		},
		{
			name: "vision timed out", contentType: "image/png",
			analyzer:    &countingVisionAnalyzer{err: llm.WithErrorClass(attachments.ErrVisionCall, context.DeadlineExceeded)},
			wantStored:  true,
			wantOutcome: notSeen + "the vision model call failed (the provider did not answer in time).]",
			wantCalls:   1,
		},
		{
			name: "image too large for vision", contentType: "image/jpeg",
			analyzer:    &countingVisionAnalyzer{err: fmt.Errorf("vision: image rec-1 is big: %w", attachments.ErrImageTooLarge)},
			wantStored:  true,
			wantOutcome: notSeen + "the image is over the 20 MiB limit for vision analysis.]",
			wantCalls:   1,
		},
		{
			name: "empty description with no error", contentType: "image/jpeg",
			analyzer:    &countingVisionAnalyzer{},
			wantStored:  true,
			wantOutcome: notSeen + "the vision model returned an empty description.]",
			wantCalls:   1,
		},
		{
			name: "stored image unreadable", contentType: "image/jpeg",
			analyzer:    &countingVisionAnalyzer{err: llm.WithErrorClass(attachments.ErrImageUnreadable, errors.New("vision: read image /SENTINEL/path: permission denied"))},
			wantStored:  true,
			wantOutcome: notSeen + "the stored image could not be read for analysis.]",
			wantCalls:   1,
		},
		{
			name: "unclassified analysis failure", contentType: "image/jpeg",
			analyzer:    &countingVisionAnalyzer{err: errors.New("vision: cache /SENTINEL/path: database is locked")},
			wantStored:  true,
			wantOutcome: notSeen + "the analysis failed.]",
			wantCalls:   1,
		},
		{
			name: "audio", contentType: "audio/aac",
			analyzer:   &countingVisionAnalyzer{description: "never asked"},
			wantStored: true, wantOutcome: "[Audio not heard: no transcript came with this message.]",
		},
		{
			name: "video", contentType: "video/mp4",
			analyzer:   &countingVisionAnalyzer{description: "never asked"},
			wantStored: true, wantOutcome: "[Video not seen: no description came with this message.]",
		},
		{
			name: "document", contentType: "application/pdf",
			analyzer:   &countingVisionAnalyzer{description: "never asked"},
			wantStored: true, wantOutcome: "[File not read: no account of its contents came with this message.]",
		},
		{
			name: "over the bridge's size limit", contentType: "image/jpeg",
			analyzer: &countingVisionAnalyzer{description: "never asked"},
			maxSize:  1, wantNote: "exceeds size limit",
			wantOutcome: notSeen + "it was not analyzed.]",
		},
		{
			name: "source dir not configured", contentType: "image/jpeg",
			analyzer:    &countingVisionAnalyzer{description: "never asked"},
			noSourceDir: true, wantNote: "source dir not configured",
			wantOutcome: notSeen + "it was not analyzed.]",
		},
		{
			name: "source file missing", contentType: "image/jpeg",
			analyzer:    &countingVisionAnalyzer{description: "never asked"},
			missingFile: true, wantNote: "file not available",
			wantOutcome: notSeen + "it was not analyzed.]",
		},
		{
			name: "legacy path without dirs", contentType: "image/jpeg",
			noStore: true, noSourceDir: true,
			wantOutcome: notSeen + "it was not analyzed.]",
		},
		{
			name: "legacy audio", contentType: "audio/aac",
			noStore: true, noSourceDir: true,
			wantOutcome: "[Audio not heard: no transcript came with this message.]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source")
			if err := os.MkdirAll(source, 0o750); err != nil {
				t.Fatal(err)
			}
			if !tt.missingFile {
				if err := os.WriteFile(filepath.Join(source, "att-1"), []byte("attachment bytes"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := BridgeConfig{
				Client: &Client{}, Runner: &testRunner{},
				Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
				Attachments: AttachmentConfig{SourceDir: source, MaxSize: tt.maxSize},
			}
			if tt.noSourceDir {
				cfg.Attachments.SourceDir = ""
			}
			if !tt.noStore {
				cfg.AttachmentStore = newTestAttachmentStore(t, filepath.Join(dir, "attachments.db"), filepath.Join(dir, "store"))
			}
			if tt.analyzer != nil {
				cfg.VisionAnalyzer = tt.analyzer
			}
			bridge := NewBridge(cfg)

			descs := bridge.processAttachments(context.Background(), []Attachment{
				{ID: "att-1", ContentType: tt.contentType, Filename: "f", Size: 16},
			}, "+15551234567", "signal-15551234567", time.Now())

			if len(descs) != 1 {
				t.Fatalf("descriptions = %d, want 1", len(descs))
			}
			lines := strings.Split(descs[0], "\n")
			if len(lines) != 2 {
				t.Fatalf("want an attachment line and one outcome line, got %q", descs[0])
			}
			if !strings.HasPrefix(lines[0], "[Attachment: "+tt.contentType) {
				t.Errorf("attachment line = %q", lines[0])
			}
			if got := attachmentIDField.MatchString(lines[0]); got != tt.wantStored {
				t.Errorf("attachment line carries attachment_id = %v, want %v: %q", got, tt.wantStored, lines[0])
			}
			if tt.wantNote != "" && !strings.Contains(lines[0], tt.wantNote) {
				t.Errorf("attachment line %q lacks its note %q", lines[0], tt.wantNote)
			}
			if lines[1] != tt.wantOutcome {
				t.Errorf("outcome line:\n got %q\nwant %q", lines[1], tt.wantOutcome)
			}
			if strings.Contains(descs[0], "SENTINEL") {
				t.Errorf("an error's own text reached the model: %q", descs[0])
			}
			if tt.analyzer != nil && tt.analyzer.calls != tt.wantCalls {
				t.Errorf("vision calls = %d, want %d", tt.analyzer.calls, tt.wantCalls)
			}
		})
	}
}

// TestSignalTurnCarriesImageNotSeen checks the outcome line reaches the
// user message a turn is built from, which is also what is stored and
// replayed on later turns.
func TestSignalTurnCarriesImageNotSeen(t *testing.T) {
	const sender = "+15551234567"
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.MkdirAll(source, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "image-id"), []byte("image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	bridge := NewBridge(BridgeConfig{
		Client: &Client{}, Runner: &testRunner{},
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		Attachments:     AttachmentConfig{SourceDir: source},
		AttachmentStore: newTestAttachmentStore(t, filepath.Join(dir, "attachments.db"), filepath.Join(dir, "store")),
		VisionAnalyzer:  &countingVisionAnalyzer{err: sentinelVisionCallError(404)},
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
	if err != nil || turn == nil {
		t.Fatalf("buildSignalTurn = %+v, %v", turn, err)
	}
	var content strings.Builder
	for _, m := range turn.Request.Messages {
		content.WriteString(m.Content)
	}
	want := "[Image not seen: no description came with this message because the vision model call failed (the provider reported that the requested model does not exist).]"
	if !strings.Contains(content.String(), want) {
		t.Errorf("turn messages lack %q:\n%s", want, content.String())
	}
	if strings.Contains(content.String(), "SENTINEL") {
		t.Errorf("an error's own text reached the turn:\n%s", content.String())
	}
}

// TestRetriedSignalTurnReusesItsAttachmentRecords checks that building a
// turn again for the same message, as the mailbox retry does after a
// failed turn, reuses the attachment record the first attempt stored:
// the line carries the same attachment_id and the store holds one row.
func TestRetriedSignalTurnReusesItsAttachmentRecords(t *testing.T) {
	const sender = "+15551234567"
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.MkdirAll(source, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "image-id"), []byte("image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newTestAttachmentStore(t, filepath.Join(dir, "attachments.db"), filepath.Join(dir, "store"))
	bridge := NewBridge(BridgeConfig{
		Client: &Client{}, Runner: &testRunner{},
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		Attachments:     AttachmentConfig{SourceDir: source},
		AttachmentStore: store,
		VisionAnalyzer:  &countingVisionAnalyzer{err: sentinelVisionCallError(503)},
	})
	payload, err := json.Marshal(Envelope{
		Source: sender, Timestamp: 1794960952450,
		DataMessage: &DataMessage{Timestamp: 1794960952450, Attachments: []Attachment{
			{ID: "image-id", Filename: "photo.jpg", ContentType: "image/jpeg"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var ids []string
	for attempt := 0; attempt < 3; attempt++ {
		turn, err := bridge.buildSignalTurn(context.Background(), sender, loop.TurnInput{
			MailboxItems: []loop.MailboxItem{{Payload: payload}},
		})
		if err != nil || turn == nil {
			t.Fatalf("attempt %d: buildSignalTurn = %+v, %v", attempt, turn, err)
		}
		var content strings.Builder
		for _, m := range turn.Request.Messages {
			content.WriteString(m.Content)
		}
		ids = append(ids, attachmentIDField.FindString(content.String()))
	}
	if ids[0] == "" || ids[1] != ids[0] || ids[2] != ids[0] {
		t.Errorf("attachment ids across attempts = %q, want one id reused", ids)
	}
	total, _, _, err := store.TelemetryStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Errorf("attachment rows after three attempts = %d, want 1", total)
	}
}
