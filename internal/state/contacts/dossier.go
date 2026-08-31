package contacts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// DossierRootName is the canonical managed document root for longitudinal
// contact synthesis.
const DossierRootName = "contacts"

var dossierOutputContract = looppkg.OutputSpec{
	Name: "contact_dossier",
	Type: looppkg.OutputTypeMaintainedDocument,
	Facets: []looppkg.FacetSpec{
		{Name: looppkg.OutputFacetStatusLine},
		{Name: looppkg.OutputFacetTeaser},
		{Name: looppkg.OutputFacetDigest},
	},
}

var archiveSessionCitationPattern = regexp.MustCompile(`archive:session([:-])([[:alnum:]-]*)`)

// DossierWriteArgs carries the content projections for one canonical contact
// dossier. The contact UUID selects the structured identity; Go derives the
// document ref, subject tag, frontmatter, and section structure.
type DossierWriteArgs struct {
	ContactID  string `json:"contact_id"`
	StatusLine string `json:"status_line"`
	Teaser     string `json:"teaser"`
	Digest     string `json:"digest"`
	Full       string `json:"full"`
	// ReceiptScope is runtime-owned concurrency state and is never exposed to
	// the model.
	ReceiptScope string `json:"-"`
}

// ConfigureDossierDocuments installs the managed document surface used by
// [Tools.WriteDossier]. Configure it only after the document store has been
// initialized so an advertised mutation tool is always callable.
func (t *Tools) ConfigureDossierDocuments(write func(context.Context, documents.WriteArgs) (string, error)) {
	if t == nil {
		return
	}
	t.dossierWrite = write
}

// DossierWritesEnabled reports whether the configured contact tools have a
// writable canonical dossier root and a live managed document surface.
func (t *Tools) DossierWritesEnabled() bool {
	return t != nil && t.dossiersWritable && t.dossierWrite != nil
}

// DossierFacetFields returns the canonical model-facing projection fields in
// document order. Callers receive a copy and may safely enrich descriptions.
func DossierFacetFields() []looppkg.FacetField {
	return dossierOutputContract.FacetFields()
}

// WriteDossier creates or replaces one contact's canonical longitudinal
// dossier. It verifies the structured contact first, validates every content
// projection together, and leaves identity, frontmatter, and markdown
// structure to Go.
func (t *Tools) WriteDossier(ctx context.Context, args DossierWriteArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("contact directory not configured")
	}
	if !t.dossiersEnabled {
		return "", fmt.Errorf("contact dossiers are not configured")
	}
	if !t.dossiersWritable {
		return "", fmt.Errorf("the contacts dossier root is not managed-writable")
	}
	if t.dossierWrite == nil {
		return "", fmt.Errorf("contact dossier document tools are not configured")
	}

	rawID := strings.TrimSpace(args.ContactID)
	id, err := uuid.Parse(rawID)
	if err != nil || id == uuid.Nil || id.String() != rawID {
		return "", fmt.Errorf("contact_id must be a canonical non-zero UUID from contact_lookup or contact_owner; got %q", args.ContactID)
	}
	contact, err := t.store.Get(id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("contact_id %s is not an active structured contact; use contact_lookup to resolve the intended contact before writing a dossier", id)
	}
	if err != nil {
		return "", fmt.Errorf("load structured contact %s: %w", id, err)
	}

	payload := looppkg.FacetPayload{
		StatusLine: args.StatusLine,
		Teaser:     args.Teaser,
		Digest:     args.Digest,
		Full:       args.Full,
	}
	var validationErrs []error
	if err := dossierOutputContract.ValidateFacetPayload(payload); err != nil {
		validationErrs = append(validationErrs, err)
	}
	if err := validateDossierEvidenceCitations(args.Full); err != nil {
		validationErrs = append(validationErrs, err)
	}
	if len(validationErrs) > 0 {
		return "", fmt.Errorf("contact dossier projections are invalid; correct every listed field and retry once: %w", errors.Join(validationErrs...))
	}
	body := dossierOutputContract.RenderFacetDocument(payload)
	return t.dossierWrite(ctx, documents.WriteArgs{
		Ref:          DossierRef(id),
		Title:        contact.FormattedName,
		Tags:         []string{DossierSubject(id)},
		Body:         &body,
		ReceiptScope: args.ReceiptScope,
	})
}

