package agent

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/memory"
)

// mockArchiver records session lifecycle calls for testing.
type mockArchiver struct {
	archived     []archivedCall
	sessions     []sessionCall
	activeID     string
	startedAt    time.Time
	sessionCount int
}

type archivedCall struct {
	conversationID string
	messages       []memory.Message
	reason         string
}

type sessionCall struct {
	action string // "start", "end"
	id     string
	reason string
}

func (m *mockArchiver) ArchiveConversation(convID string, msgs []memory.Message, reason string) error {
	m.archived = append(m.archived, archivedCall{convID, msgs, reason})
	return nil
}

func (m *mockArchiver) StartSession(convID string) (string, error) {
	m.sessionCount++
	id := "session-" + convID
	m.activeID = id
	m.sessions = append(m.sessions, sessionCall{"start", id, ""})
	return id, nil
}

func (m *mockArchiver) EndSession(sessionID, reason string) error {
	m.sessions = append(m.sessions, sessionCall{"end", sessionID, reason})
	m.activeID = ""
	return nil
}

func (m *mockArchiver) ActiveSessionID(string) string {
	return m.activeID
}

func (m *mockArchiver) EnsureSession(convID string) string {
	if m.activeID != "" {
		return m.activeID
	}
	id, _ := m.StartSession(convID)
	return id
}

func (m *mockArchiver) ArchiveIterations([]memory.ArchivedIteration) error { return nil }

func (m *mockArchiver) LinkPendingIterationToolCalls(string) error { return nil }

func (m *mockArchiver) OnMessage(string) {}

func (m *mockArchiver) ActiveSessionStartedAt(string) time.Time {
	return m.startedAt
}

// mockMemWithCompaction extends mockMem with AddCompactionSummary support.
type mockMemWithCompaction struct {
	*mockMem
	summaries []compactionSummary
}

type compactionSummary struct {
	conversationID string
	summary        string
}

func newMockMemWithCompaction() *mockMemWithCompaction {
	return &mockMemWithCompaction{mockMem: newMockMem()}
}

func (m *mockMemWithCompaction) AddCompactionSummary(convID, summary string) error {
	m.summaries = append(m.summaries, compactionSummary{convID, summary})
	// Also add as a message so GetMessages sees it.
	return m.AddMessage(convID, "system", summary)
}

func (m *mockMemWithCompaction) GetAllMessages(convID string) []memory.Message {
	return m.GetMessages(convID)
}

// newTestLoop creates a Loop with mocks suitable for session management tests.
func newTestLoop(mem MemoryStore, archiver SessionArchiver) *Loop {
	return &Loop{
		logger:   slog.Default(),
		memory:   mem,
		archiver: archiver,
	}
}

func TestCloseSession(t *testing.T) {
	tests := []struct {
		name         string
		messages     []memory.Message
		reason       string
		carryForward string
		wantArchived int
		wantReason   string
		wantHandoff  bool
	}{
		{
			name: "basic close with carry-forward",
			messages: []memory.Message{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi there"},
			},
			reason:       "topic change",
			carryForward: "User was discussing greetings.",
			wantArchived: 1,
			wantReason:   "topic change",
			wantHandoff:  true,
		},
		{
			name: "close with empty reason defaults",
			messages: []memory.Message{
				{Role: "user", Content: "test"},
			},
			reason:       "",
			carryForward: "Notes here.",
			wantArchived: 1,
			wantReason:   "close",
			wantHandoff:  true,
		},
		{
			name: "close with empty carry-forward",
			messages: []memory.Message{
				{Role: "user", Content: "bye"},
			},
			reason:       "done",
			carryForward: "",
			wantArchived: 1,
			wantReason:   "done",
			wantHandoff:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mem := newMockMemWithCompaction()
			archiver := &mockArchiver{activeID: "old-session"}
			loop := newTestLoop(mem, archiver)

			// Seed messages.
			for _, m := range tt.messages {
				_ = mem.AddMessage("conv1", m.Role, m.Content)
			}

			err := loop.CloseSession("conv1", tt.reason, tt.carryForward)
			if err != nil {
				t.Fatalf("CloseSession() error: %v", err)
			}

			// Verify archive was called.
			if len(archiver.archived) != tt.wantArchived {
				t.Errorf("archived calls = %d, want %d", len(archiver.archived), tt.wantArchived)
			}
			if len(archiver.archived) > 0 && archiver.archived[0].reason != tt.wantReason {
				t.Errorf("archive reason = %q, want %q", archiver.archived[0].reason, tt.wantReason)
			}

			// Verify old session ended.
			endCalls := filterSessions(archiver.sessions, "end")
			if len(endCalls) != 1 {
				t.Errorf("end session calls = %d, want 1", len(endCalls))
			}

			// Verify new session started.
			startCalls := filterSessions(archiver.sessions, "start")
			if len(startCalls) != 1 {
				t.Errorf("start session calls = %d, want 1", len(startCalls))
			}

			// Verify carry-forward injection.
			if tt.wantHandoff {
				if len(mem.summaries) != 1 {
					t.Fatalf("summaries = %d, want 1", len(mem.summaries))
				}
				if !strings.Contains(mem.summaries[0].summary, "[Session Handoff]") {
					t.Errorf("summary missing [Session Handoff] prefix: %q", mem.summaries[0].summary)
				}
				if !strings.Contains(mem.summaries[0].summary, tt.carryForward) {
					t.Errorf("summary missing carry-forward content")
				}
			} else {
				if len(mem.summaries) != 0 {
					t.Errorf("summaries = %d, want 0 (no carry-forward)", len(mem.summaries))
				}
			}

			// Verify old messages were cleared (only carry-forward remains, if any).
			msgs := mem.GetMessages("conv1")
			for _, m := range msgs {
				if m.Role != "system" {
					t.Errorf("non-system message survived close: role=%s content=%q", m.Role, m.Content)
				}
			}
		})
	}
}

