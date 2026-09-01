package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/state/documents"
	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

// readDocumentFacet returns one projection of a faceted document.
//
// This is the read half of the document facet contract: a consumer states the
// cost it can afford and gets a value authored for exactly that, rather than a
// whole document it has to summarize itself or a blind truncation of one.
//
// It deliberately reads the contract rather than the document's structure.
// The facet sections are rendered by Go and parsed back by Go; asking for
// "status_line" is asking the contract a question, and a caller never needs to
// know -- or be able to depend on -- that the answer is a section titled
// "Status Line".
func readDocumentFacet(ctx context.Context, dt *documents.Tools, ref, level string) (string, error) {
	if !documentfacets.IsKey(level) {
		return "", fmt.Errorf("unknown level %q; valid levels are %s", level, strings.Join(documentfacets.Keys(), ", "))
	}
	doc, err := dt.RecordWithReceipt(ctx, documents.RefArgs{Ref: ref, ReceiptScope: documentRevisionScope(ctx)})
	if err != nil {
		return "", err
	}

	faceted := len(doc.FacetContract.Facets) > 0
	payload := documentfacets.Payload{Full: doc.Body}
	if faceted {
		payload = doc.FacetContract.Parse(doc.Body)
	}
	available := make([]string, 0, len(documentfacets.Keys()))
	for _, key := range documentfacets.Keys() {
		if _, ok := payload.ByKey(key); ok {
			available = append(available, key)
		}
	}

	result := map[string]any{
		"ref":              ref,
		"level":            level,
		"faceted":          faceted,
		"levels_available": available,
		"write_tool":       documentRecordWriteTool(doc),
	}
	if title := strings.TrimSpace(doc.Title); title != "" {
		result["title"] = title
	}

	content, ok := payload.ByKey(level)
	if !ok {
		// Not an error: the document exists and the level does not. Say
		// which levels it does have, so the next call is a choice rather
		// than a retry.
		result["content"] = ""
		if faceted {
			result["note"] = fmt.Sprintf("This document publishes no %s. Read one of: %s.", level, strings.Join(available, ", "))
		} else {
			result["note"] = "This is a body-only document. Read it without level, keep that exceptional shape with doc_body_write, or update it with doc_write to migrate it into the normal projection contract."
		}
		return marshalDocumentToolResult(result)
	}
	result["content"] = content

	// full is the one facet with no budget, so it is the one that can
	// outgrow the tool-result ceiling. When it does and the document
	// publishes something cheaper, saying so beats handing back a
	// byte-truncated document: choosing a level is the remedy this tool
	// exists to offer, and the generic envelope would instead advise
	// picking a section -- the structure a level read deliberately hides.
	if len(content) > documents.MaxToolResultBytes && len(available) > 1 {
		cheaper := available[:len(available)-1]
		result["content"] = ""
		result["truncated"] = true
		result["bytes_total"] = len(content)
		result["note"] = fmt.Sprintf(
			"full is %d bytes, past the %d-byte tool-result ceiling. Read it at %s instead, or use doc_outline and doc_section to take the part you need.",
			len(content), documents.MaxToolResultBytes, strings.Join(cheaper, ", "),
		)
	}
	return marshalDocumentToolResult(result)
}

func documentRecordWriteTool(doc *documents.DocumentRecord) string {
	if doc == nil {
		return documents.DocumentBodyWriteToolName
	}
	if managedBy := strings.TrimSpace(doc.ManagedBy); managedBy != "" {
		return managedBy
	}
	if len(doc.Facets) > 0 {
		return documents.DocumentWriteToolName
	}
	return documents.DocumentBodyWriteToolName
}

// marshalDocumentToolResult renders a facet read under the same ceiling every
// other document tool result is held to.
func marshalDocumentToolResult(result map[string]any) (string, error) {
	return documents.MarshalToolResult(result)
}
