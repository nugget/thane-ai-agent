package app

import (
	"context"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/state/documents"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// A commit message is the only place a document root can say which turn
// produced a revision, so these cases pin what survives the trip: the facts
// that were present, in trailer form, without disturbing the subject a caller
// wrote.
func TestWithTurnProvenance(t *testing.T) {
	fullTurn := func(ctx context.Context) context.Context {
		ctx = tools.WithModel(ctx, "gpt-oss:120b")
		ctx = tools.WithLoopID(ctx, "loop-abc")
		ctx = tools.WithConversationID(ctx, "loop-metacognitive-1-123")
		ctx = tools.WithSessionID(ctx, "sess-1")
		ctx = tools.WithRequestID(ctx, "r_deadbeef")
		ctx = tools.WithToolCallID(ctx, "tc-1")
		return tools.WithIterationIndex(ctx, 3)
	}

	tests := []struct {
		name    string
		writer  *documentRootProvenanceWriter
		ctx     func(context.Context) context.Context
		message string
		want    map[string]string
		absent  []string
	}{
		{
			name:    "records every fact the turn carried",
			writer:  &documentRootProvenanceWriter{root: "self"},
			ctx:     fullTurn,
			message: "doc_write self:metacognitive.md",
			want: map[string]string{
				documents.TrailerModel:        "gpt-oss:120b",
				documents.TrailerLoopID:       "loop-abc",
				documents.TrailerConversation: "loop-metacognitive-1-123",
				documents.TrailerSession:      "sess-1",
				documents.TrailerRequest:      "r_deadbeef",
				documents.TrailerToolCall:     "tc-1",
				documents.TrailerIteration:    "3",
			},
		},
		{
			name:    "a turn that knows nothing leaves the message alone",
			writer:  &documentRootProvenanceWriter{root: "self"},
			ctx:     func(ctx context.Context) context.Context { return ctx },
			message: "doc_write self:metacognitive.md",
			want:    map[string]string{},
		},
		{
			name:   "the default conversation identifies nothing and is omitted",
			writer: &documentRootProvenanceWriter{root: "self"},
			ctx: func(ctx context.Context) context.Context {
				return tools.WithModel(ctx, "gpt-oss:120b")
			},
			message: "doc_write self:ego.md",
			want:    map[string]string{documents.TrailerModel: "gpt-oss:120b"},
			absent:  []string{documents.TrailerConversation},
		},
		{
			name:    "core does not restate its own parent commit",
			writer:  &documentRootProvenanceWriter{root: coreDocumentRoot, corePath: t.TempDir()},
			ctx:     fullTurn,
			message: "doc_write core:axioms.md",
			absent:  []string{documents.TrailerCoreHead},
		},
		{
			name:    "an unreadable core costs the trailer, not the write",
			writer:  &documentRootProvenanceWriter{root: "self", corePath: "/nonexistent/core"},
			ctx:     fullTurn,
			message: "doc_write self:ego.md",
			absent:  []string{documents.TrailerCoreHead},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.writer.withTurnProvenance(tc.ctx(context.Background()), tc.message)

			subject, _, _ := strings.Cut(got, "\n")
			if subject != tc.message {
				t.Errorf("subject rewritten: got %q, want %q", subject, tc.message)
			}
			trailers := parseTrailerBlock(t, got)
			for key, want := range tc.want {
				if trailers[key] != want {
					t.Errorf("trailer %s: got %q, want %q", key, trailers[key], want)
				}
			}
			for _, key := range tc.absent {
				if value, ok := trailers[key]; ok {
					t.Errorf("trailer %s should be absent, got %q", key, value)
				}
			}
			if len(tc.want) == 0 && len(tc.absent) == 0 && got != tc.message {
				t.Errorf("message with no facts to add was modified: %q", got)
			}
		})
	}
}

// A trailer block only means anything if git would parse it as one, so read it
// back the way git does rather than trusting how it was written.
func parseTrailerBlock(t *testing.T, message string) map[string]string {
	t.Helper()
	trailers := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(message), "\n") {
		key, value, ok := strings.Cut(line, ": ")
		if !ok || !strings.HasPrefix(key, "Thane-") {
			continue
		}
		trailers[key] = strings.TrimSpace(value)
	}
	return trailers
}