func TestCheckpointSession(t *testing.T) {
	tests := []struct {
		name       string
		messages   []memory.Message
		label      string
		wantReason string
		wantErr    bool
	}{
		{
			name: "basic checkpoint",
			messages: []memory.Message{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi"},
			},
			label:      "pre-refactor",
			wantReason: "checkpoint:pre-refactor",
		},
		{
			name: "checkpoint with empty label",
			messages: []memory.Message{
				{Role: "user", Content: "test"},
			},
			label:      "",
			wantReason: "checkpoint",
		},
		{
			name:     "checkpoint with no messages fails",
			messages: nil,
			label:    "empty",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mem := newMockMemWithCompaction()
			archiver := &mockArchiver{activeID: "active-session"}
			loop := newTestLoop(mem, archiver)

			for _, m := range tt.messages {
				_ = mem.AddMessage("conv1", m.Role, m.Content)
			}

			err := loop.CheckpointSession("conv1", tt.label)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("CheckpointSession() error: %v", err)
			}

			// Verify archive was called with checkpoint reason.
			if len(archiver.archived) != 1 {
				t.Fatalf("archived calls = %d, want 1", len(archiver.archived))
			}
			if archiver.archived[0].reason != tt.wantReason {
				t.Errorf("archive reason = %q, want %q", archiver.archived[0].reason, tt.wantReason)
			}

			// Verify session was NOT ended (checkpoint doesn't end sessions).
			endCalls := filterSessions(archiver.sessions, "end")
			if len(endCalls) != 0 {
				t.Errorf("end session calls = %d, want 0 (checkpoint should not end session)", len(endCalls))
			}

			// Verify messages are still in memory (checkpoint doesn't clear).
			msgs := mem.GetMessages("conv1")
			if len(msgs) != len(tt.messages) {
				t.Errorf("messages after checkpoint = %d, want %d", len(msgs), len(tt.messages))
			}
		})
	}
}

func TestCheckpointSession_NoArchiver(t *testing.T) {
	mem := newMockMemWithCompaction()
	loop := newTestLoop(mem, nil) // no archiver

	err := loop.CheckpointSession("conv1", "test")
	if err == nil {
		t.Fatal("expected error when no archiver configured")
	}
}

