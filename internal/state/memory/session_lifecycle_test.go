package memory

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestSessionCheckpointBookmarksPreservedRows(t *testing.T) {
	adapter, archive, store := newTestAdapter(t)
	for _, content := range []string{"compacted source", "active message"} {
		if err := store.AddMessage("conv", "user", content, OriginChannel); err != nil {
			t.Fatal(err)
		}
	}
	before, err := store.CurrentSessionMessages("conv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE messages SET status = 'compacted' WHERE id = ?`, before[0].ID); err != nil {
		t.Fatal(err)
	}
	// The first checkpoint may precede the first Run's deferred EnsureSession.
	if err := adapter.CheckpointSession("conv", "before reset"); err != nil {
		t.Fatal(err)
	}
	sessionID := adapter.ActiveSessionID("conv")
	if sessionID == "" {
		t.Fatal("checkpoint did not establish a session")
	}
	var label, allJSON, activeJSON string
	if err := store.DB().QueryRow(`SELECT label, message_ids, active_message_ids FROM session_checkpoints WHERE session_id = ?`,
		sessionID).Scan(&label, &allJSON, &activeJSON); err != nil {
		t.Fatal(err)
	}
	var all, active []string
	if err := json.Unmarshal([]byte(allJSON), &all); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(activeJSON), &active); err != nil {
		t.Fatal(err)
	}
	if label != "before reset" || !reflect.DeepEqual(all, []string{before[0].ID, before[1].ID}) || !reflect.DeepEqual(active, []string{before[1].ID}) {
		t.Fatalf("bookmark = %q %v %v", label, all, active)
	}
	if got := store.GetMessages("conv"); len(got) != 1 || got[0].ID != before[1].ID {
		t.Fatalf("checkpoint changed active context: %+v", got)
	}
	if err := adapter.ResetSession("conv", "reset", ""); err != nil {
		t.Fatal(err)
	}
	transcript, err := archive.GetSessionTranscript(sessionID)
	if err != nil || len(transcript) != 2 {
		t.Fatalf("bookmarked transcript after reset = %+v, %v", transcript, err)
	}
}

func TestSessionTransitionRollsBackBeforePublishing(t *testing.T) {
	for _, operation := range []string{"reset", "close", "split", "checkpoint"} {
		t.Run(operation, func(t *testing.T) {
			adapter, archive, store := newTestAdapter(t)
			for _, content := range []string{"first", "second"} {
				if err := store.AddMessage("conv", "user", content, OriginChannel); err != nil {
					t.Fatal(err)
				}
			}
			sid, err := adapter.StartSession("conv")
			if err != nil {
				t.Fatal(err)
			}
			messages, err := store.CurrentSessionMessages("conv")
			if err != nil {
				t.Fatal(err)
			}
			callbackCount := 0
			archive.SetSessionCloseCallback(func(string, string) { callbackCount++ })
			trigger := `CREATE TRIGGER reject_transition BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT, 'injected boundary failure'); END`
			if operation == "checkpoint" {
				trigger = `CREATE TRIGGER reject_transition BEFORE INSERT ON session_checkpoints BEGIN SELECT RAISE(ABORT, 'injected bookmark failure'); END`
			}
			if _, err := store.DB().Exec(trigger); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "reset":
				err = adapter.ResetSession("conv", "reset", "handoff")
			case "close":
				err = adapter.CloseConversation("conv", "delegate")
			case "split":
				err = adapter.SplitSession("conv", messages[1].ID)
			case "checkpoint":
				err = adapter.CheckpointSession("conv", "bookmark")
			}
			if err == nil {
				t.Fatal("transition ignored injected failure")
			}
			if got := adapter.ActiveSessionID("conv"); got != sid || callbackCount != 0 {
				t.Fatalf("published failed transition: current=%q callbacks=%d", got, callbackCount)
			}
			if got := store.GetMessages("conv"); len(got) != len(messages) {
				t.Fatalf("rollback lost active messages: %+v", got)
			}
			var claimed, bookmarks, sessions int
			for query, target := range map[string]*int{
				`SELECT COUNT(*) FROM messages WHERE session_id IS NOT NULL`: &claimed,
				`SELECT COUNT(*) FROM session_checkpoints`:                   &bookmarks,
				`SELECT COUNT(*) FROM sessions`:                              &sessions,
			} {
				if err := store.DB().QueryRow(query).Scan(target); err != nil {
					t.Fatal(err)
				}
			}
			if claimed != 0 || bookmarks != 0 || sessions != 1 {
				t.Fatalf("partial durable writes: claimed=%d bookmarks=%d sessions=%d", claimed, bookmarks, sessions)
			}
		})
	}
}

