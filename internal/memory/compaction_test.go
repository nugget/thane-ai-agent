package memory

import (
	"context"
	"log/slog"
	"os"
	"testing"
)

func TestCompaction(t *testing.T) {
	// Create temp database
	tmpFile, err := os.CreateTemp("", "thane-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	// Create store
	store, err := NewSQLiteStore(tmpFile.Name(), 100)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	convID := "test-conv"

	// Add many messages to trigger compaction
	for i := 0; i < 30; i++ {
		store.AddMessage(convID, "user", "This is test message number "+string(rune('0'+i%10))+" with some content to make it longer")
		store.AddMessage(convID, "assistant", "This is response number "+string(rune('0'+i%10))+" with some content to make it longer too")
	}

	// Check token count
	tokens := store.GetTokenCount(convID)
	t.Logf("Token count after 60 messages: %d", tokens)

	// Create compactor with simple summarizer
	config := CompactionConfig{
		MaxTokens:            2000, // Low threshold for testing
		TriggerRatio:         0.5,
		KeepRecent:           10,
		MinMessagesToCompact: 10,
	}
	compactor := NewCompactor(store, config, &SimpleSummarizer{}, slog.Default())

	// Check if needs compaction
	if !compactor.NeedsCompaction(convID) {
		t.Log("Compaction not needed (might be expected with low message count)")
	} else {
		t.Log("Compaction needed!")
	}

	// Get messages for compaction
	toCompact := store.GetMessagesForCompaction(convID, config.KeepRecent)
	t.Logf("Messages to compact: %d", len(toCompact))

	// Run compaction
	if len(toCompact) >= config.MinMessagesToCompact {
		err = compactor.Compact(context.Background(), convID)
		if err != nil {
			t.Fatalf("Compaction failed: %v", err)
		}
		t.Log("Compaction completed!")

		// Check messages after compaction
		messages := store.GetMessages(convID)
		t.Logf("Messages after compaction: %d", len(messages))

		// Should have summary + recent messages
		if len(messages) > config.KeepRecent+5 {
			t.Errorf("Expected around %d messages, got %d", config.KeepRecent, len(messages))
		}

		// Check new token count
		newTokens := store.GetTokenCount(convID)
		t.Logf("Token count after compaction: %d (was %d)", newTokens, tokens)

		if newTokens >= tokens {
			t.Error("Token count should have decreased after compaction")
		}
	}
}

func TestCompactionStats(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "thane-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := NewSQLiteStore(tmpFile.Name(), 100)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	config := DefaultCompactionConfig()
	compactor := NewCompactor(store, config, &SimpleSummarizer{}, slog.Default())

	stats := compactor.CompactionStats("test")
	t.Logf("Stats: %+v", stats)

	if stats["max_tokens"] != config.MaxTokens {
		t.Errorf("Expected max_tokens %d, got %v", config.MaxTokens, stats["max_tokens"])
	}
}
