package documents

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// Frontmatter keys the loop-creation tooling stamps on curator task
// documents (internal/tools/loop_create_tool.go). The advertiser reads
// them back as provenance: which loop definition owns the document and
// what that loop is for, in the owner's own prose.
const (
	loopDefinitionNameFrontmatterKey = "loop_definition_name"
	loopIntentFrontmatterKey         = "loop_intent"
)

// AdvertisableDocument is one indexed document projected for the
// context-advertising rail (#1431): everything an advertiser needs to
// build an offer — identity, authored summary, available projection
// levels with honest byte costs, and provenance — with zero file I/O.
type AdvertisableDocument struct {
	// Root and Path identify the document within its root; Ref is the
	// preassembled root:path form every document tool accepts.
	Root string `json:"root"`
	Path string `json:"path"`
	Ref  string `json:"ref"`
	// Title and Summary are the authored identity: for a faceted
	// document the summary is its teaser (or status_line fallback),
	// which is exactly the advertisement-shaped prose.
	Title   string `json:"title"`
	Summary string `json:"summary,omitempty"`
	// Facets lists the condensed projections present in the body;
	// FacetBytes carries the UTF-8 byte length of each present level,
	// "full" included. Bytes, not runes: these are context-budget
	// estimates, and they are exact because they were measured at index
	// time — an advertiser can offer only levels that exist and cost
	// them without reading the file.
	Facets     []string       `json:"facets,omitempty"`
	FacetBytes map[string]int `json:"facet_bytes,omitempty"`
	Tags       []string       `json:"tags,omitempty"`
	ModifiedAt time.Time      `json:"modified_at"`
	// AbsPath and SizeBytes let a materializer read the file directly,
	// off the store's locks — the same reason enumeration skips Refresh
	// — and prove before rendering that the bytes on disk are still the
	// bytes this row described.
	AbsPath   string `json:"-"`
	SizeBytes int64  `json:"-"`
	// Provenance from the document's frontmatter: the loop definition
	// that owns it, the generated tool that manages it, and the owning
	// loop's intent in prose ("" when unstamped). LoopIntent is match
	// evidence; the other two are the freshness-bound lookup keys into
	// the definition registry.
	LoopDefinitionName string `json:"loop_definition_name,omitempty"`
	ManagedBy          string `json:"managed_by,omitempty"`
	LoopIntent         string `json:"loop_intent,omitempty"`
}

// AdvertisableDocuments enumerates every indexed document eligible to
// advertise context, in deterministic (root, rel_path) order, from a
// single SELECT over the index.
//
// Unlike every other public query on [Store], this deliberately does
// NOT call Refresh and does not touch refreshMu. Refresh takes
// refreshMu before its throttle check, and that lock is periodically
// held across a full git re-verification pass of every root — an
// assembly-time caller on the serial context walk must never wait on
// that. The background [Store.RunRefresher] keeps rows warm within its
// refresh interval (~5s), which is fresh enough for advertising: a
// stale offer is corrected one tick later, while a blocked context
// assembly is a whole slow turn. The price is that a caller on a store
// without a running refresher sees the index as of its last refresh.
//
// Documents whose audience column reads internal are excluded in the
// SQL itself — the #1250 privacy gate: curator working notes must never
// be offered to ambient context, and must not even be fetched and
// decoded on a per-turn path.
func (s *Store) AdvertisableDocuments(ctx context.Context) ([]AdvertisableDocument, error) {
	if s == nil {
		return nil, fmt.Errorf("document index not configured")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT root, rel_path, abs_path, title, summary, facets_json, COALESCE(facet_bytes_json, '{}'), tags_json, frontmatter_json, modified_at, size_bytes
		 FROM indexed_documents
		 WHERE LOWER(TRIM(COALESCE(audience, ''))) <> ?
		 ORDER BY root, rel_path`,
		audienceInternalValue,
	)
	if err != nil {
		return nil, fmt.Errorf("enumerate advertisable documents: %w", err)
	}
	defer rows.Close()

	var docs []AdvertisableDocument
	for rows.Next() {
		var doc AdvertisableDocument
		var facetsJSON, facetBytesJSON, tagsJSON, metaJSON, modified string
		if err := rows.Scan(&doc.Root, &doc.Path, &doc.AbsPath, &doc.Title, &doc.Summary, &facetsJSON, &facetBytesJSON, &tagsJSON, &metaJSON, &modified, &doc.SizeBytes); err != nil {
			return nil, fmt.Errorf("scan advertisable document: %w", err)
		}
		doc.Ref = makeRef(doc.Root, doc.Path)
		if err := json.Unmarshal([]byte(facetsJSON), &doc.Facets); err != nil || len(doc.Facets) == 0 {
			doc.Facets = nil
		}
		if err := json.Unmarshal([]byte(facetBytesJSON), &doc.FacetBytes); err != nil || len(doc.FacetBytes) == 0 {
			doc.FacetBytes = nil
		}
		if err := json.Unmarshal([]byte(tagsJSON), &doc.Tags); err != nil {
			doc.Tags = nil
		}
		var frontmatter map[string][]string
		if err := json.Unmarshal([]byte(metaJSON), &frontmatter); err != nil {
			frontmatter = nil
		}
		// Defense in depth behind the SQL gate: the audience column
		// holds the single promoted value, while frontmatter may carry
		// several. A document any of whose audience values reads
		// internal stays out, even if the promoted one does not.
		if isInternalAudienceDocument(frontmatter) {
			continue
		}
		doc.ModifiedAt, err = database.ParseTimestamp(modified)
		if err != nil {
			return nil, fmt.Errorf("parse modified_at for %s: %w", doc.Ref, err)
		}
		doc.LoopDefinitionName = firstValue(frontmatter, loopDefinitionNameFrontmatterKey)
		doc.ManagedBy = firstValue(frontmatter, looppkg.OutputManagedByFrontmatterKey)
		doc.LoopIntent = firstValue(frontmatter, loopIntentFrontmatterKey)
		docs = append(docs, doc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("enumerate advertisable documents: %w", err)
	}
	return docs, nil
}
