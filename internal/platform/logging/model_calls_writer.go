package logging

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// drain writes queued items one transaction each until Close.
func (s *ModelCallStore) drain() {
	defer close(s.done)
	ctx := context.Background()
	for item := range s.ch {
		switch {
		case item.call != nil:
			if _, poisoned := s.poisoned[item.call.requestID]; poisoned {
				item.call.call.InputIncomplete = true
			}
			written, err := s.writeCall(ctx, item)
			if err != nil {
				s.poisoned[item.call.requestID] = struct{}{}
				s.warnPowersOfTwo(s.writeErrors.Add(1), "model call capture write failed; later calls in the request marked input_incomplete", "write_errors", err)
				continue
			}
			s.blobBytes.Add(uint64(written))
		case item.turn != nil:
			delete(s.poisoned, item.turn.requestID)
			if err := s.writeTurn(ctx, item.turn); err != nil {
				s.warnPowersOfTwo(s.writeErrors.Add(1), "model turn capture write failed", "write_errors", err)
			}
		}
	}
}

// writeCall stores a call's new blobs, refreshes last_seen on every
// blob the call references, and inserts the call row, all in one
// transaction. It returns the bytes of blobs newly stored.
func (s *ModelCallStore) writeCall(ctx context.Context, item modelCallItem) (written int, err error) {
	row := item.call
	now := FormatTimestamp(row.call.At)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for _, b := range item.blobs {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO log_model_blobs
			(hash, content, bytes, first_seen, last_seen) VALUES (?, ?, ?, ?, ?)`,
			b.hash, b.data, len(b.data), now, now)
		if err != nil {
			return 0, fmt.Errorf("insert blob: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			written += len(b.data)
		}
	}
	for _, h := range row.refs {
		if _, err := tx.ExecContext(ctx,
			`UPDATE log_model_blobs SET last_seen = ? WHERE hash = ? AND last_seen < ?`,
			now, h, now); err != nil {
			return 0, fmt.Errorf("touch blob: %w", err)
		}
	}
	if err := insertModelCallRow(ctx, tx, s.instanceID, row); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return written, nil
}

func insertModelCallRow(ctx context.Context, tx *sql.Tx, instanceID string, row *modelCallRow) error {
	c, h := row.call, row.hashed
	segments, err := json.Marshal(h.segments)
	if err != nil {
		return fmt.Errorf("marshal prompt segments: %w", err)
	}
	messages, err := json.Marshal(h.messageHashes)
	if err != nil {
		return fmt.Errorf("marshal message hashes: %w", err)
	}
	toolHashes, err := json.Marshal(h.toolHashes)
	if err != nil {
		return fmt.Errorf("marshal tool hashes: %w", err)
	}
	var firstEvent sql.NullInt64
	if c.FirstEvent != nil {
		firstEvent = sql.NullInt64{Int64: c.FirstEvent.Milliseconds(), Valid: true}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO log_model_calls (
		request_id, instance_id, call_seq, iteration, kind,
		conversation_id, loop_id, loop_name,
		deployment, resource, provider, upstream_model, serving_profile_hash,
		thane_commit, wire_model, ttl, loaded_context_window,
		stream, max_tokens, tool_choice,
		prompt_segments, message_hashes, tool_surface_hash, tool_hashes, stable_prefix_hash,
		wire_sha256, wire_bytes, response_hash, stop_reason, error_class,
		input_tokens, output_tokens, cache_creation_input_tokens, cache_read_input_tokens,
		duration_ms, first_event_ms,
		redacted, input_incomplete, image_dependent, segment_fallback, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.requestID, instanceID, row.callSeq, c.Iteration, nullString(c.Kind),
		nullString(c.ConversationID), nullString(c.LoopID), nullString(c.LoopName),
		nullString(c.Deployment), nullString(c.Profile.Resource), nullString(c.Profile.Provider),
		nullString(c.Profile.UpstreamModel), h.profileHash,
		nullString(c.ThaneCommit), nullString(c.WireModel), nullIntPtr(c.TTL), nullInt(c.LoadedContextWindow),
		nullBoolPtr(c.Stream), nullIntPtr(c.MaxTokens), nullString(c.ToolChoice),
		string(segments), string(messages), h.toolSurfaceHash, string(toolHashes), nullString(h.stablePrefixHash),
		nullString(c.WireSHA256), nullInt(c.WireBytes), nullString(h.responseHash),
		nullString(c.StopReason), nullString(c.ErrorClass),
		c.InputTokens, c.OutputTokens, c.CacheCreationInputTokens, c.CacheReadInputTokens,
		c.Duration.Milliseconds(), firstEvent,
		c.Redacted, c.InputIncomplete, h.imageDependent, h.segmentFallback, FormatTimestamp(c.At),
	)
	if err != nil {
		return fmt.Errorf("insert call: %w", err)
	}
	return nil
}

func (s *ModelCallStore) writeTurn(ctx context.Context, row *modelTurnRow) error {
	var outcomes sql.NullString
	if len(row.turn.ToolOutcomes) > 0 {
		b, err := json.Marshal(row.turn.ToolOutcomes)
		if err != nil {
			return fmt.Errorf("marshal tool outcomes: %w", err)
		}
		outcomes = sql.NullString{String: string(b), Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO log_model_turns
		(request_id, instance_id, completed, exhausted, exhaust_reason, tool_outcomes, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		row.requestID, s.instanceID, row.turn.Completed, row.turn.Exhausted,
		nullString(row.turn.ExhaustReason), outcomes, FormatTimestamp(row.turn.At))
	if err != nil {
		return fmt.Errorf("insert turn: %w", err)
	}
	return nil
}

// copyPtr returns a pointer to a copy of *p, or nil.
func copyPtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// nullIntPtr returns NULL for a nil pointer and the value otherwise,
// so a reported zero is stored as 0, not as unknown.
func nullIntPtr(p *int) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*p), Valid: true}
}

// nullBoolPtr returns NULL for a nil pointer and the value otherwise.
func nullBoolPtr(p *bool) sql.NullBool {
	if p == nil {
		return sql.NullBool{}
	}
	return sql.NullBool{Bool: *p, Valid: true}
}
