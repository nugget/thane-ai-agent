package documents

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
)

func (s *Store) relatedIntakeDocuments(ctx context.Context, root string, args IntakeArgs, title string, tags []string) ([]IntakeRelatedDocument, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT root, rel_path, title, summary, tags_json, frontmatter_json, modified_at, word_count
		 FROM indexed_documents
		 WHERE root = ?
		 ORDER BY modified_at DESC, rel_path
		 LIMIT ?`,
		root, intakeCandidateScanCap)
	if err != nil {
		return nil, fmt.Errorf("query intake candidates: %w", err)
	}
	defer rows.Close()

	queryTokens := tokenSet(strings.Join([]string{
		title,
		args.Intent,
		args.Summary,
		args.BodySnippet,
		args.ContentDigest,
		strings.Join(tags, " "),
	}, " "))
	var related []IntakeRelatedDocument
	for rows.Next() {
		var doc DocumentSummary
		if err := scanDocument(rows, &doc); err != nil {
			return nil, fmt.Errorf("scan intake candidate: %w", err)
		}
		score := intakeSimilarity(queryTokens, doc)
		if score < 0.08 {
			continue
		}
		related = append(related, IntakeRelatedDocument{
			Ref:       doc.Ref,
			Title:     doc.Title,
			Path:      doc.Path,
			Tags:      append([]string(nil), doc.Tags...),
			Score:     score,
			Rationale: intakeSimilarityRationale(score),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(related, func(i, j int) bool {
		if related[i].Score == related[j].Score {
			return related[i].Ref < related[j].Ref
		}
		return related[i].Score > related[j].Score
	})
	if len(related) > intakeSimilarLimit {
		related = related[:intakeSimilarLimit]
	}
	return related, nil
}

func intakeSimilarity(queryTokens map[string]bool, doc DocumentSummary) float64 {
	if len(queryTokens) == 0 {
		return 0
	}
	docTokens := tokenSet(strings.Join([]string{
		doc.Title,
		doc.Summary,
		doc.Path,
		strings.Join(doc.Tags, " "),
	}, " "))
	if len(docTokens) == 0 {
		return 0
	}
	overlap := 0
	for token := range queryTokens {
		if docTokens[token] {
			overlap++
		}
	}
	score := float64(overlap) / math.Sqrt(float64(len(queryTokens)*len(docTokens)))
	if score > 1 {
		score = 1
	}
	return math.Round(score*100) / 100
}

func tokenSet(raw string) map[string]bool {
	tokens := make(map[string]bool)
	var b strings.Builder
	flush := func() {
		token := b.String()
		b.Reset()
		if len(token) >= 3 {
			tokens[token] = true
		}
	}
	for _, r := range strings.ToLower(raw) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return tokens
}

func intakeSimilarityRationale(score float64) string {
	switch {
	case score >= intakeHighOverlapScore:
		return "high token overlap with title/path/summary/tags"
	case score >= intakeMaybeOverlapScore:
		return "moderate token overlap; inspect before creating a new document"
	default:
		return "low but nonzero token overlap"
	}
}
