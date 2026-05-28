package main

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"

	"github.com/nugget/thane-ai-agent/internal/state/knowledge"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// stores holds read-only handles to the three sqlite databases the
// prewarm providers consume. The DSN options keep SQLite from
// touching journal/WAL files (so a live agent writing to the same
// files is undisturbed) and disable locking entirely so the harness
// can run against an SMB-mounted Thane data directory.
type stores struct {
	thane     *sql.DB
	knowledge *sql.DB

	archive    *memory.ArchiveStore
	knowledge_ *knowledge.Store
}

// openStores opens the prod databases under dataDir in read-only
// mode. Returns a stores struct with both raw *sql.DB handles (so
// the harness can run ad-hoc queries against conversations metadata)
// and the high-level wrapper stores that the providers expect.
//
// The mattn/go-sqlite3 driver respects URI query parameters when the
// DSN begins with "file:". `mode=ro` opens read-only,
// `immutable=1` short-circuits journal recovery and lets multiple
// readers race, `nolock=1` skips POSIX advisory locks (required for
// SMB-mounted databases).
func openStores(dataDir string) (*stores, error) {
	thanePath := filepath.Join(dataDir, "thane.db")
	knowledgePath := filepath.Join(dataDir, "knowledge.db")

	for _, p := range []string{thanePath, knowledgePath} {
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("required database not found: %s", p)
		}
	}

	thaneDB, err := openReadOnly(thanePath)
	if err != nil {
		return nil, fmt.Errorf("open thane.db: %w", err)
	}
	knowledgeDB, err := openReadOnly(knowledgePath)
	if err != nil {
		thaneDB.Close()
		return nil, fmt.Errorf("open knowledge.db: %w", err)
	}

	// ArchiveStore in unified mode shares the same connection for
	// both archive and messages.
	archiveCfg := memory.DefaultArchiveConfig()
	archive, err := memory.NewArchiveStoreFromDB(thaneDB, &archiveCfg, silentLogger())
	if err != nil {
		thaneDB.Close()
		knowledgeDB.Close()
		return nil, fmt.Errorf("init archive store: %w", err)
	}
	kn, err := knowledge.NewStore(knowledgeDB, silentLogger())
	if err != nil {
		thaneDB.Close()
		knowledgeDB.Close()
		return nil, fmt.Errorf("init knowledge store: %w", err)
	}

	return &stores{
		thane:      thaneDB,
		knowledge:  knowledgeDB,
		archive:    archive,
		knowledge_: kn,
	}, nil
}

// searcher wraps the archive store in the unified MemorySearch that
// NewArchiveContextProvider consumes post-#983 (Search now returns a
// multi-surface *SearchBundle, not []SearchResult). The working-memory
// store is nil: the harness opens its databases read-only/immutable and
// cannot run the working_memory CREATE-TABLE migration. MemorySearch
// nil-guards it, so the harness still exercises the archive surfaces
// (raw messages via messages_fts + session summaries via sessions_fts)
// — the search-efficacy signal this tool exists to validate.
func (s *stores) searcher() memory.MemorySearcher {
	return memory.NewMemorySearch(s.archive, nil, silentLogger())
}

func openReadOnly(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?mode=ro&immutable=1&nolock=1"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (s *stores) close() {
	if s == nil {
		return
	}
	if s.thane != nil {
		s.thane.Close()
	}
	if s.knowledge != nil {
		s.knowledge.Close()
	}
}

// silentLogger returns a logger that writes to stderr at WARN+ only.
// The harness's normal output goes to stdout; we suppress the
// providers' startup INFO chatter so the user's terminal stays clean.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}
