package agent

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/memory"
)

func TestConversationTranscript(t *testing.T) {
	ts := time.Date(2025, 6, 15, 14, 30, 0, 0, time.UTC)

	tests := []struct {
		name     string
		messages []memory.Message
		wantSub  []string // substrings that must appear
		wantNot  []string // substrings that must NOT appear
		wantLen  int      // 0 = just check non-empty, -1 = must be empty
	}{
		{
			name:    "empty conversation",
			wantLen: -1,
		},
		{
			name: "basic user/assistant dialogue",
			messages: []memory.Message{
				{Role: "user", Content: "hello", Timestamp: ts},
				{Role: "assistant", Content: "hi there", Timestamp: ts.Add(time.Minute)},
			},
			wantSub: []string{
				"[14:30] user: hello",
				"[14:31] assistant: hi there",
			},
		},
		{
			name: "system and tool messages excluded",
			messages: []memory.Message{
				{Role: "system", Content: "you are helpful", Timestamp: ts},
				{Role: "user", Content: "help me", Timestamp: ts.Add(time.Minute)},
				{Role: "tool", Content: `{"result": "ok"}`, Timestamp: ts.Add(2 * time.Minute)},
				{Role: "assistant", Content: "done", Timestamp: ts.Add(3 * time.Minute)},
			},
			wantSub: []string{
				"user: help me",
				"assistant: done",
			},
			wantNot: []string{
				"you are helpful",
				`"result"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mem := newMockMem()
			mem.msgs["test-conv"] = append(mem.msgs["test-conv"], tt.messages...)

			l := &Loop{
				logger: slog.Default(),
				memory: mem,
			}

			got := l.ConversationTranscript("test-conv")

			if tt.wantLen == -1 {
				if got != "" {
					t.Errorf("expected empty transcript, got %q", got)
				}
				return
			}

			for _, sub := range tt.wantSub {
				if !strings.Contains(got, sub) {
					t.Errorf("transcript should contain %q, got:\n%s", sub, got)
				}
			}
			for _, sub := range tt.wantNot {
				if strings.Contains(got, sub) {
					t.Errorf("transcript should NOT contain %q, got:\n%s", sub, got)
				}
			}
		})
	}
}
