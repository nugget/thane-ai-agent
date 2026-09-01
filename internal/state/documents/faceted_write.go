package documents

import (
	"context"
	"errors"
	"fmt"
	"strings"

	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

const (
	// DocumentWriteToolName is the normal structured mutation surface for a
	// faceted document without a narrower domain owner.
	DocumentWriteToolName = "doc_write"
	// DocumentBodyWriteToolName is the exceptional whole-body mutation surface
	// for documents that intentionally have no projection contract.
	DocumentBodyWriteToolName = "doc_body_write"
)

// PublishArgs carries one atomic logical document state for the generic
// [DocumentWriteToolName] surface. Pointer compact projections distinguish an
// omitted projection from an explicitly supplied empty value.
type PublishArgs struct {
	Ref              string              `json:"ref"`
	Title            string              `json:"title,omitempty"`
	Description      string              `json:"description,omitempty"`
	Tags             []string            `json:"tags,omitempty"`
	Frontmatter      map[string][]string `json:"frontmatter,omitempty"`
	StatusLine       string              `json:"status_line"`
	Teaser           *string             `json:"teaser,omitempty"`
	Digest           *string             `json:"digest,omitempty"`
	Full             string              `json:"full"`
	ExpectedRevision string              `json:"-"`
	ReceiptScope     string              `json:"-"`
}

// FacetedWriteArgs is the shared document-layer write primitive used by the
// generic writer and narrower owners such as contact dossiers and loop
// outputs. Domain validation is the only policy supplied by an adapter; the
// contract, codec, manifest, ownership, receipts, CAS, and result shape remain
// shared.
type FacetedWriteArgs struct {
	Ref              string                             `json:"ref"`
	Title            string                             `json:"title,omitempty"`
	Description      string                             `json:"description,omitempty"`
	Tags             []string                           `json:"tags,omitempty"`
	Frontmatter      map[string][]string                `json:"frontmatter,omitempty"`
	Contract         documentfacets.Contract            `json:"contract"`
	Payload          documentfacets.Payload             `json:"payload"`
	WriteTool        string                             `json:"write_tool"`
	Validate         func(documentfacets.Payload) error `json:"-"`
	ExpectedRevision string                             `json:"-"`
	ReceiptScope     string                             `json:"-"`
}

// StructuredDocumentMutationError redirects a mutation to the tool that owns
// the target document's complete logical state.
type StructuredDocumentMutationError struct {
	Ref       string `json:"-"`
	Attempted string `json:"-"`
	WriteTool string `json:"-"`
}

// Error implements error with a one-retry recovery path for the model.
func (e *StructuredDocumentMutationError) Error() string {
	tool := strings.TrimSpace(e.WriteTool)
	if tool == "" {
		tool = DocumentWriteToolName
	}
	attempted := strings.TrimSpace(e.Attempted)
	if attempted == "" {
		attempted = "document mutation"
	}
	if tool == DocumentWriteToolName {
		return fmt.Sprintf("%s cannot change %s because it is a faceted document; no change was made. Read it, then call %s with every published projection so all views move together", attempted, e.Ref, tool)
	}
	return fmt.Sprintf("%s cannot change %s because %s owns that document; no change was made. Use %s instead", attempted, e.Ref, tool, tool)
}

// Publish creates, adopts, or republishes a generic faceted document. Legacy
// section envelopes and ordinary body-only documents acquire a durable
// manifest on this first structured write.
func (t *Tools) Publish(ctx context.Context, args PublishArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	args.Ref = strings.TrimSpace(args.Ref)
	if args.Ref == "" {
		return "", fmt.Errorf("ref is required")
	}

	var current *DocumentRecord
	record, err := t.store.Read(ctx, args.Ref)
	switch {
	case err == nil:
		current = record
	case IsNotFound(err):
	default:
		return "", fmt.Errorf("inspect %s before write: %w", args.Ref, err)
	}
	if current != nil {
		if owner := strings.TrimSpace(current.ManagedBy); owner != "" && owner != DocumentWriteToolName {
			return "", &StructuredDocumentMutationError{Ref: args.Ref, Attempted: DocumentWriteToolName, WriteTool: owner}
		}
	}

	contract := genericWriteContract(current, args.Teaser != nil, args.Digest != nil)
	return t.WriteFaceted(ctx, FacetedWriteArgs{
		Ref:              args.Ref,
		Title:            args.Title,
		Description:      args.Description,
		Tags:             append([]string(nil), args.Tags...),
		Frontmatter:      cloneFrontmatter(args.Frontmatter),
		Contract:         contract,
		Payload:          documentfacets.Payload{StatusLine: args.StatusLine, Teaser: optionalProjection(args.Teaser), Digest: optionalProjection(args.Digest), Full: args.Full},
		WriteTool:        DocumentWriteToolName,
		ExpectedRevision: args.ExpectedRevision,
		ReceiptScope:     args.ReceiptScope,
	})
}

// WriteFaceted validates and atomically persists one complete logical
// document state through the shared receipt/CAS path.
func (t *Tools) WriteFaceted(ctx context.Context, args FacetedWriteArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	args.Ref = strings.TrimSpace(args.Ref)
	args.WriteTool = strings.TrimSpace(args.WriteTool)
	if args.Ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	if args.WriteTool == "" {
		return "", fmt.Errorf("write tool is required")
	}
	current, err := t.store.Read(ctx, args.Ref)
	if err != nil && !IsNotFound(err) {
		return "", fmt.Errorf("inspect %s before %s: %w", args.Ref, args.WriteTool, err)
	}
	if current != nil {
		if owner := strings.TrimSpace(current.ManagedBy); owner != "" && owner != args.WriteTool {
			return "", &StructuredDocumentMutationError{Ref: args.Ref, Attempted: args.WriteTool, WriteTool: owner}
		}
	}

	var validationErrors []error
	if err := args.Contract.Validate(args.Payload); err != nil {
		validationErrors = append(validationErrors, err)
	}
	if args.Validate != nil {
		if err := args.Validate(args.Payload); err != nil {
			validationErrors = append(validationErrors, err)
		}
	}
	if len(validationErrors) > 0 {
		return "", fmt.Errorf("faceted document projections are invalid; correct every listed field and retry once: %w", errors.Join(validationErrors...))
	}

	body := args.Contract.Render(args.Payload)
	frontmatter := cloneFrontmatter(args.Frontmatter)
	for key, values := range (documentfacets.Manifest{Schema: documentfacets.SchemaV1, Contract: args.Contract, ManagedBy: args.WriteTool}).Frontmatter() {
		frontmatter[key] = values
	}
	writeArgs := WriteArgs{
		Ref:              args.Ref,
		Title:            args.Title,
		Description:      args.Description,
		Tags:             append([]string(nil), args.Tags...),
		Frontmatter:      frontmatter,
		Body:             &body,
		ExpectedRevision: args.ExpectedRevision,
		ReceiptScope:     args.ReceiptScope,
		StructuredTool:   args.WriteTool,
	}
	t.prepareWriteReceipt(&writeArgs)
	result, err := t.store.Write(ctx, writeArgs)
	if result != nil {
		result.Action = args.WriteTool
	}
	return t.marshalMutationResult(ctx, args.WriteTool, args.Ref, args.ReceiptScope, writeArgs.ExpectedRevision, result, err)
}

func genericWriteContract(current *DocumentRecord, teaser, digest bool) documentfacets.Contract {
	if current != nil && len(current.FacetContract.Facets) > 0 {
		return current.FacetContract
	}
	contract := documentfacets.Contract{Facets: []documentfacets.Spec{{Name: documentfacets.StatusLine}}}
	if teaser {
		contract.Facets = append(contract.Facets, documentfacets.Spec{Name: documentfacets.Teaser})
	}
	if digest {
		contract.Facets = append(contract.Facets, documentfacets.Spec{Name: documentfacets.Digest})
	}
	return contract
}

func optionalProjection(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func validateFacetedDocumentBody(body string, frontmatter map[string][]string) error {
	manifest, canonical, err := documentfacets.FromFrontmatter(frontmatter)
	if err != nil {
		return err
	}
	if !canonical {
		if _, legacy := documentfacets.ParseLegacy(body); legacy {
			return fmt.Errorf("faceted documents require a durable %s manifest; write projections through their structured tool so the legacy envelope is migrated", documentfacets.SchemaKey)
		}
		return nil
	}
	payload := manifest.Contract.Parse(body)
	var validationErrors []error
	if err := manifest.Contract.Validate(payload); err != nil {
		validationErrors = append(validationErrors, err)
	}
	if got, want := strings.TrimSpace(body), manifest.Contract.Render(payload); got != want {
		validationErrors = append(validationErrors, fmt.Errorf("body does not match the canonical faceted document codec; write logical projections through %s", manifest.ManagedBy))
	}
	if len(validationErrors) > 0 {
		return errors.Join(validationErrors...)
	}
	return nil
}

func structuredWriteTool(record *DocumentRecord) string {
	if record == nil {
		return ""
	}
	if managedBy := strings.TrimSpace(record.ManagedBy); managedBy != "" {
		return managedBy
	}
	if len(record.Facets) > 0 {
		return DocumentWriteToolName
	}
	return ""
}
