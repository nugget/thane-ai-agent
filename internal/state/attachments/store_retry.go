package attachments

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// priorIngest returns the record an earlier ingest made of the same
// receipt of the same attachment: the same channel, source identifier
// and content, from the same sender, in the same conversation, received
// at the same time. A retried message carries all of these unchanged, so
// it matches its first attempt. Two attachments in one message have
// different source identifiers, and another message referencing the same
// upload differs in its receipt, so each gets its own record. When
// earlier failures left several records of one receipt, the analyzed one
// is preferred. An attachment without a source identifier never reuses a
// record.
func (s *Store) priorIngest(ctx context.Context, hash string, params IngestParams) (*Record, error) {
	if params.Channel == "" || params.SourceID == "" {
		return nil, nil
	}
	return s.queryOne(ctx, `SELECT
		id, hash, store_path, original_name, content_type,
		size, width, height, channel, sender,
		conversation_id, received_at,
		description, analyzed_at, analysis_model, source_id
		FROM attachments
		WHERE channel = ? AND source_id = ? AND hash = ?
			AND sender = ? AND conversation_id = ? AND received_at = ?
		ORDER BY analyzed_at DESC, id
		LIMIT 1`,
		params.Channel, params.SourceID, hash,
		params.Sender, params.ConversationID, params.ReceivedAt.UTC().Format(time.RFC3339Nano))
}

// blobLock returns the lock that serializes ingests of content with
// this hash. Locks are shared by hash prefix, so unrelated content can
// wait on one another briefly, but the same content always shares a lock.
func (s *Store) blobLock(hash string) *sync.Mutex {
	prefix, _ := strconv.ParseUint(hash[:2], 16, 8)
	return &s.blobLocks[prefix%uint64(len(s.blobLocks))]
}
