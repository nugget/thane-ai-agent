package contacts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

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

// DossierReadArgs identifies one canonical contact dossier without exposing
// its derived document ref to the model.
type DossierReadArgs struct {
	ContactID string `json:"contact_id"`
	// ReceiptScope is runtime-owned concurrency state and is never exposed to
	// the model.
	ReceiptScope string `json:"-"`
}

// ConfigureDossierDocuments installs the managed document surface used by
// [Tools.ReadDossier] and [Tools.WriteDossier]. Configure it only after the
// document store has been initialized so every advertised tool is callable.
func (t *Tools) ConfigureDossierDocuments(
	read func(context.Context, documents.RefArgs) (string, error),
	write func(context.Context, documents.WriteArgs) (string, error),
) {
	if t == nil {
		return
	}
	t.dossierRead = read
	t.dossierWrite = write
}

// DossierReadsEnabled reports whether the configured canonical dossier root
// has a live managed document read surface.
func (t *Tools) DossierReadsEnabled() bool {
	return t != nil && t.dossiersEnabled && t.dossierRead != nil
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

// ReadDossier reads the canonical dossier for one active structured contact.
// An absent dossier is a successful, structured result that names the write
// door; retrying an unchanged document read cannot make an absent file appear.
func (t *Tools) ReadDossier(ctx context.Context, args DossierReadArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("contact directory not configured")
	}
	if !t.dossiersEnabled {
		return "", fmt.Errorf("contact dossiers are not configured")
	}
	if t.dossierRead == nil {
		return "", fmt.Errorf("contact dossier document reader is not configured")
	}

	contact, id, err := t.resolveDossierContact(args.ContactID)
	if err != nil {
		return "", err
	}
	ref := DossierRef(id)
	result, err := t.dossierRead(ctx, documents.RefArgs{Ref: ref, ReceiptScope: args.ReceiptScope})
	if documents.IsNotFound(err) {
		return marshalDossierAbsence(contact, ref, t.DossierWritesEnabled())
	}
	if err != nil {
		return "", fmt.Errorf("read contact dossier %s: %w", id, err)
	}
	return marshalDossierPresence(contact, ref, result)
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

	contact, id, err := t.resolveDossierContact(args.ContactID)
	if err != nil {
		return "", err
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
	if err := validateDossierSubjectIdentity(id, payload); err != nil {
		validationErrs = append(validationErrs, err)
	}
	if err := validateDossierSubjectName(contact.FormattedName, payload); err != nil {
		validationErrs = append(validationErrs, err)
	}
	if err := validateDossierEvidenceCitations(payload); err != nil {
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

func (t *Tools) resolveDossierContact(rawID string) (*Contact, uuid.UUID, error) {
	rawID = strings.TrimSpace(rawID)
	id, err := uuid.Parse(rawID)
	if err != nil || id == uuid.Nil || id.String() != rawID {
		return nil, uuid.Nil, fmt.Errorf("contact_id must be a canonical non-zero UUID from contact_lookup or contact_owner; got %q", rawID)
	}
	contact, err := t.store.Get(id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, uuid.Nil, fmt.Errorf("contact_id %s is not an active structured contact; call contact_lookup to resolve the intended contact instead of retrying this UUID", id)
	}
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("load structured contact %s: %w", id, err)
	}
	return contact, id, nil
}

type dossierReadResult struct {
	ContactID   string             `json:"contact_id"`
	ContactName string             `json:"contact_name"`
	Dossier     dossierReadState   `json:"dossier"`
	NextAction  *dossierNextAction `json:"next_action"`
}

type dossierReadState struct {
	Exists   bool            `json:"exists"`
	Ref      string          `json:"ref"`
	Document json.RawMessage `json:"document"`
}

type dossierNextAction struct {
	Tool        string `json:"tool,omitempty"`
	ContactID   string `json:"contact_id,omitempty"`
	Instruction string `json:"instruction"`
}

func marshalDossierPresence(contact *Contact, ref, document string) (string, error) {
	raw := json.RawMessage(document)
	if !json.Valid(raw) {
		return "", fmt.Errorf("contact dossier reader returned invalid JSON for %s", contact.ID)
	}
	return marshalDossierReadResult(dossierReadResult{
		ContactID:   contact.ID.String(),
		ContactName: contact.FormattedName,
		Dossier: dossierReadState{
			Exists:   true,
			Ref:      ref,
			Document: raw,
		},
	})
}

func marshalDossierAbsence(contact *Contact, ref string, writable bool) (string, error) {
	result := dossierReadResult{
		ContactID:   contact.ID.String(),
		ContactName: contact.FormattedName,
		Dossier: dossierReadState{
			Exists: false,
			Ref:    ref,
		},
	}
	if writable {
		result.NextAction = &dossierNextAction{
			Tool:      "contact_dossier_write",
			ContactID: contact.ID.String(),
			Instruction: "Create the dossier with all four projections; do not retry " +
				"contact_dossier_read until a write succeeds.",
		}
	} else {
		result.NextAction = &dossierNextAction{
			Instruction: "No canonical dossier exists and this contact root is read-only; do not retry the read.",
		}
	}
	return marshalDossierReadResult(result)
}

func marshalDossierReadResult(result dossierReadResult) (string, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("encode contact dossier result: %w", err)
	}
	return string(raw), nil
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

// NewDossierWriteValidator returns the root policy for canonical contact
// dossiers. resolveContactName must read the active structured contact on every
// invocation so contact renames take effect immediately and caller-controlled
// frontmatter cannot spoof the identity used to validate compact projections.
func NewDossierWriteValidator(resolveContactName func(uuid.UUID) (string, error)) documents.RootWriteValidator {
	return func(candidate documents.DocumentWriteCandidate) error {
		return validateDossierWrite(candidate, resolveContactName)
	}
}

func validateDossierWrite(candidate documents.DocumentWriteCandidate, resolveContactName func(uuid.UUID) (string, error)) error {
	id, err := dossierIDFromPath(candidate.Path)
	if err != nil {
		return err
	}

	wantSubject := DossierSubject(id)
	if len(candidate.Tags) != 1 || strings.TrimSpace(candidate.Tags[0]) != wantSubject {
		return fmt.Errorf("dossier %s must carry exactly one frontmatter tag, %q, so no broader subject can advertise this private dossier; use contact_dossier_write to let Go derive dossier identity and structure", candidate.Path, wantSubject)
	}
	if resolveContactName == nil {
		return fmt.Errorf("dossier %s cannot validate identity because the structured contact resolver is not configured", candidate.Path)
	}
	contactName, err := resolveContactName(id)
	if err != nil {
		return fmt.Errorf("dossier %s cannot resolve active structured contact %s: %w", candidate.Path, id, err)
	}
	contactName = strings.TrimSpace(contactName)
	if contactName == "" {
		return fmt.Errorf("dossier %s cannot validate identity because structured contact %s has no formatted name", candidate.Path, id)
	}

	payload, faceted := looppkg.ParseFacetSections(candidate.Body)
	if !faceted {
		return fmt.Errorf("dossier %s must use the status_line, teaser, digest, and full facet ladder", candidate.Path)
	}
	var validationErrs []error
	if err := dossierOutputContract.ValidateFacetPayload(payload); err != nil {
		validationErrs = append(validationErrs, fmt.Errorf("facet contract: %w", err))
	}
	if err := validateDossierSubjectIdentity(id, payload); err != nil {
		validationErrs = append(validationErrs, fmt.Errorf("identity contract: %w", err))
	}
	if titles := candidate.Frontmatter["title"]; len(titles) != 1 || strings.TrimSpace(titles[0]) != contactName {
		validationErrs = append(validationErrs, fmt.Errorf("identity contract: dossier must carry exactly one title %q matching active structured contact %s; use contact_dossier_write to let Go derive it", contactName, id))
	}
	if err := validateDossierSubjectName(contactName, payload); err != nil {
		validationErrs = append(validationErrs, fmt.Errorf("identity contract: %w", err))
	}
	if err := validateDossierEvidenceCitations(payload); err != nil {
		validationErrs = append(validationErrs, fmt.Errorf("evidence contract: %w", err))
	}
	if len(validationErrs) > 0 {
		return fmt.Errorf("dossier %s projections are invalid; correct every listed field and retry once: %w", candidate.Path, errors.Join(validationErrs...))
	}
	if got, want := strings.TrimSpace(candidate.Body), dossierOutputContract.RenderFacetDocument(payload); got != want {
		return fmt.Errorf("dossier %s must use the canonical facet section order with no text outside those sections", candidate.Path)
	}
	return nil
}

// validateDossierSubjectIdentity keeps mechanically owned identity out of the
// authored projections. The path and private subject tag already carry the
// dossier's contact UUID; repeating it spends model attention and can drift
// into a second, less reliable statement of the same identity. Other contact
// UUIDs remain valid cross-references in relationship content.
func validateDossierSubjectIdentity(id uuid.UUID, payload looppkg.FacetPayload) error {
	fields := []struct {
		name  string
		value string
	}{
		{name: "status_line", value: payload.StatusLine},
		{name: "teaser", value: payload.Teaser},
		{name: "digest", value: payload.Digest},
		{name: "full", value: payload.Full},
	}

	var redundant []string
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field.value), id.String()) {
			redundant = append(redundant, field.name)
		}
	}
	if len(redundant) == 0 {
		return nil
	}
	return fmt.Errorf("projection(s) [%s] repeat the subject contact UUID %s; omit that UUID and its derived contacts ref or contact tag because Go already binds the document path and frontmatter to this structured contact", strings.Join(redundant, ", "), id)
}

