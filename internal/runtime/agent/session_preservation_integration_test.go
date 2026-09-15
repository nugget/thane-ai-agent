package agent

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

func TestSessionLifecyclePreservesUnifiedHistory(t *testing.T) {
	t.Parallel()
	for _, foreignKeys := range []bool{false, true} {
		for _, operation := range []string{"reset", "close", "checkpoint", "split", "split_out_of_range"} {
			t.Run(fmt.Sprintf("%s/foreign_keys=%t", operation, foreignKeys), func(t *testing.T) {
				t.Parallel()
				f := newSessionPreservationFixture(t, foreignKeys)
				before := f.messageEvidence(t)
				original, err := f.mem.GetConversation(t.Context(), f.convID)
				if err != nil {
					t.Fatal(err)
				}
				binding := original.Metadata.Clone()
				switch operation {
				case "reset":
					err = f.loop.ResetConversation(f.convID)
				case "close":
					err = f.loop.CloseSession(f.convID, "topic change", "Keep the pending decision.")
				case "checkpoint":
					err = f.loop.CheckpointSession(f.convID, "before-change")
				case "split":
					err = f.loop.SplitSession(f.convID, -2, "")
				case "split_out_of_range":
					// Four current-session rows exist. The older archived row
					// must not make this otherwise-invalid offset acceptable.
					err = f.loop.SplitSession(f.convID, -4, "")
				}
				if operation == "split_out_of_range" {
					if err == nil {
						t.Fatal("split accepted an offset reaching outside the current session")
					}
				} else if err != nil {
					t.Fatalf("%s: %v", operation, err)
				}

				// Closing/resetting a session can itself be a tool call. Its
				// result arrives after the lifecycle transition has committed.
				if err := f.mem.CompleteToolCall("call-early", "transition finished", ""); err != nil {
					t.Fatalf("complete tool after %s: %v", operation, err)
				}
				after := f.messageEvidence(t)
				for id, want := range before {
					if got, ok := after[id]; !ok || got != want {
						t.Errorf("original message %q changed or disappeared: got %+v, want %+v", id, got, want)
					}
				}
				wantRows := len(before)
				if operation == "close" {
					wantRows++ // The sole additional row is the carry-forward.
				}
				if len(after) != wantRows {
					t.Errorf("message rows = %d, want %d; a lifecycle transition must not copy transcripts", len(after), wantRows)
				}
				conv, err := f.mem.GetConversation(t.Context(), f.convID)
				if err != nil || conv == nil || !reflect.DeepEqual(conv.Metadata, binding) {
					t.Fatalf("conversation binding lost: %+v, %v", conv, err)
				}

				prior, err := f.archive.GetSessionTranscript(f.priorSession)
				if err != nil || len(prior) != 1 || prior[0].ID != "historical" {
					t.Fatalf("prior transcript changed: %+v, %v", prior, err)
				}
				var indexed int
				if err := f.mem.DB().QueryRow(`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'preservationneedle'`).Scan(&indexed); err != nil {
					t.Fatal(err)
				}
				if indexed != len(before) {
					t.Errorf("searchable original rows = %d, want %d", indexed, len(before))
				}

				active := mustMemoryMessages(t, f.mem, f.convID)
				var wantIDs []string
				switch operation {
				case "reset":
				case "close":
					if len(active) != 1 || active[0].Content != "[Session Handoff]\nKeep the pending decision." {
						t.Fatalf("handoff context = %+v", active)
					}
					wantIDs = []string{active[0].ID}
				case "checkpoint", "split_out_of_range":
					wantIDs = []string{"early", "retained", "reply"}
				case "split":
					wantIDs = []string{"retained", "reply"}
				}
				var gotIDs []string
				for _, m := range active {
					gotIDs = append(gotIDs, m.ID)
				}
				if !reflect.DeepEqual(gotIDs, wantIDs) {
					t.Errorf("active IDs = %v, want %v", gotIDs, wantIDs)
				}

				oldSession, err := f.archive.GetSession(f.sessionID)
				if err != nil {
					t.Fatal(err)
				}
				current := f.adapter.ActiveSessionID(f.convID)
				if operation == "checkpoint" || operation == "split_out_of_range" {
					if current != f.sessionID || oldSession.EndedAt != nil {
						t.Fatalf("non-closing operation changed the active session: %q, %+v", current, oldSession)
					}
				} else if current == "" || current == f.sessionID || oldSession.EndedAt == nil {
					t.Fatalf("session transition missing: current=%q old=%+v", current, oldSession)
				}
				if operation == "split" {
					transcript, err := f.archive.GetSessionTranscript(f.sessionID)
					if err != nil || len(transcript) != 2 || transcript[0].ID != "compacted" || transcript[1].ID != "early" {
						t.Fatalf("split archived the wrong rows: %+v, %v", transcript, err)
					}
				}
				var messageID, result string
				if err := f.mem.DB().QueryRow(`SELECT message_id, result FROM tool_calls WHERE id = 'call-early'`).Scan(&messageID, &result); err != nil {
					t.Fatal(err)
				}
				if messageID != "early" || result != "transition finished" {
					t.Fatalf("tool linkage/result changed: message=%q result=%q", messageID, result)
				}
				var violations int
				if err := f.mem.DB().QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
					t.Fatal(err)
				}
				if violations != 0 {
					t.Errorf("foreign key violations = %d", violations)
				}
			})
		}
	}
}

