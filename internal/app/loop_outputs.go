package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/channels/mqtt"
	"github.com/nugget/thane-ai-agent/internal/documents"
	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
)

// loopObservationStore persists structured loop observations emitted by
// output targets. It is append-only and shares the primary Thane DB.
type loopObservationStore struct {
	db *sql.DB
}

func newLoopObservationStore(db *sql.DB) (*loopObservationStore, error) {
	if db == nil {
		return nil, fmt.Errorf("nil database connection")
	}
	store := &loopObservationStore{db: db}
	if err := store.migrate(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *loopObservationStore) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS loop_observations (
		id TEXT PRIMARY KEY,
		timestamp TEXT NOT NULL,
		loop_id TEXT NOT NULL,
		loop_name TEXT NOT NULL,
		operation TEXT NOT NULL,
		iteration INTEGER NOT NULL DEFAULT 0,
		conversation_id TEXT NOT NULL DEFAULT '',
		request_id TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		content TEXT NOT NULL DEFAULT '',
		summary_json TEXT NOT NULL DEFAULT '{}',
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		supervisor INTEGER NOT NULL DEFAULT 0,
		metadata_json TEXT NOT NULL DEFAULT '{}',
		active_tags_json TEXT NOT NULL DEFAULT '[]'
	);
	CREATE INDEX IF NOT EXISTS idx_loop_observations_timestamp ON loop_observations(timestamp DESC);
	CREATE INDEX IF NOT EXISTS idx_loop_observations_loop ON loop_observations(loop_id, timestamp DESC);
	CREATE INDEX IF NOT EXISTS idx_loop_observations_request ON loop_observations(request_id);
	`
	_, err := s.db.Exec(schema)
	return err
}

func (s *loopObservationStore) Record(ctx context.Context, observation looppkg.Observation) error {
	if s == nil {
		return fmt.Errorf("loop observation store is not configured")
	}
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate loop observation ID: %w", err)
	}
	summaryJSON, err := json.Marshal(observation.Summary)
	if err != nil {
		return fmt.Errorf("marshal observation summary: %w", err)
	}
	metadataJSON, err := json.Marshal(observation.Metadata)
	if err != nil {
		return fmt.Errorf("marshal observation metadata: %w", err)
	}
	activeTagsJSON, err := json.Marshal(observation.ActiveTags)
	if err != nil {
		return fmt.Errorf("marshal observation active tags: %w", err)
	}
	timestamp := observation.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO loop_observations
			(id, timestamp, loop_id, loop_name, operation, iteration, conversation_id, request_id, model, content, summary_json, input_tokens, output_tokens, supervisor, metadata_json, active_tags_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id.String(),
		timestamp.UTC().Format(time.RFC3339),
		observation.LoopID,
		observation.LoopName,
		string(observation.Operation),
		observation.Iteration,
		observation.ConversationID,
		observation.RequestID,
		observation.Model,
		strings.TrimSpace(observation.Content),
		string(summaryJSON),
		observation.InputTokens,
		observation.OutputTokens,
		boolToInt(observation.Supervisor),
		string(metadataJSON),
		string(activeTagsJSON),
	)
	if err != nil {
		return fmt.Errorf("insert loop observation: %w", err)
	}
	return nil
}

type loopOutputDispatcher struct {
	observations *loopObservationStore
	documents    *documents.Store
	publishMQTT  func(context.Context, string, []byte, bool) error
	logger       *slog.Logger
}

func newLoopOutputDispatcher(observations *loopObservationStore, documents *documents.Store, mqttPub *mqtt.Publisher, logger *slog.Logger) *loopOutputDispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	var publishMQTT func(context.Context, string, []byte, bool) error
	if mqttPub != nil {
		publishMQTT = mqttPub.PublishTopic
	}
	return &loopOutputDispatcher{
		observations: observations,
		documents:    documents,
		publishMQTT:  publishMQTT,
		logger:       logger,
	}
}

func (a *App) ensureLoopOutputDispatcher() *loopOutputDispatcher {
	if a == nil {
		return nil
	}
	return newLoopOutputDispatcher(a.loopObservationStore, a.documentStore, a.mqttPub, a.logger)
}

func (d *loopOutputDispatcher) Deliver(ctx context.Context, delivery looppkg.OutputDelivery) error {
	if d == nil || len(delivery.Targets) == 0 {
		return nil
	}
	var errs []error
	for _, target := range delivery.Targets {
		if err := d.deliverTarget(ctx, target, delivery.Observation); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (d *loopOutputDispatcher) deliverTarget(ctx context.Context, target looppkg.OutputTarget, observation looppkg.Observation) error {
	switch target.Kind {
	case looppkg.OutputTargetObservationLog:
		if d.observations == nil {
			return fmt.Errorf("loop output target %q is not configured", target.Kind)
		}
		return d.observations.Record(ctx, observation)
	case looppkg.OutputTargetDocumentJournal:
		if d.documents == nil {
			return fmt.Errorf("loop output target %q is not configured", target.Kind)
		}
		entry := formatLoopObservationEntry(observation)
		if strings.TrimSpace(entry) == "" {
			return nil
		}
		_, err := d.documents.JournalUpdate(ctx, documents.JournalUpdateArgs{
			Ref:         target.Ref,
			Entry:       entry,
			Window:      target.Window,
			MaxWindows:  target.MaxWindows,
			Title:       target.Title,
			Description: target.Description,
			Tags:        append([]string(nil), target.Tags...),
		})
		if err != nil {
			return fmt.Errorf("deliver %s to %s: %w", target.Kind, target.Ref, err)
		}
		return nil
	case looppkg.OutputTargetMQTTTopic:
		if d.publishMQTT == nil {
			return fmt.Errorf("loop output target %q is not configured", target.Kind)
		}
		payload, err := json.Marshal(observation)
		if err != nil {
			return fmt.Errorf("marshal mqtt observation payload: %w", err)
		}
		if err := d.publishMQTT(ctx, target.Topic, payload, target.Retain); err != nil {
			return fmt.Errorf("deliver %s to %s: %w", target.Kind, target.Topic, err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported loop output target kind %q", target.Kind)
	}
}

func formatLoopObservationEntry(observation looppkg.Observation) string {
	content := strings.TrimSpace(observation.Content)
	summaryJSON := ""
	if len(observation.Summary) > 0 {
		if data, err := json.Marshal(observation.Summary); err == nil && string(data) != "{}" {
			summaryJSON = string(data)
		}
	}

	var parts []string
	if content != "" {
		parts = append(parts, content)
	}
	if summaryJSON != "" {
		parts = append(parts, "Summary: "+summaryJSON)
	}

	var contextParts []string
	if observation.LoopName != "" {
		contextParts = append(contextParts, "loop="+observation.LoopName)
	}
	if observation.Iteration > 0 {
		contextParts = append(contextParts, fmt.Sprintf("iteration=%d", observation.Iteration))
	}
	if observation.RequestID != "" {
		contextParts = append(contextParts, "request_id="+observation.RequestID)
	}
	if observation.Model != "" {
		contextParts = append(contextParts, "model="+observation.Model)
	}
	if observation.Supervisor {
		contextParts = append(contextParts, "supervisor=true")
	}
	if len(contextParts) > 0 {
		parts = append(parts, "Context: "+strings.Join(contextParts, "; "))
	}

	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
