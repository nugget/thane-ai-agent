package main

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// loadChannelBinding extracts the channel binding stored under
// conversations.metadata.channel_binding for the given conversation
// ID. Returns nil (no error) when the conversation has no binding —
// API-originated conversations don't, and that's fine.
func loadChannelBinding(db *sql.DB, convID string) (*memory.ChannelBinding, error) {
	var raw sql.NullString
	err := db.QueryRow(
		`SELECT json_extract(metadata, '$.channel_binding') FROM conversations WHERE id = ?`,
		convID,
	).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("conversation %q not found", convID)
	}
	if err != nil {
		return nil, fmt.Errorf("query channel binding: %w", err)
	}
	if !raw.Valid || raw.String == "" {
		return nil, nil
	}
	var binding memory.ChannelBinding
	if err := json.Unmarshal([]byte(raw.String), &binding); err != nil {
		return nil, fmt.Errorf("decode channel binding: %w", err)
	}
	return &binding, nil
}

// loadBindingFromJSON parses a JSON-encoded ChannelBinding (as stored
// in conversations.metadata.channel_binding). Empty input returns nil.
func loadBindingFromJSON(raw string) (*memory.ChannelBinding, error) {
	if raw == "" {
		return nil, nil
	}
	var binding memory.ChannelBinding
	if err := json.Unmarshal([]byte(raw), &binding); err != nil {
		return nil, fmt.Errorf("decode channel binding: %w", err)
	}
	return &binding, nil
}

// latestUserMessage returns the most recent user message recorded for
// the conversation, or an empty string when none exists. Tool and
// assistant turns are skipped.
func latestUserMessage(db *sql.DB, convID string) (string, error) {
	var content sql.NullString
	err := db.QueryRow(
		`SELECT content FROM messages WHERE conversation_id = ? AND role = 'user'
		 ORDER BY timestamp DESC LIMIT 1`,
		convID,
	).Scan(&content)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("query latest user message: %w", err)
	}
	if !content.Valid {
		return "", nil
	}
	return content.String, nil
}
