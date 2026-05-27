package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// cmdSurfaces queries the same phrase across every distilled
// memory surface in thane.db, side-by-side with the raw
// messages_fts result the current archive_search uses. This makes
// visible how much retrievable signal lives in surfaces that
// archive_search doesn't currently index: session summaries /
// titles / tags, working_memory entries, and indexed_documents
// summaries.
//
// LIKE-based scans are used for the un-indexed surfaces because
// they have small corpora (479 + 86 + 525 rows). FTS would be
// nicer but the rows are few enough that exhaustive scan answers
// in microseconds — and the harness can't create persistent FTS
// tables against a read-only mount.
func cmdSurfaces(g *globals, args []string) error {
	fs := flag.NewFlagSet("surfaces", flag.ContinueOnError)
	message := fs.String("message", "", "search phrase (required)")
	limit := fs.Int("limit", 5, "top-K hits per surface")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *message == "" {
		return fmt.Errorf("surfaces: -message is required")
	}

	s, err := openStores(g.DataDir)
	if err != nil {
		return err
	}
	defer s.close()

	report := surfacesReport{Query: *message}

	// 1. messages_fts (what archive_search uses today). Use the
	// phrase-first + filter variant since the compare experiment
	// showed it's the strongest baseline.
	msgHits, err := runVariant(s.thane, *message,
		variantSpec{Sanitizer: sanitizerPhraseFirst, Filter: filterDropAnticipation},
		*limit, "")
	if err != nil {
		return fmt.Errorf("messages_fts: %w", err)
	}
	report.Surfaces = append(report.Surfaces, surfaceHits{
		Name:        "messages_fts (current archive_search)",
		Description: "Raw archived turns; the only surface today's archive_search indexes.",
		Hits:        msgHits,
	})

	// 2. sessions.summary / title / tags
	sessHits, err := scanSessions(s.thane, *message, *limit)
	if err != nil {
		return fmt.Errorf("sessions: %w", err)
	}
	report.Surfaces = append(report.Surfaces, surfaceHits{
		Name:        "sessions.summary / title / tags",
		Description: "Post-hoc narrative summaries written by the session-metadata summarizer. Wrote 479 in Feb, then 0/month after Mar 1.",
		Hits:        sessHits,
	})

	// 3. working_memory.content
	wmHits, err := scanWorkingMemory(s.thane, *message, *limit)
	if err != nil {
		return fmt.Errorf("working_memory: %w", err)
	}
	report.Surfaces = append(report.Surfaces, surfaceHits{
		Name:        "working_memory.content",
		Description: "In-session living distillations the agent wrote to itself. 86 entries.",
		Hits:        wmHits,
	})

	// 4. indexed_documents.summary / title
	docHits, err := scanIndexedDocs(s.thane, *message, *limit)
	if err != nil {
		return fmt.Errorf("indexed_documents: %w", err)
	}
	report.Surfaces = append(report.Surfaces, surfaceHits{
		Name:        "indexed_documents.summary / title",
		Description: "KB-doc summaries (525 rows). doc_search hits the body; summary/title surface is not searched today.",
		Hits:        docHits,
	})

	return renderSurfaces(g.Format, report)
}

type surfacesReport struct {
	Query    string        `json:"query"`
	Surfaces []surfaceHits `json:"surfaces"`
}

type surfaceHits struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Hits        []rawHit `json:"hits"`
}

