package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/events"
	"github.com/nugget/thane-ai-agent/internal/messages"
)

func TestDatasetWriter_WriteRecordCreatesExpectedPartition(t *testing.T) {
	dir := t.TempDir()
	writer, err := OpenDatasetWriter(dir)
	if err != nil {
		t.Fatalf("OpenDatasetWriter() error = %v", err)
	}
	defer writer.Close()

	ts := time.Date(2026, time.April, 10, 15, 4, 5, 0, time.UTC)
	record := DatasetRecord{
		Timestamp: ts,
		Dataset:   DatasetEvents,
		Kind:      "startup",
		Payload:   map[string]any{"ready": true},
	}
	if err := writer.WriteRecord(record); err != nil {
		t.Fatalf("WriteRecord() error = %v", err)
	}

	segmentTime := ts.In(time.Local)
	path := filepath.Join(dir, DatasetEvents, segmentTime.Format(time.DateOnly), segmentTime.Format("15")+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("line count = %d, want 1", len(lines))
	}

	var got DatasetRecord
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got.Dataset != DatasetEvents {
		t.Errorf("dataset = %q, want %q", got.Dataset, DatasetEvents)
	}
	if got.Kind != "startup" {
		t.Errorf("kind = %q, want %q", got.Kind, "startup")
	}
	if got.EventID == "" {
		t.Error("event_id empty, want generated ID")
	}
}

func TestDatasetWriter_ConcurrentWritesProduceValidJSONL(t *testing.T) {
	dir := t.TempDir()
	writer, err := OpenDatasetWriter(dir)
	if err != nil {
		t.Fatalf("OpenDatasetWriter() error = %v", err)
	}
	defer writer.Close()

	const total = 64
	ts := time.Date(2026, time.April, 10, 16, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	errCh := make(chan error, total)
	for i := range total {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errCh <- writer.WriteRecord(DatasetRecord{
				Timestamp: ts,
				Dataset:   DatasetRequests,
				Kind:      "request_complete",
				Payload:   map[string]any{"index": i},
			})
		}(i)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("WriteRecord() error = %v", err)
		}
	}

	segmentTime := ts.In(time.Local)
	path := filepath.Join(dir, DatasetRequests, segmentTime.Format(time.DateOnly), segmentTime.Format("15")+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != total {
		t.Fatalf("line count = %d, want %d", len(lines), total)
	}
	for _, line := range lines {
		var got DatasetRecord
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("json.Unmarshal() error = %v", err)
		}
		if got.Dataset != DatasetRequests {
			t.Errorf("dataset = %q, want %q", got.Dataset, DatasetRequests)
		}
	}
}

func TestDatasetHandler_RoutesDatasetsAndStdout(t *testing.T) {
	dir := t.TempDir()
	writer, err := OpenDatasetWriter(dir)
	if err != nil {
		t.Fatalf("OpenDatasetWriter() error = %v", err)
	}
	defer writer.Close()

	var stdout bytes.Buffer
	handler := NewDatasetHandler(slog.NewJSONHandler(&stdout, nil), writer, DatasetHandlerOptions{
		DatasetLevel:    slog.LevelInfo,
		StdoutLevel:     slog.LevelInfo,
		StdoutEnabled:   true,
		EventsEnabled:   true,
		RequestsEnabled: true,
		AccessEnabled:   true,
	})
	logger := slog.New(handler)

	logger.Info("startup complete", "component", "bootstrap")
	logger.Info("llm call", "subsystem", SubsystemAgent, "request_id", "r_123", "kind", events.KindLLMCall)
	logger.Info("request handled", "kind", "http_access", "server", "api")
	logger.Warn("message envelope delivery failed", "component", "message_bus", "error", "boom")

	stdoutLines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(stdoutLines) != 2 {
		t.Fatalf("stdout lines = %d, want 2", len(stdoutLines))
	}
	if strings.Contains(stdout.String(), `"request_id":"r_123"`) {
		t.Error("stdout should not contain request dataset chatter")
	}
	if strings.Contains(stdout.String(), `"kind":"http_access"`) {
		t.Error("stdout should not contain access dataset chatter")
	}

	assertDatasetLineCount(t, dir, DatasetEvents, 1)
	assertDatasetLineCount(t, dir, DatasetRequests, 1)
	assertDatasetLineCount(t, dir, DatasetAccess, 1)
}

