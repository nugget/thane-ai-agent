package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

type archiveKnowledgeProvider struct {
	archive *memory.ArchiveStore
	working *memory.WorkingMemoryStore
}

func (archiveKnowledgeProvider) name() string       { return "archives" }
func (archiveKnowledgeProvider) permission() string { return "archive_search" }

func (p archiveKnowledgeProvider) search(ctx context.Context, request knowledgeSearchRequest) (knowledgeSourceResult, error) {
	if p.archive == nil {
		return knowledgeSourceResult{}, fmt.Errorf("archive store is not configured")
	}
	bundle, err := memory.NewMemorySearch(p.archive, p.working, nil).SearchContext(ctx, memory.SearchOptions{
		Query: request.Query, Limit: request.Limit, NoContext: true,
	})
	if err != nil {
		return knowledgeSourceResult{}, err
	}
	result := knowledgeSourceResult{Truncated: bundle.Truncated || bundle.TotalMessages > len(bundle.Messages), UnavailableSurfaces: bundle.UnavailableSurfaces}
	now := time.Now()
	var messages, sessions, working []knowledgeHit
	for _, hit := range bundle.Messages {
		excerpt := hit.Highlight
		if excerpt == "" {
			excerpt = hit.Match.Content
		}
		ref := "archive:session:" + hit.SessionID
		var read *knowledgeRead
		if hit.SessionID != "" {
			read = &knowledgeRead{Tool: "archive_session_transcript", Arguments: map[string]string{"session_id": hit.SessionID}}
		} else {
			ref = "archive:message:" + hit.Match.ID
		}
		messages = append(messages, knowledgeHit{
			Source: p.name(), Kind: "message", Evidence: "primary", Ref: ref, MessageID: hit.Match.ID,
			Role: hit.Match.Role, Origin: hit.Match.Origin,
			ConversationID: hit.Match.ConversationID, Title: hit.Match.Role + " message",
			Excerpt: truncateUTF8(excerpt, 1000), MatchType: hit.MatchType,
			Age: promptfmt.FormatDeltaOnly(hit.Match.Timestamp, now), TimeBasis: "message_sent", Read: read,
		})
	}
	for _, hit := range bundle.Sessions {
		sessions = append(sessions, knowledgeHit{
			Source: p.name(), Kind: "session_summary", Evidence: "synthesis", Ref: "archive:session:" + hit.SessionID,
			ConversationID: hit.ConversationID, Title: truncateUTF8(hit.Title, 240), Excerpt: truncateUTF8(hit.Highlight, 1000),
			AuthoredSummary: truncateUTF8(hit.Summary, 800), Age: promptfmt.FormatDeltaOnly(hit.StartedAt, now), TimeBasis: "session_started",
			SummaryTruncated: len(hit.Summary) > 800,
			Read:             &knowledgeRead{Tool: "archive_session_transcript", Arguments: map[string]string{"session_id": hit.SessionID}},
		})
	}
	for _, hit := range bundle.WorkingMemory {
		working = append(working, knowledgeHit{
			Source: p.name(), Kind: "working_memory", Evidence: "synthesis", Ref: "memory:conversation:" + hit.ConversationID,
			ConversationID: hit.ConversationID, Excerpt: truncateUTF8(hit.Highlight, 1000),
			Age: promptfmt.FormatDeltaOnly(hit.UpdatedAt, now), TimeBasis: "summary_updated",
		})
	}
	// Each memory surface owns its rank. Interleave without multiplying the
	// global result allowance or treating a summary as independent evidence.
	for index := 0; ; index++ {
		added := false
		for _, hits := range [][]knowledgeHit{messages, sessions, working} {
			if index < len(hits) {
				result.Hits = append(result.Hits, hits[index])
				added = true
			}
		}
		if !added {
			break
		}
	}
	if len(result.Hits) > request.Limit {
		result.Hits = result.Hits[:request.Limit]
		result.Truncated = true
	}
	return result, nil
}

type documentKnowledgeProvider struct {
	tools *documents.Tools
}

func (documentKnowledgeProvider) name() string       { return "documents" }
func (documentKnowledgeProvider) permission() string { return "doc_search" }

func (p documentKnowledgeProvider) search(ctx context.Context, request knowledgeSearchRequest) (knowledgeSourceResult, error) {
	if p.tools == nil {
		return knowledgeSourceResult{}, fmt.Errorf("document index is not configured")
	}
	found, err := p.tools.SearchContent(ctx, documents.SearchQuery{Query: request.Query, Root: request.Root, Limit: request.Limit})
	if err != nil {
		return knowledgeSourceResult{}, err
	}
	result := knowledgeSourceResult{Truncated: found.Truncated}
	for _, root := range found.Roots {
		result.Roots = append(result.Roots, knowledgeRoot{Root: root.Root, Body: root.Body, Status: root.Status})
	}
	now := time.Now()
	for _, match := range found.Hits {
		doc := match.Document
		modified, err := database.ParseTimestamp(doc.ModifiedAt)
		if err != nil {
			return knowledgeSourceResult{}, fmt.Errorf("document %q modified time: %w", doc.Ref, err)
		}
		// A document's location or facet layout does not establish primary
		// provenance. Preserve the uncertainty until its citations are read.
		hit := knowledgeHit{
			Source: p.name(), Kind: "document", Evidence: "unspecified", Ref: doc.Ref,
			Title: truncateUTF8(doc.Title, 240), Excerpt: truncateUTF8(match.Excerpt, 1000),
			MatchType: match.MatchType,
			Age:       promptfmt.FormatDeltaOnly(modified, now), TimeBasis: "document_modified",
			Facets: doc.Facets, WriteTool: match.WriteTool,
			Read: &knowledgeRead{Tool: "doc_read", Arguments: map[string]string{"ref": doc.Ref}},
		}
		if len(doc.Facets) > 0 {
			hit.AuthoredSummary = truncateUTF8(doc.Summary, 800)
			hit.SummaryTruncated = len(doc.Summary) > 800
		}
		result.Hits = append(result.Hits, hit)
	}
	return result, nil
}