type sessionPreservationFixture struct {
	mem          *memory.SQLiteStore
	archive      *memory.ArchiveStore
	adapter      *memory.ArchiveAdapter
	loop         *Loop
	convID       string
	sessionID    string
	priorSession string
}

func newSessionPreservationFixture(t *testing.T, foreignKeys bool) *sessionPreservationFixture {
	t.Helper()
	mem, err := memory.NewSQLiteStore(filepath.Join(t.TempDir(), "thane.db"), 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mem.Close(); err != nil {
			t.Error(err)
		}
	})
	mem.DB().SetMaxOpenConns(1)
	if foreignKeys {
		if _, err := mem.DB().Exec(`PRAGMA foreign_keys = ON`); err != nil {
			t.Fatal(err)
		}
	}
	archive, err := memory.NewArchiveStoreFromDB(mem.DB(), nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if !archive.FTSEnabled() {
		t.Fatal("production archive FTS was not initialized")
	}
	adapter := memory.NewArchiveAdapter(archive, mem, slog.Default())
	f := &sessionPreservationFixture{mem: mem, archive: archive, adapter: adapter, convID: "preservation-conversation"}
	f.loop = newTestLoop(mem, adapter)
	if err := mem.BindConversationChannel(f.convID, &memory.ChannelBinding{
		Channel: "signal", Address: "+15550001111", ContactID: "contact-one", TrustZone: "trusted",
	}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	prior, err := archive.StartSessionAt(f.convID, base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	f.priorSession = prior.ID
	if err := archive.EndSessionAt(prior.ID, "earlier", base.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	current, err := archive.StartSessionAt(f.convID, base)
	if err != nil {
		t.Fatal(err)
	}
	f.sessionID = current.ID
	for i, row := range []struct{ id, role, status string }{
		{"historical", "user", "archived"},
		{"compacted", "user", "compacted"},
		{"early", "assistant", "active"},
		{"retained", "user", "active"},
		{"reply", "assistant", "active"},
	} {
		sessionID := f.sessionID
		if row.id == "historical" {
			sessionID = f.priorSession
		}
		toolCalls := ""
		if row.id == "early" {
			toolCalls = `[{"id":"call-early","type":"function","function":{"name":"session_close","arguments":"{}"}}]`
		}
		midTurn := 0
		if row.id == "retained" {
			midTurn = 1
		}
		_, err := mem.DB().Exec(`INSERT INTO messages
			(id, conversation_id, session_id, role, content, timestamp, token_count, tool_calls, tool_call_id, status, mid_turn, origin)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			row.id, f.convID, sessionID, row.role, row.id+" preservationneedle", base.Add(time.Duration(i)*time.Second), 11+i, toolCalls, "", row.status, midTurn, memory.OriginChannel)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := mem.RecordToolCall(f.convID, "early", "call-early", "session_close", `{}`); err != nil {
		t.Fatal(err)
	}
	return f
}

type preservedMessageEvidence struct {
	conversationID, role, content, timestamp, toolCalls, toolCallID, origin string
	tokenCount, midTurn                                                     int
}

func (f *sessionPreservationFixture) messageEvidence(t *testing.T) map[string]preservedMessageEvidence {
	t.Helper()
	rows, err := f.mem.DB().Query(`SELECT id, conversation_id, role, content, CAST(timestamp AS TEXT),
		COALESCE(tool_calls, ''), COALESCE(tool_call_id, ''), COALESCE(origin, ''), token_count, mid_turn
		FROM messages WHERE conversation_id = ?`, f.convID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := make(map[string]preservedMessageEvidence)
	for rows.Next() {
		var id string
		var evidence preservedMessageEvidence
		if err := rows.Scan(&id, &evidence.conversationID, &evidence.role, &evidence.content, &evidence.timestamp,
			&evidence.toolCalls, &evidence.toolCallID, &evidence.origin, &evidence.tokenCount, &evidence.midTurn); err != nil {
			t.Fatal(err)
		}
		out[id] = evidence
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
