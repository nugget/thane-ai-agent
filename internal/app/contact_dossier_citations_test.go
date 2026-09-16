package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// TestConfigureContactDossierDocuments_ResolvesCitedSessionPrefixes pins
// the production wiring of the citation resolver end to end: contact
// tools configured the way initChannels configures them look a refused
// leading part up in a real archive, across the whole archive, and name
// the full citation. Without an archive store no resolver is wired.
func TestConfigureContactDossierDocuments_ResolvesCitedSessionPrefixes(t *testing.T) {
	ctx := context.Background()
	contactStore := newEmailIdentityStore(t)
	household, _ := seedForkPair(t, contactStore)
	docStore, _ := newContactsDocumentStore(t)

	working, err := memory.NewSQLiteStore(t.TempDir()+"/working.db", 100)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = working.Close() })
	archive, err := memory.NewArchiveStoreFromDB(working.DB(), nil, nil)
	if err != nil {
		t.Fatalf("NewArchiveStoreFromDB: %v", err)
	}
	const citedID = "0190aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee"
	old := time.Date(2025, 1, 10, 9, 0, 0, 0, time.UTC)
	insertArchiveSession(t, working, citedID, old, "Alice plans the garden")
	// More than the 100 newest sessions, so an older prefix is found
	// only by a lookup across the whole archive.
	for i := range 101 {
		insertArchiveSession(t, working, fmt.Sprintf("01a00000-0000-7000-8000-%012d", i), time.Now().UTC().Add(-time.Duration(i)*time.Minute), "recent")
	}

	tools := contacts.NewTools(contactStore, nil)
	tools.ConfigureDossierRoot(true, true)
	configureContactDossierDocuments(tools, documents.NewTools(docStore), docStore, archive)
	_, err = tools.WriteDossier(ctx, contacts.DossierWriteArgs{
		ContactID:  household.ID.String(),
		StatusLine: "Current.",
		Teaser:     "Useful hook.",
		Digest:     "Enough context to act.",
		Full:       "Garden plans. — evidence: archive:session-0190aaaa",
	})
	// The age is measured from the clock the write runs on, so the
	// citation is asserted as the two halves either side of it; the
	// delta itself is pinned against a fixed clock in the contacts
	// package, which renders it.
	head := `cite archive:session:` + citedID + ` (age -`
	tail := `, time_basis session_started, title "Alice plans the garden")`
	if err == nil || !strings.Contains(err.Error(), head) || !strings.Contains(err.Error(), tail) {
		t.Fatalf("WriteDossier() = %v, want the refusal to name %q ... %q", err, head, tail)
	}

	if archiveSessionResolver(nil) != nil {
		t.Error("without an archive store the citation resolver must stay unwired")
	}
}

func insertArchiveSession(t *testing.T, working *memory.SQLiteStore, id string, startedAt time.Time, title string) {
	t.Helper()
	if _, err := working.DB().Exec(`INSERT INTO sessions (id, conversation_id, started_at, title, summary) VALUES (?, 'conv', ?, ?, '')`,
		id, startedAt.Format(time.RFC3339Nano), title); err != nil {
		t.Fatalf("insert session %s: %v", id, err)
	}
}
