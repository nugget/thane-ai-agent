package checkpoint

import (
	"database/sql"
	"log/slog"
	"os"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// mockConversationProvider returns test conversations.
type mockConversationProvider struct {
	conversations []Conversation
}

func (m *mockConversationProvider) GetConversations() ([]Conversation, error) {
	return m.conversations, nil
}

// mockFactProvider returns test facts.
type mockFactProvider struct {
	facts []Fact
}

func (m *mockFactProvider) GetFacts() ([]Fact, error) {
	return m.facts, nil
}

func TestGetStartupStatus_Empty(t *testing.T) {
	// Create temp database
	tmpDB, err := os.CreateTemp("", "checkpoint-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	db, err := sql.Open("sqlite3", tmpDB.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cp, err := NewCheckpointer(db, Config{}, logger)
	if err != nil {
		t.Fatal(err)
	}

	// Empty providers
	cp.SetProviders(
		&mockConversationProvider{},
		&mockFactProvider{},
		nil,
	)

	status, err := cp.GetStartupStatus()
	if err != nil {
		t.Fatalf("GetStartupStatus failed: %v", err)
	}

	if status.Conversations != 0 {
		t.Errorf("expected 0 conversations, got %d", status.Conversations)
	}
	if status.Messages != 0 {
		t.Errorf("expected 0 messages, got %d", status.Messages)
	}
	if status.Facts != 0 {
		t.Errorf("expected 0 facts, got %d", status.Facts)
	}
	if status.LastCheckpoint != nil {
		t.Error("expected nil LastCheckpoint")
	}
}

func TestGetStartupStatus_WithData(t *testing.T) {
	tmpDB, err := os.CreateTemp("", "checkpoint-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	db, err := sql.Open("sqlite3", tmpDB.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cp, err := NewCheckpointer(db, Config{}, logger)
	if err != nil {
		t.Fatal(err)
	}

	// Providers with test data
	cp.SetProviders(
		&mockConversationProvider{
			conversations: []Conversation{
				{
					ID: "conv-1",
					Messages: []Message{
						{Role: "user", Content: "hello"},
						{Role: "assistant", Content: "hi there"},
					},
				},
				{
					ID: "conv-2",
					Messages: []Message{
						{Role: "user", Content: "test"},
					},
				},
			},
		},
		&mockFactProvider{
			facts: []Fact{
				{Key: "fact-1", Value: "value-1"},
				{Key: "fact-2", Value: "value-2"},
				{Key: "fact-3", Value: "value-3"},
			},
		},
		nil,
	)

	status, err := cp.GetStartupStatus()
	if err != nil {
		t.Fatalf("GetStartupStatus failed: %v", err)
	}

	if status.Conversations != 2 {
		t.Errorf("expected 2 conversations, got %d", status.Conversations)
	}
	if status.Messages != 3 {
		t.Errorf("expected 3 messages, got %d", status.Messages)
	}
	if status.Facts != 3 {
		t.Errorf("expected 3 facts, got %d", status.Facts)
	}
}

func TestStartupStatus_Struct(t *testing.T) {
	now := time.Now()
	status := StartupStatus{
		Conversations:  5,
		Messages:       42,
		Facts:          10,
		LastCheckpoint: &now,
	}

	if status.Conversations != 5 {
		t.Error("Conversations mismatch")
	}
	if status.Messages != 42 {
		t.Error("Messages mismatch")
	}
	if status.Facts != 10 {
		t.Error("Facts mismatch")
	}
	if status.LastCheckpoint == nil || !status.LastCheckpoint.Equal(now) {
		t.Error("LastCheckpoint mismatch")
	}
}