// scanSessions LIKE-searches the sessions table across summary,
// title, and tags JSON. Tiny corpus (479 rows with summaries) so
// no FTS needed for the experiment.
func scanSessions(db *sql.DB, query string, limit int) ([]rawHit, error) {
	pat := "%" + query + "%"
	rows, err := db.Query(`
		SELECT id, conversation_id, started_at,
		       coalesce(title, ''), coalesce(summary, ''), coalesce(tags, '')
		FROM sessions
		WHERE summary LIKE ? OR title LIKE ? OR tags LIKE ?
		ORDER BY started_at DESC
		LIMIT ?
	`, pat, pat, pat, limit*2)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now()
	out := []rawHit{}
	rank := 0
	for rows.Next() {
		var id, convID, startedStr, title, summary, tags string
		if err := rows.Scan(&id, &convID, &startedStr, &title, &summary, &tags); err != nil {
			return nil, err
		}
		rank++
		ts, _ := time.Parse(time.RFC3339Nano, startedStr)
		if ts.IsZero() {
			ts, _ = time.Parse("2006-01-02 15:04:05.999999999-07:00", startedStr)
		}
		var age string
		if !ts.IsZero() {
			age = promptfmt.FormatDeltaOnly(ts, now)
		}
		// Synthesize a preview: prefer summary > title > tags.
		preview := summary
		if preview == "" {
			preview = title
		}
		if preview == "" {
			preview = tags
		}
		out = append(out, rawHit{
			Rank:           rank,
			Role:           "session",
			ConversationID: convID,
			SessionID:      id,
			AgeDelta:       age,
			Content:        truncateContent(preview, 160),
			FullContent:    summary + " | title=" + title + " | tags=" + tags,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

// scanWorkingMemory LIKE-searches working_memory.content. Each
// row is one session's living distillation; the corpus is 86 rows
// today so a full scan is essentially free.
func scanWorkingMemory(db *sql.DB, query string, limit int) ([]rawHit, error) {
	pat := "%" + query + "%"
	rows, err := db.Query(`
		SELECT conversation_id, content, updated_at
		FROM working_memory
		WHERE content LIKE ?
		ORDER BY updated_at DESC
		LIMIT ?
	`, pat, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now()
	out := []rawHit{}
	rank := 0
	for rows.Next() {
		var convID, content, updatedStr string
		if err := rows.Scan(&convID, &content, &updatedStr); err != nil {
			return nil, err
		}
		rank++
		ts, _ := time.Parse(time.RFC3339Nano, updatedStr)
		if ts.IsZero() {
			ts, _ = time.Parse("2006-01-02 15:04:05.999999999-07:00", updatedStr)
		}
		var age string
		if !ts.IsZero() {
			age = promptfmt.FormatDeltaOnly(ts, now)
		}
		out = append(out, rawHit{
			Rank:           rank,
			Role:           "working_memory",
			ConversationID: convID,
			AgeDelta:       age,
			Content:        truncateContent(content, 160),
			FullContent:    content,
		})
	}
	return out, rows.Err()
}

// scanIndexedDocs LIKE-searches indexed_documents on summary and
// title. The body is searched separately by doc_search; this
// surface tests whether the summary-level signal would lift
// retrieval if archive_search consulted it too.
func scanIndexedDocs(db *sql.DB, query string, limit int) ([]rawHit, error) {
	pat := "%" + query + "%"
	rows, err := db.Query(`
		SELECT root, rel_path, coalesce(title,''), coalesce(summary,''), coalesce(modified_at,'')
		FROM indexed_documents
		WHERE summary LIKE ? OR title LIKE ?
		ORDER BY modified_at DESC
		LIMIT ?
	`, pat, pat, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now()
	out := []rawHit{}
	rank := 0
	for rows.Next() {
		var root, relPath, title, summary, modStr string
		if err := rows.Scan(&root, &relPath, &title, &summary, &modStr); err != nil {
			return nil, err
		}
		rank++
		ts, _ := time.Parse(time.RFC3339Nano, modStr)
		if ts.IsZero() {
			ts, _ = time.Parse("2006-01-02 15:04:05.999999999-07:00", modStr)
		}
		var age string
		if !ts.IsZero() {
			age = promptfmt.FormatDeltaOnly(ts, now)
		}
		preview := summary
		if preview == "" {
			preview = title
		}
		out = append(out, rawHit{
			Rank:           rank,
			Role:           "kb_doc",
			ConversationID: root + ":" + relPath,
			AgeDelta:       age,
			Content:        truncateContent(preview, 160),
			FullContent:    "title=" + title + " | summary=" + summary,
		})
	}
	return out, rows.Err()
}

func renderSurfaces(format string, r surfacesReport) error {
	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	fmt.Printf("═══ surfaces: %q ═══\n", r.Query)
	for _, s := range r.Surfaces {
		fmt.Println()
		fmt.Printf("[%s]  %d hits\n", s.Name, len(s.Hits))
		if s.Description != "" {
			fmt.Printf("  %s\n", s.Description)
		}
		if len(s.Hits) == 0 {
			fmt.Println("  (no matches)")
			continue
		}
		for _, h := range s.Hits {
			conv := h.ConversationID
			if len(conv) > 36 {
				conv = conv[:33] + "..."
			}
			fmt.Printf("  %d. role=%-15s t=%-10s conv=%-36s\n", h.Rank, h.Role, h.AgeDelta, conv)
			body := strings.ReplaceAll(h.Content, "\n", " ")
			fmt.Printf("     %s\n", body)
		}
	}
	fmt.Println()
	fmt.Println("Summary (hits per surface):")
	for _, s := range r.Surfaces {
		short := s.Name
		if i := strings.Index(short, " "); i > 0 && len(short) > 36 {
			short = short[:36] + "…"
		}
		fmt.Printf("  %-44s %d\n", short, len(s.Hits))
	}
	return nil
}