// validateDossierSubjectName keeps the compact lookup projections focused on
// relationship signal. The structured contact and dossier title already name
// the subject, while digest and full remain standalone prose where using the
// name can improve clarity.
func validateDossierSubjectName(name string, payload looppkg.FacetPayload) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}

	fields := []struct {
		name  string
		value string
	}{
		{name: "status_line", value: payload.StatusLine},
		{name: "teaser", value: payload.Teaser},
	}

	var redundant []string
	for _, field := range fields {
		if containsFoldedPhrase(field.value, name) {
			redundant = append(redundant, field.name)
		}
	}
	if len(redundant) == 0 {
		return nil
	}
	return fmt.Errorf("projection(s) [%s] repeat the subject contact name %q; omit the name because the structured contact and dossier title already identify the subject; digest and full may use it when standalone prose needs it", strings.Join(redundant, ", "), name)
}

func containsFoldedPhrase(text, phrase string) bool {
	phrase = strings.TrimSpace(phrase)
	if phrase == "" {
		return false
	}

	textRunes := []rune(text)
	phraseRunes := []rune(phrase)
	for start := 0; start+len(phraseRunes) <= len(textRunes); start++ {
		end := start + len(phraseRunes)
		if !strings.EqualFold(string(textRunes[start:end]), phrase) {
			continue
		}
		leftBoundary := start == 0 || !isDossierNameRune(textRunes[start-1])
		rightBoundary := end == len(textRunes) || !isDossierNameRune(textRunes[end])
		if leftBoundary && rightBoundary {
			return true
		}
	}
	return false
}

func isDossierNameRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r)
}

// validateDossierEvidenceCitations keeps archive claims independently
// checkable. Archive tools accept short session prefixes for interactive
// convenience, but imported sessions can share those prefixes; durable
// citations therefore need the full canonical UUID.
func validateDossierEvidenceCitations(payload looppkg.FacetPayload) error {
	fields := []struct {
		name  string
		value string
	}{
		{name: "status_line", value: payload.StatusLine},
		{name: "teaser", value: payload.Teaser},
		{name: "digest", value: payload.Digest},
		{name: "full", value: payload.Full},
	}

	seen := make(map[string]struct{})
	invalid := make([]string, 0)
	for _, field := range fields {
		for _, match := range archiveSessionCitationPattern.FindAllStringSubmatch(field.value, -1) {
			citation := match[0]
			rawID := match[2]
			id, err := uuid.Parse(rawID)
			if match[1] == ":" && err == nil && id != uuid.Nil && id.String() == rawID {
				continue
			}
			key := field.name + "\x00" + citation
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			invalid = append(invalid, field.name+"="+citation)
		}
	}
	if len(invalid) == 0 {
		return nil
	}
	sort.Strings(invalid)
	return fmt.Errorf("archive-session citation(s) [%s] do not use a full canonical UUID; replace each with archive:session:<full-session-uuid> from archive_search or archive_sessions because short prefixes can be ambiguous", strings.Join(invalid, ", "))
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
