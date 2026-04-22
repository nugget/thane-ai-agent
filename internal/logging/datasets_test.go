package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
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

	// Discover the segment via a directory walk rather than
	// recomputing the path from the timestamp — keeps the test
	// decoupled from the partitioning scheme.
	lines := readDatasetLines(t, dir, DatasetEvents)
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

	lines := readDatasetLines(t, dir, DatasetRequests)
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

	// stdout fires for:
	//   - "startup complete" (events, level info)
	//   - "message envelope delivery failed" (events, level warn)
	// It suppresses the request and access lines because those
	// datasets are meant to live on disk, not pollute stdout.
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

	// The bootstrap line and the message-bus warn both land in
	// events — the loops/delegates/envelopes datasets are reserved
	// for structured bus events written via the direct-sink path.
	assertDatasetLineCount(t, dir, DatasetEvents, 2)
	assertDatasetLineCount(t, dir, DatasetRequests, 1)
	assertDatasetLineCount(t, dir, DatasetAccess, 1)
}

func TestDatasetHandler_StdoutFiresWhenDatasetWriteFails(t *testing.T) {
	// Use a real writer backed by a non-existent root to trigger the
	// error path. MkdirAll will succeed on /tmp/<tempdir>, but pointing
	// OpenDatasetWriter at a file path (not a directory) makes the
	// subsequent MkdirAll for the dataset subdirectory fail.
	tmp := t.TempDir()
	// Create a regular file where the logger will try to create a
	// directory, causing WriteRecord's os.MkdirAll to fail.
	blocker := filepath.Join(tmp, "root")
	if err := os.WriteFile(blocker, []byte("not-a-dir"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	writer := &DatasetWriter{root: blocker, active: make(map[string]datasetSegment)}

	var stdout bytes.Buffer
	handler := NewDatasetHandler(slog.NewJSONHandler(&stdout, nil), writer, DatasetHandlerOptions{
		DatasetLevel:  slog.LevelInfo,
		StdoutLevel:   slog.LevelInfo,
		StdoutEnabled: true,
		EventsEnabled: true,
	})

	err := handler.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelWarn, "boom", 0))
	if err == nil {
		t.Fatal("Handle() returned nil, want dataset-write error")
	}
	if stdout.Len() == 0 {
		t.Error("stdout empty — write failure should not suppress operator output")
	}
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

	// Walk the whole dataset subtree rather than recomputing the path
	// from time.Now(): a clock tick between write and assertion would
	// point us at the wrong partition and mask a real failure.
	assertDatasetEmpty(t, dir, DatasetAccess)
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
	// ok=false without an explicit error field is a degraded outcome (WARN),
	// not a hard failure.
	if record.Severity != "WARN" {
		t.Errorf("severity = %q, want %q", record.Severity, "WARN")
	}
}

