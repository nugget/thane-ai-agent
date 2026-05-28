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

// cmdCompare runs four variants of the same FTS5 query against
// thane.db's messages_fts table and emits a side-by-side view of
// the top-K results plus a "synthetic vs conversational" summary
// per variant. The four variants A/B/C/D cross two retrieval-side
// changes the diagnosis pointed at:
//
//	         |  sanitizer = OR  |  sanitizer = phrase-first
//	---------+------------------+---------------------------
//	no filter|        A          |            B
//	filter   |        C          |            D
//
// The filter drops wake-bridge synthetic content (anticipation
// events whose `content` field begins with "Anticipation matched:")
// at search time. The phrase-first sanitizer tries a true FTS5
// phrase match before falling back to OR-of-terms for queries that
// return zero phrase hits, so single-word queries and rare phrases
// keep their old recall.
//
// The harness runs this entirely against the production thane.db
// in read-only mode — no production code paths execute, so we can
// poke at retrieval changes without restarting the agent.
func cmdCompare(g *globals, args []string) error {
	fs := flag.NewFlagSet("compare", flag.ContinueOnError)
	message := fs.String("message", "", "search query (required)")
	limit := fs.Int("limit", 8, "top-K hits to show per variant")
	excludeConv := fs.String("exclude-conv", "", "conversation_id to drop post-search (e.g. the current Signal conv)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *message == "" {
		return fmt.Errorf("compare: -message is required")
	}

	s, err := openStores(g.DataDir)
	if err != nil {
		return err
	}
	defer s.close()

	variants := []variantSpec{
		{Label: "A baseline (OR sanitizer, no filter)", Sanitizer: sanitizerOR, Filter: filterNone},
		{Label: "B phrase-first (no filter)", Sanitizer: sanitizerPhraseFirst, Filter: filterNone},
		{Label: "C baseline + drop anticipation events", Sanitizer: sanitizerOR, Filter: filterDropAnticipation},
		{Label: "D phrase-first + drop anticipation events", Sanitizer: sanitizerPhraseFirst, Filter: filterDropAnticipation},
	}

	results := make([]variantResult, 0, len(variants))
	for _, v := range variants {
		hits, err := runVariant(s.thane, *message, v, *limit, *excludeConv)
		if err != nil {
			return fmt.Errorf("variant %s: %w", v.Label, err)
		}
		results = append(results, variantResult{Spec: v, Hits: hits})
	}

	return renderCompare(g.Format, *message, results, *excludeConv)
}

// variantSpec is one A/B cell.
type variantSpec struct {
	Label     string
	Sanitizer sanitizerMode
	Filter    filterMode
}

type variantResult struct {
	Spec variantSpec
	Hits []rawHit
}

type sanitizerMode int

const (
	sanitizerOR sanitizerMode = iota
	sanitizerPhraseFirst
)

type filterMode int

const (
	filterNone filterMode = iota
	filterDropAnticipation
)

// runVariant executes one variant's SQL and returns rawHit values
// ranked by FTS5 BM25. The phrase-first variant attempts an exact
// phrase match and, if the phrase came back thin (fewer than
// limit hits), backfills with OR-of-terms results so single-word
// queries and rare-phrase queries don't lose recall. Phrase hits
// always come first; OR backfill is appended in BM25 order with
// dedup by (conversation_id, content) so the precision-first
// shape stays intact.
func runVariant(db *sql.DB, query string, v variantSpec, limit int, excludeConv string) ([]rawHit, error) {
	if v.Sanitizer == sanitizerPhraseFirst {
		phraseHits, err := executeFTS(db, phraseQuery(query), v.Filter, limit, excludeConv)
		if err != nil {
			return nil, err
		}
		if len(phraseHits) >= limit {
			return phraseHits, nil
		}
		// Backfill with OR results, deduped against the phrase hits.
		orHits, err := executeFTS(db, orQuery(query), v.Filter, limit*2, excludeConv)
		if err != nil {
			return nil, err
		}
		merged := mergeHits(phraseHits, orHits, limit)
		return merged, nil
	}
	return executeFTS(db, orQuery(query), v.Filter, limit, excludeConv)
}