// DossierRef returns the stable document ref for a contact UUID.
func DossierRef(id uuid.UUID) string {
	return DossierRootName + ":" + id.String() + ".md"
}

// DossierSubject returns the canonical request-subject and frontmatter tag
// that bind a dossier to its structured contact record.
func DossierSubject(id uuid.UUID) string {
	return "contact:" + id.String()
}

// ValidateDossierWrite enforces the contact dossier path, subject tag, and
// complete facet ladder before a managed document write can mutate the root.
func ValidateDossierWrite(candidate documents.DocumentWriteCandidate) error {
	id, err := dossierIDFromPath(candidate.Path)
	if err != nil {
		return err
	}

	wantSubject := DossierSubject(id)
	if len(candidate.Tags) != 1 || strings.TrimSpace(candidate.Tags[0]) != wantSubject {
		return fmt.Errorf("dossier %s must carry exactly one frontmatter tag, %q, so no broader subject can advertise this private dossier; use contact_dossier_write to let Go derive dossier identity and structure", candidate.Path, wantSubject)
	}

	payload, faceted := looppkg.ParseFacetSections(candidate.Body)
	if !faceted {
		return fmt.Errorf("dossier %s must use the status_line, teaser, digest, and full facet ladder", candidate.Path)
	}
	if err := dossierOutputContract.ValidateFacetPayload(payload); err != nil {
		return fmt.Errorf("dossier %s facet contract: %w", candidate.Path, err)
	}
	if err := validateDossierEvidenceCitations(payload.Full); err != nil {
		return fmt.Errorf("dossier %s evidence contract: %w", candidate.Path, err)
	}
	if got, want := strings.TrimSpace(candidate.Body), dossierOutputContract.RenderFacetDocument(payload); got != want {
		return fmt.Errorf("dossier %s must use the canonical facet section order with no text outside those sections", candidate.Path)
	}
	return nil
}

// validateDossierEvidenceCitations keeps archive claims independently
// checkable. Archive tools accept short session prefixes for interactive
// convenience, but imported sessions can share those prefixes; durable
// citations therefore need the full canonical UUID.
func validateDossierEvidenceCitations(full string) error {
	matches := archiveSessionCitationPattern.FindAllStringSubmatch(full, -1)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(matches))
	invalid := make([]string, 0)
	for _, match := range matches {
		citation := match[0]
		rawID := match[2]
		id, err := uuid.Parse(rawID)
		if match[1] == ":" && err == nil && id != uuid.Nil && id.String() == rawID {
			continue
		}
		if _, duplicate := seen[citation]; duplicate {
			continue
		}
		seen[citation] = struct{}{}
		invalid = append(invalid, citation)
	}
	if len(invalid) == 0 {
		return nil
	}
	sort.Strings(invalid)
	return fmt.Errorf("full has archive-session citation(s) [%s] without a full canonical UUID; replace each with archive:session:<full-session-uuid> from archive_search or archive_sessions because short prefixes can be ambiguous", strings.Join(invalid, ", "))
}

func dossierIDFromPath(relPath string) (uuid.UUID, error) {
	relPath = strings.TrimSpace(strings.ReplaceAll(relPath, `\`, "/"))
	if relPath == "" || strings.Contains(relPath, "/") || !strings.HasSuffix(relPath, ".md") {
		return uuid.Nil, fmt.Errorf("contacts root accepts only top-level <canonical-contact-uuid>.md dossiers; got %q", relPath)
	}
	rawID := strings.TrimSuffix(relPath, ".md")
	id, err := uuid.Parse(rawID)
	if err != nil || id == uuid.Nil || id.String() != rawID {
		return uuid.Nil, fmt.Errorf("contacts root dossier filename must contain a canonical non-zero contact UUID; got %q", relPath)
	}
	return id, nil
}
