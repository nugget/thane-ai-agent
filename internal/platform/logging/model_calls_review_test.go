package logging

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// TestModelCallTurn_FinishSnapshotsOutcomes pins that a queued outcome
// row owns its ledger: the drain marshals it later, so a caller reusing
// the map or a nested target map after Finish returns must not change
// what is stored.
func TestModelCallTurn_FinishSnapshotsOutcomes(t *testing.T) {
	db := openTestDB(t)
	s := newModelCallStore(db, ModelCallStoreConfig{InstanceID: "test", Logger: slog.New(slog.DiscardHandler)})

	ledger := map[string]ToolOutcomeRecord{
		"publish_output_digest": {
			Calls: 1, Successes: 1,
			Targets: map[string]ToolTargetRecord{"core:digest.md": {Calls: 1, Successes: 1}},
		},
	}
	want := map[string]ToolOutcomeRecord{
		"publish_output_digest": {
			Calls: 1, Successes: 1,
			Targets: map[string]ToolTargetRecord{"core:digest.md": {Calls: 1, Successes: 1}},
		},
	}
	if !s.Turn("r_alice").Finish(ModelTurn{Completed: true, ToolOutcomes: ledger, At: time.Now()}) {
		t.Fatal("Finish not queued")
	}
	// The caller reuses its ledger before the drain has run.
	ledger["publish_output_digest"].Targets["core:digest.md"] = ToolTargetRecord{Calls: 9, Failures: 9}
	ledger["doc_write"] = ToolOutcomeRecord{Calls: 7}
	s.start()
	s.Close()

	var raw string
	if err := db.QueryRow(`SELECT tool_outcomes FROM log_model_turns WHERE request_id = 'r_alice'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var got map[string]ToolOutcomeRecord
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stored outcomes = %+v, want %+v: a write after Finish reached the row", got, want)
	}
}

// TestModelCallTurn_QueuedRowHoldsNoBlobBytes pins that a queued call
// carries bytes only for blobs new to its turn, so repeating the same
// history does not hold a full copy per queue entry.
func TestModelCallTurn_QueuedRowHoldsNoBlobBytes(t *testing.T) {
	db := openTestDB(t)
	s := newModelCallStore(db, ModelCallStoreConfig{InstanceID: "test", Logger: slog.New(slog.DiscardHandler)})
	turn := s.Turn("r_alice")
	for range 2 {
		if !turn.Capture(fixtureCall()) {
			t.Fatal("capture not queued")
		}
	}

	first, second := <-s.ch, <-s.ch
	for i, item := range []modelCallItem{first, second} {
		if item.call.hashed.blobs != nil {
			t.Errorf("item %d: queued row still holds the blob set", i+1)
		}
		if len(item.call.refs) == 0 {
			t.Errorf("item %d: queued row lost its blob references", i+1)
		}
	}
	if len(first.blobs) == 0 {
		t.Error("first call queued no blob bytes, want its whole input")
	}
	for _, b := range first.blobs {
		if len(b.data) == 0 {
			t.Errorf("first call's blob %s has no bytes", b.hash)
		}
	}
	if len(second.blobs) != 0 {
		t.Errorf("identical second call queued %d blobs, want 0", len(second.blobs))
	}
	s.Close()
}

// TestModelCallStore_MissingBlobMarksIncomplete pins that a row is
// stored complete only when every blob it references exists. The
// second call's blobs were suppressed by the turn's sent set, and the
// first call never reached the writer, so nothing poisons the request:
// only the existence check can flag the row.
func TestModelCallStore_MissingBlobMarksIncomplete(t *testing.T) {
	db := openTestDB(t)
	s := newModelCallStore(db, ModelCallStoreConfig{InstanceID: "test", Logger: slog.New(slog.DiscardHandler)})
	turn := s.Turn("r_alice")
	for range 2 {
		if !turn.Capture(fixtureCall()) {
			t.Fatal("capture not queued")
		}
	}
	<-s.ch // the first call and its blobs are lost before the writer sees them
	s.start()
	s.Close()

	if got := countRows(t, db, `SELECT COUNT(*) FROM log_model_calls WHERE request_id = 'r_alice' AND input_incomplete = 1`); got != 1 {
		t.Errorf("input_incomplete rows = %d, want 1", got)
	}
	if got := s.Stats().MissingBlobRows; got != 1 {
		t.Errorf("MissingBlobRows = %d, want 1", got)
	}

	// A call whose blobs were all stored stays complete.
	ok := newModelCallStore(db, ModelCallStoreConfig{InstanceID: "test", Logger: slog.New(slog.DiscardHandler)})
	ok.start()
	if !ok.Turn("r_bob").Capture(fixtureCall()) {
		t.Fatal("capture not queued")
	}
	ok.Close()
	if got := countRows(t, db, `SELECT COUNT(*) FROM log_model_calls WHERE request_id = 'r_bob' AND input_incomplete = 0`); got != 1 {
		t.Errorf("complete rows for r_bob = %d, want 1", got)
	}
}

// TestModelCallStore_StalledWritesAreBounded pins that a write which
// cannot get a connection fails within its deadline instead of stopping
// the drain, and that Close returns once its grace runs out even when
// every queued write is still waiting.
func TestModelCallStore_StalledWritesAreBounded(t *testing.T) {
	tests := []struct {
		name         string
		writeTimeout time.Duration
		closeGrace   time.Duration
	}{
		{name: "each write times out", writeTimeout: 50 * time.Millisecond, closeGrace: time.Minute},
		{name: "close cancels what remains", writeTimeout: time.Minute, closeGrace: 100 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			db.SetMaxOpenConns(1)
			// Hold the only connection so every write waits for the pool.
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()

			s := newModelCallStore(db, ModelCallStoreConfig{InstanceID: "test", Logger: slog.New(slog.DiscardHandler)})
			s.writeTimeout, s.closeGrace = tt.writeTimeout, tt.closeGrace
			turn := s.Turn("r_alice")
			for range 3 {
				if !turn.Capture(fixtureCall()) {
					t.Fatal("capture not queued")
				}
			}
			s.start()

			closed := make(chan struct{})
			go func() { s.Close(); close(closed) }()
			select {
			case <-closed:
			case <-time.After(10 * time.Second):
				t.Fatal("Close did not return while the pool was exhausted")
			}
			if got := s.Stats().WriteErrors; got != 3 {
				t.Errorf("write errors = %d, want 3", got)
			}
		})
	}
}

// TestModelCallStore_UnsentSectionsMarkInputIncomplete pins that a call
// is stored complete only when the captured prompt is what the provider
// sent. A provider that transmits declared sections (Anthropic) received
// something other than the Content stored once those sections cannot be
// located in it; a provider that sends Content received exactly what was
// stored.
func TestModelCallStore_UnsentSectionsMarkInputIncomplete(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		unlocatable    bool
		unsectioned    bool // the first system message declares no sections
		laterSections  bool // a later system message declares sections
		wantIncomplete bool
	}{
		{name: "anthropic with unlocatable sections", provider: "anthropic", unlocatable: true, wantIncomplete: true},
		{name: "openai_compat with unlocatable sections", provider: "openai_compat", unlocatable: true},
		{name: "anthropic with located sections", provider: "anthropic"},
		{name: "anthropic sending a later message's sections", provider: "anthropic", unsectioned: true, laterSections: true, wantIncomplete: true},
		{name: "openai_compat with a later message's sections", provider: "openai_compat", unsectioned: true, laterSections: true},
		{name: "anthropic with no sections anywhere", provider: "anthropic", unsectioned: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			s := NewModelCallStore(db, ModelCallStoreConfig{InstanceID: "test", Logger: slog.New(slog.DiscardHandler)})
			c := fixtureCall()
			c.Profile.Provider = tt.provider
			if tt.unlocatable {
				c.Messages[0].Content = "rewritten prompt"
			}
			if tt.unsectioned {
				c.Messages[0].Sections = nil
			}
			if tt.laterSections {
				c.Messages = append(c.Messages, llm.Message{
					Role: "system", Content: "Late guidance.",
					Sections: []llm.PromptSection{{Name: "LATE GUIDANCE", Content: "Late guidance."}},
				})
			}
			if !s.Turn("r_alice").Capture(c) {
				t.Fatal("capture not queued")
			}
			s.Close()
			got := countRows(t, db, `SELECT COUNT(*) FROM log_model_calls WHERE input_incomplete = 1`) == 1
			if got != tt.wantIncomplete {
				t.Errorf("input_incomplete = %v, want %v", got, tt.wantIncomplete)
			}
		})
	}
}

// TestModelCallStore_TurnRowRecordsLostCalls pins that a turn's outcome
// row says when its capture is incomplete, including when the lost call
// was the last one and no later call exists to carry input_incomplete,
// and that it records every call the turn tried to capture.
func TestModelCallStore_TurnRowRecordsLostCalls(t *testing.T) {
	tests := []struct {
		name           string
		failFinalWrite bool
		wantIncomplete bool
	}{
		{name: "every call stored", wantIncomplete: false},
		{name: "the final call's write failed", failFinalWrite: true, wantIncomplete: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			if tt.failFinalWrite {
				if _, err := db.Exec(`CREATE TRIGGER inject_failure BEFORE INSERT ON log_model_calls
					WHEN NEW.request_id = 'r_alice' AND NEW.call_seq = 2
					BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			s := NewModelCallStore(db, ModelCallStoreConfig{InstanceID: "test", Logger: slog.New(slog.DiscardHandler)})
			turn := s.Turn("r_alice")
			for range 2 {
				if !turn.Capture(fixtureCall()) {
					t.Fatal("capture not queued")
				}
			}
			if !turn.Finish(ModelTurn{Completed: true, At: time.Now()}) {
				t.Fatal("Finish not queued")
			}
			s.Close()

			var calls int
			var incomplete bool
			if err := db.QueryRow(`SELECT call_count, capture_incomplete FROM log_model_turns WHERE request_id = 'r_alice'`).
				Scan(&calls, &incomplete); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Errorf("call_count = %d, want 2", calls)
			}
			if incomplete != tt.wantIncomplete {
				t.Errorf("capture_incomplete = %v, want %v", incomplete, tt.wantIncomplete)
			}
		})
	}
}