// mergeHits returns up to limit results: phrase hits first
// (already in BM25 order), then OR hits not already represented in
// the phrase set. The dedup key is (conversation_id, content
// prefix) so two hits with identical text from the same conv don't
// both make it through.
func mergeHits(phrase, or []rawHit, limit int) []rawHit {
	seen := make(map[string]struct{}, len(phrase)+len(or))
	key := func(h rawHit) string {
		c := h.Content
		if len(c) > 80 {
			c = c[:80]
		}
		return h.ConversationID + "|" + c
	}
	out := make([]rawHit, 0, limit)
	for _, h := range phrase {
		k := key(h)
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, h)
		if len(out) >= limit {
			return out
		}
	}
	for _, h := range or {
		k := key(h)
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		// Renumber rank to reflect merged ordering.
		h.Rank = len(out) + 1
		out = append(out, h)
		if len(out) >= limit {
			return out
		}
	}
	return out
}

// phraseQuery returns the FTS5 phrase-match form: a single
// double-quoted phrase. Empty input collapses to empty.
func phraseQuery(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return ""
	}
	// Escape any embedded double-quotes per FTS5 string-literal rules.
	q = strings.ReplaceAll(q, `"`, `""`)
	return `"` + q + `"`
}

// orQuery returns the current production sanitizer behavior:
// wrap each whitespace-split term in quotes and OR them. This is
// the baseline for the A/B.
func orQuery(q string) string {
	words := strings.Fields(q)
	if len(words) == 0 {
		return ""
	}
	quoted := make([]string, len(words))
	for i, w := range words {
		w = strings.ReplaceAll(w, `"`, `""`)
		quoted[i] = `"` + w + `"`
	}
	return strings.Join(quoted, " OR ")
}

