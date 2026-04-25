package logging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
)

const (
	// DatasetEvents is the low-volume operator-significant lifecycle stream.
	DatasetEvents = "events"
	// DatasetRequests captures request lifecycle and model/tool activity.
	DatasetRequests = "requests"
	// DatasetAccess captures HTTP access-style request traffic.
	DatasetAccess = "access"
	// DatasetLoops captures loop lifecycle events.
	DatasetLoops = "loops"
	// DatasetDelegates captures delegate lifecycle events.
	DatasetDelegates = "delegates"
	// DatasetEnvelopes captures message-envelope delivery audit records.
	DatasetEnvelopes = "envelopes"
)

// Shared log-attribute keys and values used to route slog records into
// the right dataset. Kept in one place so the handler classifier and
// the HTTP access middleware cannot drift against each other.
const (
	// KindHTTPAccess labels HTTP access-log records for the access dataset.
	KindHTTPAccess = "http_access"
	// KindRequestReceived is an alternate access-log kind for legacy
	// compatibility with earlier call sites.
	KindRequestReceived = "request_received"

	// ComponentMessageBus labels slog records originating from the
	// envelope message bus plumbing.
	ComponentMessageBus = "message_bus"
)

// DatasetRecord is one append-only structured JSONL record in a dataset stream.
type DatasetRecord struct {
	EventID        string         `json:"event_id"`
	Timestamp      time.Time      `json:"ts"`
	Dataset        string         `json:"dataset"`
	Kind           string         `json:"kind"`
	SchemaVersion  int            `json:"schema_version"`
	RequestID      string         `json:"request_id,omitempty"`
	SessionID      string         `json:"session_id,omitempty"`
	ConversationID string         `json:"conversation_id,omitempty"`
	LoopID         string         `json:"loop_id,omitempty"`
	DelegateID     string         `json:"delegate_id,omitempty"`
	Source         string         `json:"source,omitempty"`
	Severity       string         `json:"severity,omitempty"`
	Payload        map[string]any `json:"payload,omitempty"`
}

type datasetSegment struct {
	key  string
	file *os.File
}

// DatasetWriter appends structured JSONL records into dataset/date/hour segments.
// It keeps at most one active segment open per dataset.
type DatasetWriter struct {
	root string

	mu     sync.Mutex
	active map[string]datasetSegment
}

// OpenDatasetWriter creates a filesystem-backed writer under root.
func OpenDatasetWriter(root string) (*DatasetWriter, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create logging root %s: %w", root, err)
	}
	return &DatasetWriter{
		root:   root,
		active: make(map[string]datasetSegment),
	}, nil
}

// Root returns the dataset root directory.
func (w *DatasetWriter) Root() string {
	if w == nil {
		return ""
	}
	return w.root
}

// Close closes all active dataset segment files.
func (w *DatasetWriter) Close() error {
	if w == nil {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	var firstErr error
	for dataset, seg := range w.active {
		if seg.file == nil {
			continue
		}
		if err := seg.file.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close dataset %s segment: %w", dataset, err)
		}
	}
	clear(w.active)
	return firstErr
}

// WriteRecord appends one structured record to the correct dataset/date/hour segment.
func (w *DatasetWriter) WriteRecord(record DatasetRecord) error {
	if w == nil {
		return nil
	}
	if strings.TrimSpace(record.Dataset) == "" {
		return fmt.Errorf("dataset record missing dataset")
	}
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now().UTC()
	}
	if record.SchemaVersion == 0 {
		record.SchemaVersion = 1
	}
	if strings.TrimSpace(record.EventID) == "" {
		record.EventID = datasetEventID()
	}

	line, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal dataset record: %w", err)
	}
	line = append(line, '\n')

	key, path := datasetSegmentKeyAndPath(w.root, record.Dataset, record.Timestamp)

	w.mu.Lock()
	defer w.mu.Unlock()

	seg := w.active[record.Dataset]
	if seg.key != key || seg.file == nil {
		if seg.file != nil {
			if err := seg.file.Close(); err != nil {
				return fmt.Errorf("close previous dataset segment: %w", err)
			}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create dataset directory %s: %w", filepath.Dir(path), err)
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("open dataset segment %s: %w", path, err)
		}
		seg = datasetSegment{key: key, file: file}
		w.active[record.Dataset] = seg
	}

	if _, err := seg.file.Write(line); err != nil {
		return fmt.Errorf("write dataset record to %s: %w", path, err)
	}
	return nil
}

