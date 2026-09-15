package agent

import (
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

func newTestLoop(mem MemoryStore, archiver SessionArchiver) *Loop {
	return &Loop{logger: slog.Default(), memory: mem, archiver: archiver}
}

func newSessionTestLoop(t *testing.T) (*Loop, *memory.SQLiteStore, *memory.ArchiveStore) {
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
	archive, err := memory.NewArchiveStoreFromDB(mem.DB(), nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return newTestLoop(mem, memory.NewArchiveAdapter(archive, mem, slog.Default())), mem, archive
}

func TestCloseSession(t *testing.T) {
	for _, tc := range []struct{ name, reason, carryForward, wantReason string }{
		{"carry forward", "topic change", "User was discussing greetings.", "topic change"},
		{"default reason", "", "Notes here.", "close"},
		{"no handoff", "done", "", "done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loop, mem, archive := newSessionTestLoop(t)
			oldID := loop.archiver.EnsureSession("conv1")
			for _, content := range []string{"hello", "hi there"} {
				if err := mem.AddMessage("conv1", "user", content, memory.OriginChannel); err != nil {
					t.Fatal(err)
				}
			}
			before := mem.GetMessages("conv1")
			if err := loop.CloseSession("conv1", tc.reason, tc.carryForward); err != nil {
				t.Fatal(err)
			}
			closed, err := archive.GetSession(oldID)
			if err != nil || closed.EndedAt == nil || closed.EndReason != tc.wantReason {
				t.Fatalf("closed session=%+v error=%v", closed, err)
			}
			if current := loop.archiver.ActiveSessionID("conv1"); current == "" || current == oldID {
				t.Fatalf("missing successor: %q", current)
			}
			transcript, err := archive.GetSessionTranscript(oldID)
			if err != nil || len(transcript) != len(before) {
				t.Fatalf("transcript=%+v error=%v", transcript, err)
			}
			for i, msg := range before {
				if transcript[i].ID != msg.ID || transcript[i].Content != msg.Content || transcript[i].Origin != msg.Origin {
					t.Fatalf("original row changed: %+v -> %+v", msg, transcript[i])
				}
			}
			active := mem.GetMessages("conv1")
			if tc.carryForward == "" {
				if len(active) != 0 {
					t.Fatalf("unexpected active messages: %+v", active)
				}
			} else if len(active) != 1 || active[0].Role != "system" || active[0].Content != "[Session Handoff]\n"+tc.carryForward {
				t.Fatalf("handoff=%+v", active)
			}
		})
	}
}

func TestCheckpointSession(t *testing.T) {
	for _, tc := range []struct {
		name, label string
		empty       bool
	}{
		{"labeled", "pre-refactor", false}, {"unlabeled", "", false}, {"empty", "empty", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loop, mem, archive := newSessionTestLoop(t)
			sid := loop.archiver.EnsureSession("conv1")
			if !tc.empty {
				if err := mem.AddMessage("conv1", "user", "checkpoint evidence", memory.OriginChannel); err != nil {
					t.Fatal(err)
				}
			}
			before := mem.GetMessages("conv1")
			err := loop.CheckpointSession("conv1", tc.label)
			if tc.empty {
				if err == nil {
					t.Fatal("empty checkpoint succeeded")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if after := mem.GetMessages("conv1"); !reflect.DeepEqual(before, after) {
				t.Fatalf("checkpoint changed active messages: %+v -> %+v", before, after)
			}
			session, err := archive.GetSession(sid)
			if err != nil || session.EndedAt != nil || loop.archiver.ActiveSessionID("conv1") != sid {
				t.Fatalf("checkpoint closed session: %+v error=%v", session, err)
			}
			var label string
			if err := mem.DB().QueryRow(`SELECT label FROM session_checkpoints WHERE session_id = ?`, sid).Scan(&label); err != nil {
				t.Fatal(err)
			}
			if label != tc.label {
				t.Fatalf("checkpoint label=%q want=%q", label, tc.label)
			}
		})
	}
}

func TestSplitSession(t *testing.T) {
	for _, tc := range []struct {
		name           string
		count, atIndex int
		atMessage      string
		prefix         int
		errorText      string
	}{
		{"negative index", 5, -2, "", 3, ""},
		{"content match", 5, 0, "msg3", 2, ""},
		{"last message", 3, -1, "", 2, ""},
		{"out of range", 1, -5, "", 0, "out of range"},
		{"no match", 2, 0, "missing", 0, "no message found"},
		{"empty", 0, -1, "", 0, "no messages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loop, mem, archive := newSessionTestLoop(t)
			sid := loop.archiver.EnsureSession("conv1")
			for i := range tc.count {
				content := "msg" + string(rune('1'+i))
				if err := mem.AddMessage("conv1", "user", content, memory.OriginChannel); err != nil {
					t.Fatal(err)
				}
			}
			before := mem.GetMessages("conv1")
			err := loop.SplitSession("conv1", tc.atIndex, tc.atMessage)
			if tc.errorText != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errorText) {
					t.Fatalf("error=%v want %q", err, tc.errorText)
				}
				if after := mem.GetMessages("conv1"); !reflect.DeepEqual(before, after) {
					t.Fatal("failed split changed messages")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			closed, err := archive.GetSession(sid)
			if err != nil || closed.EndedAt == nil || closed.EndReason != "split" {
				t.Fatalf("closed session=%+v error=%v", closed, err)
			}
			transcript, err := archive.GetSessionTranscript(sid)
			if err != nil || len(transcript) != tc.prefix {
				t.Fatalf("prefix=%+v error=%v", transcript, err)
			}
			active := mem.GetMessages("conv1")
			if len(active) != tc.count-tc.prefix {
				t.Fatalf("suffix=%+v", active)
			}
			for i, message := range append(transcript, active...) {
				if message.ID != before[i].ID || message.Content != before[i].Content || !message.Timestamp.Equal(before[i].Timestamp) {
					t.Fatalf("split rewrote row: %+v -> %+v", before[i], message)
				}
			}
			if current := loop.archiver.ActiveSessionID("conv1"); current == "" || current == sid {
				t.Fatalf("missing split successor: %q", current)
			}
		})
	}
}

func TestSessionTransitionsClearPersistedCapabilityTags(t *testing.T) {
	for _, operation := range []string{"close", "split", "reset"} {
		t.Run(operation, func(t *testing.T) {
			loop, mem, _ := newSessionTestLoop(t)
			store := newTestCapStore(t)
			loop.SetCapabilityTagStore(store)
			if err := store.SaveTags("conv1", []string{"forge", "web"}); err != nil {
				t.Fatal(err)
			}
			for _, content := range []string{"first", "second", "third"} {
				if err := mem.AddMessage("conv1", "user", content, memory.OriginChannel); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			switch operation {
			case "close":
				err = loop.CloseSession("conv1", "topic change", "Carry this forward.")
			case "split":
				err = loop.SplitSession("conv1", -1, "")
			case "reset":
				err = loop.ResetConversation("conv1")
			}
			if err != nil {
				t.Fatal(err)
			}
			tags, err := store.LoadTags("conv1")
			if err != nil || len(tags) != 0 {
				t.Fatalf("tags=%v error=%v", tags, err)
			}
		})
	}
}

func TestSessionOperationsWithoutArchiver(t *testing.T) {
	for _, operation := range []string{"checkpoint", "split", "reset", "close"} {
		t.Run(operation, func(t *testing.T) {
			mem := memory.NewStore(100) // Same ephemeral backend used by thane ask.
			loop := newTestLoop(mem, nil)
			if err := mem.AddMessage("conv1", "user", "one-shot context", ""); err != nil {
				t.Fatal(err)
			}
			var err error
			switch operation {
			case "checkpoint":
				err = loop.CheckpointSession("conv1", "test")
			case "split":
				err = loop.SplitSession("conv1", -1, "")
			case "reset":
				err = loop.ResetConversation("conv1")
			case "close":
				err = loop.CloseSession("conv1", "done", "")
			}
			if operation == "checkpoint" || operation == "split" {
				if err == nil || !strings.Contains(err.Error(), "no archiver") {
					t.Fatalf("error=%v want missing archiver", err)
				}
				if len(mem.GetMessages("conv1")) != 1 {
					t.Fatal("unsupported durable operation changed ephemeral context")
				}
			} else if err != nil || len(mem.GetMessages("conv1")) != 0 {
				t.Fatalf("ephemeral reset: error=%v messages=%v", err, mem.GetMessages("conv1"))
			}
		})
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