// TestModelCallTurn_DroppedCallMarksTurnIncomplete pins that a call
// rejected by a full queue, which never reaches the writer and so can
// poison nothing, still leaves the turn's outcome row incomplete.
func TestModelCallTurn_DroppedCallMarksTurnIncomplete(t *testing.T) {
	db := openTestDB(t)
	s := newModelCallStore(db, ModelCallStoreConfig{InstanceID: "test", QueueSize: 2, Logger: slog.New(slog.DiscardHandler)})
	turn := s.Turn("r_alice")
	for i := range 3 {
		if queued := turn.Capture(fixtureCall()); queued != (i < 2) {
			t.Fatalf("capture %d queued = %v, want %v", i+1, queued, i < 2)
		}
	}
	s.start()
	deadline := time.Now().Add(5 * time.Second)
	for countRows(t, db, `SELECT COUNT(*) FROM log_model_calls WHERE request_id = 'r_alice'`) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("drain did not commit the queued calls")
		}
		time.Sleep(time.Millisecond)
	}
	if !turn.Finish(ModelTurn{Completed: true, At: time.Now()}) {
		t.Fatal("Finish not queued")
	}
	s.Close()

	var calls int
	var incomplete bool
	if err := db.QueryRow(`SELECT call_count, capture_incomplete FROM log_model_turns WHERE request_id = 'r_alice'`).
		Scan(&calls, &incomplete); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || !incomplete {
		t.Errorf("call_count = %d, capture_incomplete = %v; want 3, true", calls, incomplete)
	}
}

