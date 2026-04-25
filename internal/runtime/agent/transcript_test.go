package agent

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

func TestConversationTranscript(t *testing.T) {
	// Fixed reference time for deterministic assertions.
	now := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

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
			name: "basic user/assistant dialogue with delta timestamps",
			messages: []memory.Message{
				{Role: "user", Content: "hello", Timestamp: now.Add(-60 * time.Second)},
				{Role: "assistant", Content: "hi there", Timestamp: now},
			},
			wantSub: []string{
				"[-60s] user: hello",
				"[-0s] assistant: hi there",
			},
		},
		{
			name: "system and tool messages excluded",
			messages: []memory.Message{
				{Role: "system", Content: "you are helpful", Timestamp: now.Add(-3 * time.Minute)},
				{Role: "user", Content: "help me", Timestamp: now.Add(-2 * time.Minute)},
				{Role: "tool", Content: `{"result": "ok"}`, Timestamp: now.Add(-time.Minute)},
				{Role: "assistant", Content: "done", Timestamp: now},
			},
			wantSub: []string{
				"[-120s] user: help me",
				"[-0s] assistant: done",
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
				logger:  slog.Default(),
				memory:  mem,
				nowFunc: clock,
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