func TestSplitSession(t *testing.T) {
	tests := []struct {
		name          string
		messages      []memory.Message
		atIndex       int
		atMessage     string
		wantPreSplit  int
		wantPostSplit int
		wantErr       bool
		errContains   string
	}{
		{
			name: "split by negative index",
			messages: []memory.Message{
				{Role: "user", Content: "msg1"},
				{Role: "assistant", Content: "msg2"},
				{Role: "user", Content: "msg3"},
				{Role: "assistant", Content: "msg4"},
				{Role: "user", Content: "msg5"},
			},
			atIndex:       -2,
			wantPreSplit:  3,
			wantPostSplit: 2,
		},
		{
			name: "split by message content",
			messages: []memory.Message{
				{Role: "user", Content: "let's talk about weather"},
				{Role: "assistant", Content: "sure, it's sunny"},
				{Role: "user", Content: "now let's discuss cooking"},
				{Role: "assistant", Content: "great topic"},
			},
			atMessage:     "discuss cooking",
			wantPreSplit:  2,
			wantPostSplit: 2,
		},
		{
			name: "split at -1 keeps only last message",
			messages: []memory.Message{
				{Role: "user", Content: "old"},
				{Role: "assistant", Content: "also old"},
				{Role: "user", Content: "newest"},
			},
			atIndex:       -1,
			wantPreSplit:  2,
			wantPostSplit: 1,
		},
		{
			name: "index out of range",
			messages: []memory.Message{
				{Role: "user", Content: "only one"},
			},
			atIndex:     -5,
			wantErr:     true,
			errContains: "out of range",
		},
		{
			name: "no matching message",
			messages: []memory.Message{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi"},
			},
			atMessage:   "nonexistent content",
			wantErr:     true,
			errContains: "no message found",
		},
		{
			name:     "empty messages",
			messages: nil,
			atIndex:  -1,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mem := newMockMemWithCompaction()
			archiver := &mockArchiver{activeID: "active-session"}
			loop := newTestLoop(mem, archiver)

			for _, m := range tt.messages {
				_ = mem.AddMessage("conv1", m.Role, m.Content)
			}

			err := loop.SplitSession("conv1", tt.atIndex, tt.atMessage)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("SplitSession() error: %v", err)
			}

			// Verify pre-split messages were archived.
			if len(archiver.archived) != 1 {
				t.Fatalf("archived calls = %d, want 1", len(archiver.archived))
			}
			if len(archiver.archived[0].messages) != tt.wantPreSplit {
				t.Errorf("archived messages = %d, want %d", len(archiver.archived[0].messages), tt.wantPreSplit)
			}
			if archiver.archived[0].reason != "split" {
				t.Errorf("archive reason = %q, want %q", archiver.archived[0].reason, "split")
			}

			// Verify old session ended.
			endCalls := filterSessions(archiver.sessions, "end")
			if len(endCalls) != 1 {
				t.Errorf("end session calls = %d, want 1", len(endCalls))
			}

			// Verify new session started.
			startCalls := filterSessions(archiver.sessions, "start")
			if len(startCalls) != 1 {
				t.Errorf("start session calls = %d, want 1", len(startCalls))
			}

			// Verify post-split messages retained in memory.
			msgs := mem.GetMessages("conv1")
			if len(msgs) != tt.wantPostSplit {
				t.Errorf("messages after split = %d, want %d", len(msgs), tt.wantPostSplit)
			}
		})
	}
}

func TestSplitSession_NoArchiver(t *testing.T) {
	mem := newMockMemWithCompaction()
	loop := newTestLoop(mem, nil)

	err := loop.SplitSession("conv1", -1, "")
	if err == nil {
		t.Fatal("expected error when no archiver configured")
	}
}

func TestFindSplitPoint(t *testing.T) {
	msgs := []memory.Message{
		{Role: "user", Content: "first message"},
		{Role: "assistant", Content: "second message"},
		{Role: "user", Content: "third message about cooking"},
		{Role: "assistant", Content: "fourth message"},
		{Role: "user", Content: "fifth message"},
	}

	tests := []struct {
		name      string
		atIndex   int
		atMessage string
		want      int
		wantErr   bool
	}{
		{name: "index -1", atIndex: -1, want: 4},
		{name: "index -3", atIndex: -3, want: 2},
		{name: "index -4", atIndex: -4, want: 1},
		{name: "message match", atMessage: "cooking", want: 2},
		{name: "message match first occurrence", atMessage: "message", want: 1},
		{name: "index too far back", atIndex: -5, wantErr: true},
		{name: "index at zero", atIndex: -6, wantErr: true},
		{name: "no match", atMessage: "nonexistent", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := findSplitPoint(msgs, tt.atIndex, tt.atMessage)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("findSplitPoint() error: %v", err)
			}
			if got != tt.want {
				t.Errorf("findSplitPoint() = %d, want %d", got, tt.want)
			}
		})
	}
}

// filterSessions returns session calls matching the given action.
func filterSessions(calls []sessionCall, action string) []sessionCall {
	var filtered []sessionCall
	for _, c := range calls {
		if c.action == action {
			filtered = append(filtered, c)
		}
	}
	return filtered
}
