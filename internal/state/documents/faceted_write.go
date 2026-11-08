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
	// RejectionIsError surfaces a refused publish as a
	// *MutationRejectedError rather than an inline applied:false payload;
	// see [WriteArgs.RejectionIsError] for who sets it and why.
	RejectionIsError bool `json:"-"`
	// PreviousWriteTools permits a trusted adapter to rename its own write
	// surface while atomically migrating the document contract.
	PreviousWriteTools []string `json:"-"`
	// Shortened lists projections the adapter's own [documentfacets.Shrinker.Fit]
	// shortened, reported in the result with any this write shortens. A
	// payload carrying any is validated as the store will save it.
	Shortened []documentfacets.Shortened `json:"-"`
}

// shortenedWarning frames shortened_projections in a write's warnings.
const shortenedWarning = "shortened_projections were a little over their character limit, so Go had a model shorten them before saving; each saved_text is exactly what is stored now, so treat it, not what you sent, as the current projection."

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
		if owner := strings.TrimSpace(current.ManagedBy); owner != "" && owner != args.WriteTool && !containsWriteTool(args.PreviousWriteTools, owner) {
			return "", &StructuredDocumentMutationError{Ref: args.Ref, Attempted: args.WriteTool, WriteTool: owner}
		}
	}

	sent := args.Payload
	var shortened []documentfacets.Shortened
	args.Payload, shortened, err = t.shrinker.Fit(ctx, sent, func(payload documentfacets.Payload) error {
		// Shortened text is judged as the store will save it, including
		// text the adapter's own Fit shortened, which arrives here as
		// sent; the author's own text still meets the store's folding
		// and checks in Store.Write, as before.
		if payload != sent || len(args.Shortened) > 0 {
			stored, err := StoredPayload(args.Ref, args.Contract, payload)
			if err != nil {
				return err
			}
			payload = stored
		}
		var validationErrors []error
		if err := args.Contract.Validate(payload); err != nil {
			validationErrors = append(validationErrors, err)
		}
		if args.Validate != nil {
			if err := args.Validate(payload); err != nil {
				validationErrors = append(validationErrors, err)
			}
		}
		if len(validationErrors) > 0 {
			return documentfacets.InvalidProjectionsError("faceted document projections", validationErrors...)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	body := args.Contract.Render(args.Payload)
	frontmatter := cloneFrontmatter(args.Frontmatter)
	for key, values := range (documentfacets.Manifest{Schema: documentfacets.SchemaV1, Contract: args.Contract, ManagedBy: args.WriteTool}).Frontmatter() {
		frontmatter[key] = values
	}
	writeArgs := WriteArgs{
		Ref:                args.Ref,
		Title:              args.Title,
		Description:        args.Description,
		Tags:               append([]string(nil), args.Tags...),
		Frontmatter:        frontmatter,
		Body:               &body,
		ExpectedRevision:   args.ExpectedRevision,
		ReceiptScope:       args.ReceiptScope,
		RejectionIsError:   args.RejectionIsError,
		StructuredTool:     args.WriteTool,
		PreviousWriteTools: append([]string(nil), args.PreviousWriteTools...),
	}
	t.prepareWriteReceipt(&writeArgs)
	result, err := t.store.Write(ctx, writeArgs)
	if result != nil {
		result.Action = args.WriteTool
		// The store saved the body with its glyph artifacts folded, and
		// the validation above passed only a payload whose projections
		// read back as written once folded ([StoredPayload]), so each
		// saved_text folded the same way is the stored projection.
		for _, entry := range append(args.Shortened, shortened...) {
			entry.SavedText = normalizeGlyphArtifacts(entry.SavedText)
			result.Shortened = append(result.Shortened, entry)
		}
		if len(result.Shortened) > 0 {
			result.Warnings = append(result.Warnings, shortenedWarning)
		}
	}
	payload, err := t.marshalMutationResult(ctx, args.WriteTool, args.Ref, args.ReceiptScope, writeArgs.ExpectedRevision, result, err)
	return deliverMutation(payload, err, args.RejectionIsError)
}

func containsWriteTool(tools []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, tool := range tools {
		if strings.TrimSpace(tool) == target {
			return true
		}
	}
	return false
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
		// The contract marks each violation with its projection key, but
		// here every projection arrived inside one whole body, so those
		// keys are not arguments of the call that sent it. Keep the text
		// and drop the marks: a structured writer that does take the keys
		// as arguments has already validated them with the marks intact.
		validationErrors = append(validationErrors, errors.New(err.Error()))
	}
	if got, want := strings.TrimSpace(body), manifest.Contract.Render(payload); got != want {
		validationErrors = append(validationErrors, fmt.Errorf("body does not match the canonical faceted document codec; write logical projections through %s", manifest.ManagedBy))
	}
	if len(validationErrors) > 0 {
		return errors.Join(validationErrors...)
	}
	return nil
}

// StoredPayload returns payload as [Store.Write] would save it under ref,
// or why the store would refuse it: the body payload renders to, with its
// glyph artifacts folded and its prose guarded as the store does, parsed
// back with contract. A body that does not render back to itself, the rule
// validateFacetedDocumentBody applies, is refused here too.
//
// A faceted writer runs all of its validation on this stored form of a
// shortened payload ([documentfacets.Shrinker.Fit]) rather than on the
// shrink model's reply, which is what keeps the shrinker to two outcomes.
// For any shrink reply, a write either lands, with every shortened
// projection read back from the store equal to the saved_text its result
// reports, or returns byte for byte the refusal the same write returns with
// no shrinker. Never a third: a reply the store would fold into a reserved
// heading, a rendered delta or the subject's name cannot become a refusal
// about text the author never sent.
func StoredPayload(ref string, contract documentfacets.Contract, payload documentfacets.Payload) (documentfacets.Payload, error) {
	body := normalizeGlyphArtifacts(contract.Render(payload))
	if _, err := guardAuthoredProse(ref, body); err != nil {
		return documentfacets.Payload{}, err
	}
	stored := contract.Parse(body)
	if contract.Render(stored) != strings.TrimSpace(body) {
		return documentfacets.Payload{}, fmt.Errorf("%s would not read back as the projections written once its glyph artifacts are folded", ref)
	}
	return stored, nil
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
	if record.InvalidFacetManifest != "" {
		return DocumentWriteToolName
	}
	return ""
}

func invalidFacetManifestReadError(record *DocumentRecord) error {
	if record == nil || strings.TrimSpace(record.InvalidFacetManifest) == "" {
		return nil
	}
	owner := strings.TrimSpace(record.ManagedBy)
	if owner == "" {
		owner = DocumentWriteToolName
	}
	return fmt.Errorf("document %s has an invalid faceted manifest; no private storage envelope was returned. Repair it by calling %s with the complete logical projections, or correct the reserved frontmatter through repository administration: %s", record.Ref, owner, record.InvalidFacetManifest)
}