// datasetSegmentKeyAndPath returns the partition key and filesystem path
// for a record at the given timestamp. Partitioning is done in UTC so
// the directory layout (date/hour folders) matches the ts field inside
// each record and DST transitions cannot produce overlapping or missing
// partitions.
func datasetSegmentKeyAndPath(root, dataset string, ts time.Time) (string, string) {
	segmentTime := ts.UTC()
	day := segmentTime.Format(time.DateOnly)
	hour := segmentTime.Format("15")
	return dataset + "/" + day + "/" + hour,
		filepath.Join(root, dataset, day, hour+".jsonl")
}

func datasetEventID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}

// DatasetRecordFromOperationalEvent converts a structured operational bus event
// into a dataset record for loops or delegates.
func DatasetRecordFromOperationalEvent(event events.Event) (DatasetRecord, bool) {
	dataset := ""
	switch event.Source {
	case events.SourceLoop:
		dataset = DatasetLoops
	case events.SourceDelegate:
		dataset = DatasetDelegates
	default:
		return DatasetRecord{}, false
	}

	payload := cloneMap(event.Data)
	record := DatasetRecord{
		Timestamp:     event.Timestamp.UTC(),
		Dataset:       dataset,
		Kind:          event.Kind,
		SchemaVersion: 1,
		Source:        event.Source,
		Severity:      severityForOperationalEvent(event),
		Payload:       payload,
	}
	if value, _ := payload["request_id"].(string); value != "" {
		record.RequestID = value
	}
	if value, _ := payload["conversation_id"].(string); value != "" {
		record.ConversationID = value
	}
	if value, _ := payload["loop_id"].(string); value != "" {
		record.LoopID = value
	}
	if value, _ := payload["delegate_id"].(string); value != "" {
		record.DelegateID = value
	}
	return record, true
}

func severityForOperationalEvent(event events.Event) string {
	// Error-bearing events graduate to ERROR so forensic consumers can
	// filter them cleanly. A kind containing "error" (e.g. loop_error,
	// delegate_error) or an explicit non-nil error field both signal a
	// failure mode, not just a degraded outcome.
	lowerKind := strings.ToLower(event.Kind)
	if strings.Contains(lowerKind, "error") {
		return "ERROR"
	}
	if errValue, exists := event.Data["error"]; exists && errValue != nil {
		return "ERROR"
	}
	// An explicit ok=false without an error field is a degraded outcome
	// rather than a hard failure (e.g. a tool returning unsuccessful).
	if ok, exists := event.Data["ok"].(bool); exists && !ok {
		return "WARN"
	}
	return "INFO"
}

// DatasetRecordFromEnvelopeAudit converts one envelope bus delivery attempt
// into an append-only envelopes dataset record.
func DatasetRecordFromEnvelopeAudit(now time.Time, env messages.Envelope, result *messages.DeliveryResult, deliveryErr error) DatasetRecord {
	kind := "delivery_attempt"
	severity := "INFO"
	switch {
	case deliveryErr != nil:
		kind = "delivery_failed"
		severity = "WARN"
	case result != nil && result.Status == messages.DeliveryQueued:
		kind = "delivery_queued"
	case result != nil && result.Status == messages.DeliveryDelivered:
		kind = "delivery_delivered"
	}

	payload := map[string]any{
		"envelope_id": env.ID,
		"type":        env.Type,
		"from":        env.From,
		"to":          env.To,
		"priority":    env.Priority,
	}
	if len(env.Scope) > 0 {
		payload["scope"] = append([]string(nil), env.Scope...)
	}
	if env.Payload != nil {
		payload["envelope_payload"] = env.Payload
	}
	if result != nil {
		payload["route"] = result.Route
		payload["delivery_status"] = result.Status
		if result.Details != nil {
			payload["details"] = result.Details
		}
	}
	if deliveryErr != nil {
		payload["error"] = deliveryErr.Error()
	}

	record := DatasetRecord{
		Timestamp:     now.UTC(),
		Dataset:       DatasetEnvelopes,
		Kind:          kind,
		SchemaVersion: 1,
		Source:        "message_bus",
		Severity:      severity,
		Payload:       payload,
	}
	if env.From.Kind == messages.IdentityDelegate && env.From.ID != "" {
		record.DelegateID = env.From.ID
	}
	if env.From.Kind == messages.IdentityLoop && env.From.ID != "" {
		record.LoopID = env.From.ID
	}
	if env.To.Kind == messages.DestinationLoop && env.To.Selector == messages.SelectorID {
		record.LoopID = env.To.Target
	}
	return record
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
