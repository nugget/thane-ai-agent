package memory

import (
	"context"
	"testing"
)

// Existing preservation tests require a successful read before inspecting
// its content; an error must never look like an expected empty slice.
func mustReadMessages(t *testing.T, read func(context.Context, string) ([]Message, error), conversationID string) []Message {
	t.Helper()
	messages, err := read(context.Background(), conversationID)
	if err != nil {
		t.Fatalf("read messages for %q: %v", conversationID, err)
	}
	return messages
}