// TestModelCallStore_EvictedMarkerStillLeavesTurnIncomplete pins that a
// turn which lost a call reads incomplete even after the failure marker
// aged out before Finish: the stored-row count, not the marker, is what
// decides.
func TestModelCallStore_EvictedMarkerStillLeavesTurnIncomplete(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(`CREATE TRIGGER inject_failure BEFORE INSERT ON log_model_calls
		WHEN NEW.request_id = 'r_alice' AND NEW.call_seq = 2
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	s := newModelCallStore(db, ModelCallStoreConfig{InstanceID: "test", Logger: slog.New(slog.DiscardHandler)})
	var clock atomic.Int64
	clock.Store(time.Unix(1_800_000_000, 0).UnixNano())
	s.now = func() time.Time { return time.Unix(0, clock.Load()) }
	s.start()

	alice := s.Turn("r_alice")
	for range 2 {
		if !alice.Capture(fixtureCall()) {
			t.Fatal("capture not queued")
		}
	}
	waitFor(t, "the second call's write to fail", func() bool { return s.Stats().WriteErrors == 1 })

	// An hour and more passes, and another request's write sweeps the
	// idle marker away before alice finishes.
	clock.Add(int64(poisonedIdleTTL + 2*poisonedSweepInterval))
	if !s.Turn("r_bob").Capture(fixtureCall()) {
		t.Fatal("capture not queued")
	}
	waitFor(t, "bob's call to commit", func() bool {
		return countRows(t, db, `SELECT COUNT(*) FROM log_model_calls WHERE request_id = 'r_bob'`) == 1
	})
	if !alice.Finish(ModelTurn{Completed: true, At: time.Now()}) {
		t.Fatal("Finish not queued")
	}
	s.Close()

	if len(s.poisoned) != 0 {
		t.Fatalf("poisoned = %v, want the marker swept before Finish (test precondition)", s.poisoned)
	}
	var calls int
	var incomplete bool
	if err := db.QueryRow(`SELECT call_count, capture_incomplete FROM log_model_turns WHERE request_id = 'r_alice'`).
		Scan(&calls, &incomplete); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !incomplete {
		t.Errorf("call_count = %d, capture_incomplete = %v; want 2, true", calls, incomplete)
	}
}

// waitFor polls cond until it holds or five seconds pass.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}