func TestDatasetRecordFromOperationalEvent_ErrorSeverity(t *testing.T) {
	tests := []struct {
		name  string
		kind  string
		data  map[string]any
		level string
	}{
		{
			name:  "kind contains error",
			kind:  events.KindLoopError,
			data:  map[string]any{"loop_id": "loop_a"},
			level: "ERROR",
		},
		{
			name:  "explicit error field",
			kind:  events.KindComplete,
			data:  map[string]any{"delegate_id": "deleg_b", "error": "boom"},
			level: "ERROR",
		},
		{
			name:  "ok false without error",
			kind:  events.KindComplete,
			data:  map[string]any{"delegate_id": "deleg_c", "ok": false},
			level: "WARN",
		},
		{
			name:  "ok true",
			kind:  events.KindComplete,
			data:  map[string]any{"delegate_id": "deleg_d", "ok": true},
			level: "INFO",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record, ok := DatasetRecordFromOperationalEvent(events.Event{
				Timestamp: time.Now(),
				Source:    events.SourceDelegate,
				Kind:      tt.kind,
				Data:      tt.data,
			})
			if !ok {
				t.Fatal("DatasetRecordFromOperationalEvent() = false, want true")
			}
			if record.Severity != tt.level {
				t.Errorf("severity = %q, want %q", record.Severity, tt.level)
			}
		})
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

// assertDatasetLineCount sums JSONL line counts across every segment
// file under the dataset subtree. Walking the tree (rather than
// recomputing the partition path from time.Now()) keeps the assertion
// correct when the test straddles an hour/day boundary.
func assertDatasetLineCount(t *testing.T, root, dataset string, want int) {
	t.Helper()

	got := datasetLineCount(t, root, dataset)
	if got != want {
		t.Fatalf("%s line count = %d, want %d", dataset, got, want)
	}
}

// assertDatasetEmpty verifies that no segment files have been created
// for the dataset. Tolerates the dataset root not existing at all.
func assertDatasetEmpty(t *testing.T, root, dataset string) {
	t.Helper()

	got := datasetLineCount(t, root, dataset)
	if got != 0 {
		t.Fatalf("%s line count = %d, want 0 (dataset should be empty)", dataset, got)
	}
}

func datasetLineCount(t *testing.T, root, dataset string) int {
	t.Helper()
	return len(readDatasetLines(t, root, dataset))
}

// readDatasetLines walks every segment file under root/dataset and
// returns each JSONL line in file-path order. Tests use this instead
// of recomputing partition paths from the clock, which would be racy
// across hour/day boundaries.
func readDatasetLines(t *testing.T, root, dataset string) []string {
	t.Helper()

	datasetDir := filepath.Join(root, dataset)
	if _, err := os.Stat(datasetDir); os.IsNotExist(err) {
		return nil
	}

	var files []string
	err := filepath.WalkDir(datasetDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", datasetDir, err)
	}
	sort.Strings(files)

	var lines []string
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("ReadFile(%q) error = %v", file, err)
		}
		trimmed := strings.TrimSpace(string(data))
		if trimmed == "" {
			continue
		}
		lines = append(lines, strings.Split(trimmed, "\n")...)
	}
	return lines
}

// TestDatasetWriter_RolloverAcrossHourAndDay verifies that records
// whose timestamps straddle an hour or day boundary land in the
// correct per-partition segment file, and that consecutive writes
// rotate cleanly without losing records. This covers the "rotation to
// a new date/hour segment happens cleanly" item in the #698 test plan.
func TestDatasetWriter_RolloverAcrossHourAndDay(t *testing.T) {
	dir := t.TempDir()
	writer, err := OpenDatasetWriter(dir)
	if err != nil {
		t.Fatalf("OpenDatasetWriter() error = %v", err)
	}
	defer writer.Close()

	// Three records spanning an hour roll (14 -> 15) and a day roll
	// (2026-04-10 23:xx -> 2026-04-11 00:xx). All UTC.
	records := []DatasetRecord{
		{Timestamp: time.Date(2026, 4, 10, 14, 59, 0, 0, time.UTC), Dataset: DatasetEvents, Kind: "a"},
		{Timestamp: time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC), Dataset: DatasetEvents, Kind: "b"},
		{Timestamp: time.Date(2026, 4, 10, 23, 59, 30, 0, time.UTC), Dataset: DatasetEvents, Kind: "c"},
		{Timestamp: time.Date(2026, 4, 11, 0, 0, 15, 0, time.UTC), Dataset: DatasetEvents, Kind: "d"},
	}
	for _, r := range records {
		if err := writer.WriteRecord(r); err != nil {
			t.Fatalf("WriteRecord(%s) error = %v", r.Kind, err)
		}
	}

	// Expect four distinct segment files, one per (day, hour).
	expectedPaths := []string{
		filepath.Join(dir, DatasetEvents, "2026-04-10", "14.jsonl"),
		filepath.Join(dir, DatasetEvents, "2026-04-10", "15.jsonl"),
		filepath.Join(dir, DatasetEvents, "2026-04-10", "23.jsonl"),
		filepath.Join(dir, DatasetEvents, "2026-04-11", "00.jsonl"),
	}
	for i, path := range expectedPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing segment %q: %v", path, err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) != 1 {
			t.Errorf("segment %q line count = %d, want 1", path, len(lines))
		}
		var got DatasetRecord
		if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
			t.Fatalf("json.Unmarshal(%q) error = %v", path, err)
		}
		if got.Kind != records[i].Kind {
			t.Errorf("segment %q kind = %q, want %q", path, got.Kind, records[i].Kind)
		}
	}

	// Full dataset count should be four — one per segment.
	if got := datasetLineCount(t, dir, DatasetEvents); got != 4 {
		t.Errorf("total line count = %d, want 4", got)
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