func TestDatasetHandler_SkipsDisabledDatasets(t *testing.T) {
	dir := t.TempDir()
	writer, err := OpenDatasetWriter(dir)
	if err != nil {
		t.Fatalf("OpenDatasetWriter() error = %v", err)
	}
	defer writer.Close()

	handler := NewDatasetHandler(nil, writer, DatasetHandlerOptions{
		DatasetLevel:    slog.LevelInfo,
		EventsEnabled:   true,
		RequestsEnabled: true,
		AccessEnabled:   false,
	})
	logger := slog.New(handler)
	logger.Info("request handled", "kind", "http_access", "server", "api")

	segmentTime := time.Now().In(time.Local)
	path := filepath.Join(dir, DatasetAccess, segmentTime.Format(time.DateOnly), segmentTime.Format("15")+".jsonl")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("access dataset path exists unexpectedly: %q", path)
	}
}

func TestDatasetRecordFromOperationalEvent(t *testing.T) {
	record, ok := DatasetRecordFromOperationalEvent(events.Event{
		Timestamp: time.Date(2026, time.April, 10, 17, 0, 0, 0, time.UTC),
		Source:    events.SourceDelegate,
		Kind:      events.KindComplete,
		Data: map[string]any{
			"delegate_id": "deleg_123",
			"ok":          false,
		},
	})
	if !ok {
		t.Fatal("DatasetRecordFromOperationalEvent() = false, want true")
	}
	if record.Dataset != DatasetDelegates {
		t.Errorf("dataset = %q, want %q", record.Dataset, DatasetDelegates)
	}
	if record.DelegateID != "deleg_123" {
		t.Errorf("delegate_id = %q, want %q", record.DelegateID, "deleg_123")
	}
	if record.Severity != "WARN" {
		t.Errorf("severity = %q, want %q", record.Severity, "WARN")
	}
}

func TestDatasetRecordFromEnvelopeAudit(t *testing.T) {
	now := time.Date(2026, time.April, 10, 18, 0, 0, 0, time.UTC)
	env := messages.Envelope{
		ID:   "env_123",
		Type: messages.TypeSignal,
		From: messages.Identity{Kind: messages.IdentityLoop, ID: "loop_from"},
		To: messages.Destination{
			Kind:     messages.DestinationLoop,
			Selector: messages.SelectorID,
			Target:   "loop_to",
		},
		Priority: messages.PriorityUrgent,
	}
	result := &messages.DeliveryResult{
		Route:  "loop",
		Status: messages.DeliveryQueued,
	}

	record := DatasetRecordFromEnvelopeAudit(now, env, result, nil)
	if record.Dataset != DatasetEnvelopes {
		t.Errorf("dataset = %q, want %q", record.Dataset, DatasetEnvelopes)
	}
	if record.Kind != "delivery_queued" {
		t.Errorf("kind = %q, want %q", record.Kind, "delivery_queued")
	}
	if record.LoopID != "loop_to" {
		t.Errorf("loop_id = %q, want %q", record.LoopID, "loop_to")
	}
}

func assertDatasetLineCount(t *testing.T, root, dataset string, want int) {
	t.Helper()

	segmentTime := time.Now().In(time.Local)
	path := filepath.Join(root, dataset, segmentTime.Format(time.DateOnly), segmentTime.Format("15")+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != want {
		t.Fatalf("%s line count = %d, want %d", dataset, len(lines), want)
	}
}

func TestDatasetHandler_EnabledWithStdoutOnly(t *testing.T) {
	var stdout bytes.Buffer
	handler := NewDatasetHandler(slog.NewJSONHandler(&stdout, nil), nil, DatasetHandlerOptions{
		StdoutLevel:   slog.LevelInfo,
		StdoutEnabled: true,
	})

	if !handler.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("Enabled(info) = false, want true with stdout enabled")
	}
}
