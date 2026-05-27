package memory

import (
	"strings"
	"testing"
)

// TestSearchSessions_FindsByTitleSummaryTags exercises the
// sessions_fts virtual table end-to-end: a session with a summary,
// a title, and tags is found by a query matching any of those
// columns. BM25 ranking orders the most relevant hit first.
func TestSearchSessions_FindsByTitleSummaryTags(t *testing.T) {
	store := newTestArchiveStore(t)
	if !store.FTSEnabled() {
		t.Skip("FTS5 not available")
	}

	// Create three ended sessions with distinct distilled content.
	mk := func(convID, title, summary, tags string) string {
		sess, err := store.StartSession(convID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.EndSession(sess.ID, "reset"); err != nil {
			t.Fatal(err)
		}
		meta := &SessionMetadata{OneLiner: summary}
		if err := store.SetSessionMetadata(sess.ID, meta, title, splitTags(tags)); err != nil {
			t.Fatal(err)
		}
		return sess.ID
	}

	mk("conv-1",
		"Pool heater scheduling",
		"User asked about reprogramming the pool heater for shoulder season.",
		"pool,heater,hvac")
	mk("conv-2",
		"Garage door fix",
		"Replaced the relay in the opener.",
		"garage,maintenance")
	poolSchedule := mk("conv-3",
		"Pool patio lighting",
		"Discussed dimmer options for the patio lights near the pool.",
		"pool,patio,lighting")

	// "pool heater" should rank the first session above the patio-lighting
	// one because both columns (title+summary) hit on the phrase, and
	// above the garage-door one because that one doesn't match at all.
	results, err := store.SearchSessions("pool heater", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one session match")
	}
	if results[0].Title != "Pool heater scheduling" {
		t.Errorf("top hit = %q, expected the heater-titled session ranked first", results[0].Title)
	}

	// Tag-only query also matches.
	tagResults, err := store.SearchSessions("patio", 5)
	if err != nil {
		t.Fatal(err)
	}
	foundPatio := false
	for _, r := range tagResults {
		if r.SessionID == poolSchedule {
			foundPatio = true
			break
		}
	}
	if !foundPatio {
		t.Error("tag-only match on 'patio' did not surface the patio-lighting session")
	}
}

// TestSearchSessions_BackfillOnFirstInit verifies the migration
// path: sessions that already existed before sessions_fts was
// created get indexed by the one-shot backfill in trySetupSessionsFTS.
// Without the backfill, only sessions written after the index
// creation would be searchable.
func TestSearchSessions_BackfillOnFirstInit(t *testing.T) {
	dbPath := t.TempDir() + "/backfill.db"

	// Pass 1: create a session WITHOUT the FTS index. This simulates a
	// pre-Finding-2 database. We seed a session directly into the raw
	// sessions table, bypassing the FTS triggers entirely by dropping
	// them after the store wires them up.
	store1, err := NewArchiveStore(dbPath, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !store1.FTSEnabled() {
		store1.Close()
		t.Skip("FTS5 not available")
	}

	// Drop the FTS table and triggers to simulate a pre-existing db
	// that has sessions but no FTS infrastructure yet.
	db := store1.DB()
	for _, stmt := range []string{
		"DROP TRIGGER IF EXISTS sessions_fts_ai",
		"DROP TRIGGER IF EXISTS sessions_fts_ad",
		"DROP TRIGGER IF EXISTS sessions_fts_au",
		"DROP TABLE IF EXISTS sessions_fts",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("teardown %s: %v", stmt, err)
		}
	}

	// Seed a session that no FTS trigger sees.
	sess, err := store1.StartSession("conv-legacy")
	if err != nil {
		t.Fatal(err)
	}
	if err := store1.EndSession(sess.ID, "reset"); err != nil {
		t.Fatal(err)
	}
	if err := store1.SetSessionMetadata(sess.ID,
		&SessionMetadata{OneLiner: "Legacy pre-FTS session about kitchen timer."},
		"Kitchen timer setup",
		[]string{"kitchen", "automation"},
	); err != nil {
		t.Fatal(err)
	}
	store1.Close()

	// Pass 2: re-open the store. The constructor calls
	// trySetupSessionsFTS, which should recreate the FTS table,
	// install triggers, and backfill the legacy session.
	store2, err := NewArchiveStore(dbPath, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store2.Close() })

	results, err := store2.SearchSessions("kitchen timer", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("backfill did not index the legacy session — search returned no hits")
	}
	if results[0].SessionID != sess.ID {
		t.Errorf("expected backfilled session %s, got %s",
			ShortID(sess.ID), ShortID(results[0].SessionID))
	}
}