// executeFTS runs the FTS5 query with optional content filter and
// post-filters by excludeConv. Over-fetches when excludeConv is set
// so the LIMIT still yields the intended number of non-excluded
// rows.
func executeFTS(db *sql.DB, ftsExpr string, filter filterMode, limit int, excludeConv string) ([]rawHit, error) {
	if ftsExpr == "" {
		return nil, nil
	}
	conds := []string{"messages_fts MATCH ?"}
	args := []any{ftsExpr}
	if filter == filterDropAnticipation {
		conds = append(conds, "m.content NOT LIKE 'Anticipation matched:%'")
	}
	// Over-fetch to allow exclusion filtering without losing hits.
	fetch := limit
	if excludeConv != "" {
		fetch = limit * 3
		if fetch < limit+5 {
			fetch = limit + 5
		}
	}
	q := `SELECT m.role, m.conversation_id, COALESCE(m.session_id, '') AS session_id,
	             m.content, m.timestamp
	      FROM messages_fts
	      JOIN messages m ON m.rowid = messages_fts.rowid
	      WHERE ` + strings.Join(conds, " AND ") + `
	      ORDER BY rank LIMIT ?`
	args = append(args, fetch)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("fts query: %w", err)
	}
	defer rows.Close()

	now := time.Now()
	out := make([]rawHit, 0, limit)
	rank := 0
	for rows.Next() {
		var role, convID, sessID, content, tsStr string
		if err := rows.Scan(&role, &convID, &sessID, &content, &tsStr); err != nil {
			return nil, err
		}
		if excludeConv != "" && convID == excludeConv {
			continue
		}
		rank++
		ts, _ := time.Parse(time.RFC3339Nano, tsStr)
		if ts.IsZero() {
			ts, _ = time.Parse("2006-01-02 15:04:05.999999999-07:00", tsStr)
		}
		var age string
		if !ts.IsZero() {
			age = promptfmt.FormatDeltaOnly(ts, now)
		}
		out = append(out, rawHit{
			Rank:           rank,
			Role:           role,
			ConversationID: convID,
			SessionID:      sessID,
			AgeDelta:       age,
			Content:        truncateContent(content, 110),
			FullContent:    content,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

// classifyHit tags a hit as "anticipation" (purely synthetic
// content generated by the wake bridge — no human-typed text
// inside) vs "conversational" (everything else, including envelope
// wrappers around real counterparty text). The earlier draft of
// this classifier treated "System:" / "Signal message from" /
// "Email message from" prefixes as synthetic, which was wrong —
// those are envelopes around real content and the underlying text
// is exactly what we want search to surface. Only "Anticipation
// matched:" is fully generated by the wake bridge with no human
// content inside.
func classifyHit(h rawHit) string {
	c := strings.TrimSpace(h.Content)
	if strings.HasPrefix(c, "Anticipation matched:") {
		return "anticipation"
	}
	return "conversational"
}

// containsPhrase reports whether the hit content contains the
// literal search phrase (case-insensitive, with common typographic
// variations folded). For multi-word queries this distinguishes
// phrase-first hits (which guarantee the phrase is in the content)
// from OR-of-terms hits (which only guarantee one of the words
// matched). Folding handles the "game‑room door" (en-dash) variant
// of "game room door" the model has been known to emit, plus the
// hyphenated "game-room" form, so the precision metric doesn't
// undercount stylistic variants.
func containsPhrase(h rawHit, phrase string) bool {
	if phrase == "" {
		return false
	}
	// Prefer the full content when present so the metric isn't fooled
	// by phrases that fall past the truncated-preview window.
	body := h.FullContent
	if body == "" {
		body = h.Content
	}
	return strings.Contains(normalizeForPhrase(body), normalizeForPhrase(phrase))
}

// normalizeForPhrase lowercases and folds typographic dashes /
// non-breaking spaces to ASCII space so equivalent phrasings match.
func normalizeForPhrase(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer(
		"‐", " ", // hyphen
		"‑", " ", // non-breaking hyphen
		"‒", " ", // figure dash
		"–", " ", // en dash
		"—", " ", // em dash
		" ", " ", // nbsp
		" ", " ", // narrow nbsp
		"-", " ",
	).Replace(s)
	// collapse runs of whitespace
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// renderCompare prints the four variants in a consistent format and
// closes with a summary table of synthetic vs conversational top-K
// composition. Human format optimizes for terminal readability;
// JSON format dumps the structured per-variant hits.
func renderCompare(format, query string, results []variantResult, excludeConv string) error {
	if format == "json" {
		return renderCompareJSON(query, results, excludeConv)
	}
	return renderCompareHuman(query, results, excludeConv)
}

func renderCompareHuman(query string, results []variantResult, excludeConv string) error {
	fmt.Printf("═══ compare: %q (limit per variant = %d) ═══\n", query, topK(results))
	if excludeConv != "" {
		fmt.Printf("    excluding conversation: %s\n", excludeConv)
	}
	for _, r := range results {
		fmt.Println()
		fmt.Printf("[%s]  %d hits\n", r.Spec.Label, len(r.Hits))
		for _, h := range r.Hits {
			conv := h.ConversationID
			if len(conv) > 28 {
				conv = conv[:25] + "..."
			}
			kind := classifyHit(h)
			fmt.Printf("  %d. role=%-9s kind=%-14s t=%-10s conv=%-28s\n", h.Rank, h.Role, kind, h.AgeDelta, conv)
			fmt.Printf("     %s\n", h.Content)
		}
	}
	fmt.Println()
	fmt.Println("Summary (top-K composition):")
	fmt.Println("  variant                                              | anticipation | conv | phrase-in-content")
	fmt.Println("  -----------------------------------------------------|--------------|------|------------------")
	for _, r := range results {
		anticipation := 0
		conversational := 0
		phraseHits := 0
		for _, h := range r.Hits {
			if classifyHit(h) == "conversational" {
				conversational++
			} else {
				anticipation++
			}
			if containsPhrase(h, query) {
				phraseHits++
			}
		}
		n := len(r.Hits)
		fmt.Printf("  %-52s |    %d/%d       | %d/%d  | %d/%d\n",
			r.Spec.Label, anticipation, n, conversational, n, phraseHits, n)
	}
	return nil
}

func renderCompareJSON(query string, results []variantResult, excludeConv string) error {
	type variantOut struct {
		Label          string   `json:"label"`
		Sanitizer      string   `json:"sanitizer"`
		Filter         string   `json:"filter"`
		HitCount       int      `json:"hit_count"`
		Synthetic      int      `json:"synthetic"`
		Conversational int      `json:"conversational"`
		Hits           []rawHit `json:"hits"`
	}
	out := struct {
		Query    string       `json:"query"`
		Excluded string       `json:"excluded_conversation,omitempty"`
		Variants []variantOut `json:"variants"`
	}{Query: query, Excluded: excludeConv}
	for _, r := range results {
		v := variantOut{
			Label:    r.Spec.Label,
			HitCount: len(r.Hits),
			Hits:     r.Hits,
		}
		switch r.Spec.Sanitizer {
		case sanitizerOR:
			v.Sanitizer = "or"
		case sanitizerPhraseFirst:
			v.Sanitizer = "phrase-first"
		}
		switch r.Spec.Filter {
		case filterNone:
			v.Filter = "none"
		case filterDropAnticipation:
			v.Filter = "drop-anticipation"
		}
		for _, h := range r.Hits {
			if classifyHit(h) == "conversational" {
				v.Conversational++
			} else {
				v.Synthetic++
			}
		}
		out.Variants = append(out.Variants, v)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func topK(results []variantResult) int {
	if len(results) == 0 {
		return 0
	}
	return len(results[0].Hits)
}
