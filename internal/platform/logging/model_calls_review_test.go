package logging

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"testing"
	"time"
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
