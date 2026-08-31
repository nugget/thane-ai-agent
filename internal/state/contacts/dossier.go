package contacts

import (
	"fmt"
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
	foundSubject := false
	for _, tag := range candidate.Tags {
		tag = strings.TrimSpace(tag)
		if tag == wantSubject {
			foundSubject = true
			continue
		}
		if strings.HasPrefix(tag, "contact:") {
			return fmt.Errorf("dossier %s carries mismatched contact tag %q; use exactly %q for its structured contact", candidate.Path, tag, wantSubject)
		}
	}
	if !foundSubject {
		return fmt.Errorf("dossier %s must carry frontmatter tag %q so request context can resolve it without guessing from prose", candidate.Path, wantSubject)
	}

	payload, faceted := looppkg.ParseFacetSections(candidate.Body)
	if !faceted {
		return fmt.Errorf("dossier %s must use the status_line, teaser, digest, and full facet ladder", candidate.Path)
	}
	if err := dossierOutputContract.ValidateFacetPayload(payload); err != nil {
		return fmt.Errorf("dossier %s facet contract: %w", candidate.Path, err)
	}
	if got, want := strings.TrimSpace(candidate.Body), dossierOutputContract.RenderFacetDocument(payload); got != want {
		return fmt.Errorf("dossier %s must use the canonical facet section order with no text outside those sections", candidate.Path)
	}
	return nil
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