func TestSessionSplitMovesMessagesAndPreservesExecutionProvenance(t *testing.T) {
	adapter, archive, store := newTestAdapter(t)
	for _, content := range []string{"compacted prefix", "retained active", "retained reply"} {
		if err := store.AddMessage("conv", "user", content, OriginChannel); err != nil {
			t.Fatal(err)
		}
	}
	sid, err := adapter.StartSession("conv")
	if err != nil {
		t.Fatal(err)
	}
	messages, err := store.CurrentSessionMessages("conv")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordToolCall("conv", messages[2].ID, "retained-call", "example", `{}`); err != nil {
		t.Fatal(err)
	}
	if err := archive.ArchiveIterations([]ArchivedIteration{{
		SessionID: sid, Model: "test-model", StartedAt: messages[2].Timestamp,
		InputTokens: 11, OutputTokens: 7, ToolCallCount: 1, HasToolCalls: true,
		ToolCallIDs: []string{"retained-call"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE messages SET status = 'compacted' WHERE id = ?`, messages[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CheckpointSession("conv", "claim ownership"); err != nil {
		t.Fatal(err)
	}
	callbackCount := 0
	archive.SetSessionCloseCallback(func(closedID, reason string) {
		callbackCount++
		if closedID != sid || reason != "split" {
			t.Errorf("close callback = %q %q", closedID, reason)
		}
		// This read uses another connection and sees only committed state.
		closed, err := archive.GetSession(closedID)
		if err != nil || closed.EndedAt == nil || adapter.ActiveSessionID("conv") == closedID {
			t.Errorf("callback observed uncommitted transition: %+v, %v", closed, err)
		}
	})
	if err := adapter.SplitSession("conv", messages[1].ID); err != nil {
		t.Fatal(err)
	}
	next := adapter.ActiveSessionID("conv")
	if next == "" || next == sid || callbackCount != 1 {
		t.Fatalf("split transition = %q callbacks=%d", next, callbackCount)
	}
	for _, message := range messages[1:] {
		var gotSession, status string
		if err := store.DB().QueryRow(`SELECT session_id, status FROM messages WHERE id = ?`, message.ID).Scan(&gotSession, &status); err != nil {
			t.Fatal(err)
		}
		if gotSession != next || status != "active" {
			t.Errorf("retained message = session %q status %q", gotSession, status)
		}
	}
	if err := store.CompleteToolCall("retained-call", "completed after split", ""); err != nil {
		t.Fatal(err)
	}
	var callSession, callMessage, callResult, callStatus string
	var iterationIndex int
	if err := store.DB().QueryRow(`SELECT session_id, message_id, result, status, iteration_index FROM tool_calls WHERE id = 'retained-call'`).Scan(
		&callSession, &callMessage, &callResult, &callStatus, &iterationIndex); err != nil {
		t.Fatal(err)
	}
	if callSession != sid || callMessage != messages[2].ID || callResult != "completed after split" || callStatus != "archived" || iterationIndex != 0 {
		t.Fatalf("original tool execution = session %q message %q result %q status %q iteration %d", callSession, callMessage, callResult, callStatus, iterationIndex)
	}
	if calls, err := archive.GetSessionToolCalls(next); err != nil || len(calls) != 0 {
		t.Fatalf("successor acquired historical executions: %+v, %v", calls, err)
	}
	if iterations, err := archive.GetSessionIterations(next); err != nil || len(iterations) != 0 {
		t.Fatalf("successor duplicated historical usage: %+v, %v", iterations, err)
	}
	// Reusing index zero in the successor must not change which original
	// execution the retained message's tool call resolves to.
	if err := archive.ArchiveIterations([]ArchivedIteration{{SessionID: next, Model: "successor-model", StartedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	var model string
	var inputTokens, outputTokens int
	if err := store.DB().QueryRow(`SELECT i.model, i.input_tokens, i.output_tokens FROM tool_calls t
		JOIN archive_iterations i ON i.session_id = t.session_id AND i.iteration_index = t.iteration_index
		WHERE t.id = 'retained-call'`).Scan(&model, &inputTokens, &outputTokens); err != nil {
		t.Fatal(err)
	}
	if model != "test-model" || inputTokens != 11 || outputTokens != 7 {
		t.Fatalf("execution resolved to the wrong iteration: model=%q usage=%d/%d", model, inputTokens, outputTokens)
	}
}

func TestSessionSplitRejectsCompactedSuffixWithoutChangingContext(t *testing.T) {
	adapter, _, store := newTestAdapter(t)
	for _, content := range []string{CompactionSummaryPrefix + " prior history", "compacted source", "recent message"} {
		if err := store.AddMessage("conv", "system", content, OriginInternal); err != nil {
			t.Fatal(err)
		}
	}
	sid, err := adapter.StartSession("conv")
	if err != nil {
		t.Fatal(err)
	}
	messages, err := store.CurrentSessionMessages("conv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE messages SET status = 'compacted' WHERE id = ?`, messages[1].ID); err != nil {
		t.Fatal(err)
	}
	before := store.GetMessages("conv")
	if err := adapter.SplitSession("conv", messages[1].ID); err == nil {
		t.Fatal("split accepted a retained compacted source without its summary")
	}
	if after := store.GetMessages("conv"); !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected split changed active context: before=%+v after=%+v", before, after)
	}
	if got := adapter.ActiveSessionID("conv"); got != sid {
		t.Fatalf("rejected split changed session from %q to %q", sid, got)
	}
	var sessions int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 {
		t.Fatalf("rejected split left %d sessions", sessions)
	}
}

func TestCompactionCannotRestoreClosedSessionContext(t *testing.T) {
	adapter, _, store := newTestAdapter(t)
	if err := store.AddMessage("conv", "user", "old context", OriginChannel); err != nil {
		t.Fatal(err)
	}
	messages := store.GetMessages("conv")
	if err := adapter.ResetSession("conv", "reset", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyCompaction("conv", []string{messages[0].ID}, "stale summary", time.Now()); err == nil {
		t.Fatal("compaction revived closed session context")
	}
	if got := store.GetMessages("conv"); len(got) != 0 {
		t.Fatalf("stale summary entered fresh session: %+v", got)
	}
	var status string
	if err := store.DB().QueryRow(`SELECT status FROM messages WHERE id = ?`, messages[0].ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "archived" {
		t.Fatalf("compaction changed closed source status to %q", status)
	}
}

func TestCloseConversationIsIdempotent(t *testing.T) {
	adapter, archive, store := newTestAdapter(t)
	if err := store.AddMessage("conv", "user", "completed delegate", OriginInternal); err != nil {
		t.Fatal(err)
	}
	callbacks := 0
	archive.SetSessionCloseCallback(func(string, string) { callbacks++ })
	for range 2 {
		if err := adapter.CloseConversation("conv", "delegate"); err != nil {
			t.Fatal(err)
		}
	}
	var sessions int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if callbacks != 1 || sessions != 1 || adapter.ActiveSessionID("conv") != "" {
		t.Fatalf("duplicate close changed durable state: callbacks=%d sessions=%d", callbacks, sessions)
	}
}

func TestRecordSessionToolCallRejectsWrongConversation(t *testing.T) {
	adapter, _, store := newTestAdapter(t)
	for _, conversation := range []string{"owner", "other"} {
		if err := store.AddMessage(conversation, "user", "message", OriginChannel); err != nil {
			t.Fatal(err)
		}
	}
	sid, err := adapter.StartSession("owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSessionToolCall("other", sid, "", "mismatched-call", "example", `{}`); err == nil {
		t.Fatal("recorded tool call against another conversation's session")
	}
	var calls int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls`).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("rejected tool call left %d rows", calls)
	}
}