// TestSearchSessions_EmptyQueryReturnsNothing — a blank query
// shouldn't crash or run an unbounded scan. Returns empty slice.
func TestSearchSessions_EmptyQueryReturnsNothing(t *testing.T) {
	store := newTestArchiveStore(t)
	if !store.FTSEnabled() {
		t.Skip("FTS5 not available")
	}

	got, err := store.SearchSessions("", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("empty query returned %d results, want 0", len(got))
	}
}

// TestWorkingMemorySearch_FindsByContent exercises the
// working_memory_fts virtual table: a row written via the normal
// Set path becomes searchable through the FTS index, and a
// phrase-shaped query returns it in BM25 order.
func TestWorkingMemorySearch_FindsByContent(t *testing.T) {
	store, archive := newTestWorkingMemoryStoreWithFTS(t)
	if !archive.FTSEnabled() || !store.FTSEnabled() {
		t.Skip("FTS5 not available")
	}

	// Two conversations with distinct working-memory state.
	if err := store.Set("conv-a",
		"Recent thread: the pool heater scheduling came up; user is "+
			"reprogramming it for shoulder season. Tone: low-stakes, no urgency."); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("conv-b",
		"Active arc: helping debug an HA automation that triggers on bedroom motion. "+
			"User is mildly frustrated; respond directly."); err != nil {
		t.Fatal(err)
	}

	got, err := store.Search("pool heater", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least one working-memory hit for 'pool heater'")
	}
	if got[0].ConversationID != "conv-a" {
		t.Errorf("top hit = %q, expected conv-a (the only row mentioning 'pool heater')",
			got[0].ConversationID)
	}
	if !strings.Contains(strings.ToLower(got[0].Content), "pool heater") {
		t.Errorf("content didn't contain the matched phrase: %q", got[0].Content)
	}
}

// TestWorkingMemorySearch_UpdateReindexes verifies the UPDATE
// trigger replaces stale text in the FTS index — set a row,
// search and find it, then update the row, search and find the
// new content (not the old).
func TestWorkingMemorySearch_UpdateReindexes(t *testing.T) {
	store, archive := newTestWorkingMemoryStoreWithFTS(t)
	if !archive.FTSEnabled() || !store.FTSEnabled() {
		t.Skip("FTS5 not available")
	}

	if err := store.Set("conv-update", "talking about the dryer making a clicking noise on the spin cycle"); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Search("clicking noise", 5)
	if len(got) == 0 {
		t.Fatal("initial Set didn't reach the FTS index")
	}

	// Replace content entirely.
	if err := store.Set("conv-update", "now discussing the upstairs HVAC thermostat readings"); err != nil {
		t.Fatal(err)
	}

	// Old content is gone.
	if got, _ := store.Search("clicking noise", 5); len(got) != 0 {
		t.Errorf("after update, stale 'clicking noise' content still in FTS index: %+v", got)
	}
	// New content is searchable.
	got2, err := store.Search("HVAC thermostat", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) == 0 || got2[0].ConversationID != "conv-update" {
		t.Errorf("after update, new content not in FTS index: %+v", got2)
	}
}

// newTestWorkingMemoryStoreWithFTS spins up a backing archive (which
// owns the FTS5-availability gate) and a working memory store on the
// same connection. Returns both because tests need to check
// FTSEnabled() on each layer.
func newTestWorkingMemoryStoreWithFTS(t *testing.T) (*WorkingMemoryStore, *ArchiveStore) {
	t.Helper()
	dbPath := t.TempDir() + "/working-fts.db"
	archive, err := NewArchiveStore(dbPath, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { archive.Close() })

	wm, err := NewWorkingMemoryStore(archive.DB(), archive.FTSEnabled())
	if err != nil {
		t.Fatal(err)
	}
	return wm, archive
}

// splitTags parses a comma-separated list into a clean []string,
// matching how the rest of the package stores tags.
func splitTags(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
