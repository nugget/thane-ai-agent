package contacts

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// EmbeddingClient generates embeddings for semantic search.
type EmbeddingClient interface {
	Generate(ctx context.Context, text string) ([]float32, error)
}

// OwnerChannelActivity describes one currently active owner-scoped
// interactive channel loop.
type OwnerChannelActivity struct {
	Channel        string
	LoopID         string
	LoopName       string
	ConversationID string
	ContactName    string
	State          string
	LastActive     time.Time
}

const ownerActivitySummaryLimit = 8

// Tools provides contact-related tools for the agent.
type Tools struct {
	store             *Store
	embeddings        EmbeddingClient
	selfContactName   string
	operatorContactID uuid.UUID
	ownerContactName  string
	ownerActivity     func() []OwnerChannelActivity
	dossiersEnabled   bool
	dossiersWritable  bool
	dossierRead       func(context.Context, documents.RefArgs) (string, error)
	dossierWrite      func(context.Context, documents.WriteArgs) (string, error)
	mutationSink      func(context.Context, ContactMutation) error
}

// ContactMutation describes one committed contact_save change for downstream
// consumers such as the archivist queue. Fields names the structured scalar
// or property keys whose authority changed.
type ContactMutation struct {
	ContactID   uuid.UUID           `json:"contact_id"`
	ContactName string              `json:"contact_name"`
	Created     bool                `json:"created"`
	Fields      []string            `json:"fields"`
	Provenance  *PropertyProvenance `json:"provenance"`
}

// NewTools creates contact tools using the given store and optional committed
// mutation sink. The sink is a construction dependency so a live tool surface
// cannot race with post-startup rewiring.
func NewTools(store *Store, mutationSink func(context.Context, ContactMutation) error) *Tools {
	return &Tools{store: store, mutationSink: mutationSink}
}

// SetEmbeddingClient sets the embedding client for semantic search.
func (t *Tools) SetEmbeddingClient(client EmbeddingClient) {
	t.embeddings = client
}

// SetSelfContactName sets the contact name used to resolve name="self"
// in export operations.
func (t *Tools) SetSelfContactName(name string) {
	t.selfContactName = name
}

// ConfigureOperatorContactID sets the stable contact UUID used to resolve the
// primary human operator.
func (t *Tools) ConfigureOperatorContactID(id uuid.UUID) {
	t.operatorContactID = id
}

// ConfigureDossierRoot controls whether rich contact results expose the
// deterministic dossier trailhead and whether that trailhead may suggest
// creating an absent dossier.
func (t *Tools) ConfigureDossierRoot(enabled, writable bool) {
	t.dossiersEnabled = enabled
	t.dossiersWritable = enabled && writable
}

// SetOwnerContactName sets the contact name used to resolve the
// primary human operator contact for legacy configurations.
func (t *Tools) SetOwnerContactName(name string) {
	t.ownerContactName = name
}

// SetOwnerActivitySource configures a source of active owner-scoped
// channel activity for the contact_owner helper.
func (t *Tools) SetOwnerActivitySource(src func() []OwnerChannelActivity) {
	t.ownerActivity = src
}

// OwnerContact returns the configured operator contact, or falls back to
// the sole admin contact when no explicit operator identity is set.
func (t *Tools) OwnerContact(_ string) (string, error) {
	c, err := t.resolveOwnerContact()
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString(t.formatContact(c))
	if summary := t.formatOwnerActivitySummary(); summary != "" {
		sb.WriteString("\n\n")
		sb.WriteString(summary)
	}
	return sb.String(), nil
}

func (t *Tools) resolveOwnerContact() (*Contact, error) {
	if t.operatorContactID != uuid.Nil {
		full, err := t.store.GetWithProperties(t.operatorContactID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("configured operator contact %s not found", t.operatorContactID)
		}
		if err != nil {
			return nil, fmt.Errorf("get configured operator contact details: %w", err)
		}
		return full, nil
	}

	name := strings.TrimSpace(t.ownerContactName)
	if name != "" {
		c, err := t.store.ResolveContact(name)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("configured legacy operator contact name %q not found", name)
		}
		if err != nil {
			return nil, fmt.Errorf("resolve configured legacy operator contact name: %w", err)
		}
		full, err := t.store.GetWithProperties(c.ID)
		if err != nil {
			return nil, fmt.Errorf("get configured operator contact details: %w", err)
		}
		return full, nil
	}

	admins, err := t.store.FindByTrustZone(ZoneAdmin)
	if err != nil {
		return nil, fmt.Errorf("list admin contacts: %w", err)
	}
	switch len(admins) {
	case 0:
		return nil, fmt.Errorf("operator contact not configured: set identity.operator_contact_id or mark exactly one admin contact")
	case 1:
		full, err := t.store.GetWithProperties(admins[0].ID)
		if err != nil {
			return nil, fmt.Errorf("get operator contact details: %w", err)
		}
		return full, nil
	default:
		names := make([]string, 0, len(admins))
		for _, admin := range admins {
			names = append(names, admin.FormattedName)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("operator contact is ambiguous: multiple admin contacts found (%s); set identity.operator_contact_id", strings.Join(names, ", "))
	}
}

// SaveContactArgs are arguments for the contact_save tool.
type SaveContactArgs struct {
	Name              string            `json:"name"`                          // maps to FormattedName
	Kind              string            `json:"kind,omitempty"`                // individual, group, org, location
	TrustZone         string            `json:"trust_zone,omitempty"`          // admin, household, trusted, known
	GivenName         string            `json:"given_name,omitempty"`          // vCard N given name
	FamilyName        string            `json:"family_name,omitempty"`         // vCard N family name
	Nickname          string            `json:"nickname,omitempty"`            // vCard NICKNAME
	Org               string            `json:"org,omitempty"`                 // vCard ORG
	Title             string            `json:"title,omitempty"`               // vCard TITLE
	Role              string            `json:"role,omitempty"`                // vCard ROLE
	Note              string            `json:"note,omitempty"`                // vCard NOTE
	AISummary         string            `json:"ai_summary,omitempty"`          // AI-generated context
	OriginTags        []string          `json:"origin_tags,omitempty"`         // tags pinned when this contact is the session origin
	OriginContextRefs []string          `json:"origin_context_refs,omitempty"` // refs injected when this contact is the session origin
	Facts             map[string]string `json:"facts,omitempty"`               // freeform AI metadata
}

// propertyKeys lists fact keys that should be stored as vCard properties
// in contact_properties rather than freeform facts.
var propertyKeys = map[string]string{
	"email":  "EMAIL",
	"phone":  "TEL",
	"signal": "IMPP",
	"matrix": "IMPP",
}

// saveContactKnownFields lists the top-level JSON keys that SaveContactArgs
// recognizes. Any other top-level string values are rescued into the Facts map
// so models that flatten email, phone, etc. don't lose data silently.
var saveContactKnownFields = map[string]bool{
	"name": true, "kind": true, "trust_zone": true,
	"given_name": true, "family_name": true, "nickname": true,
	"org": true, "title": true, "role": true,
	"note": true, "ai_summary": true, "origin_tags": true,
	"origin_context_refs": true, "facts": true,
}

// SaveContact creates or updates a contact. When a contact with the
// given name already exists, only non-empty fields are overwritten.
// Facts are additive. Email and phone values are stored as vCard
// properties (EMAIL, TEL) in contact_properties.
//
// Top-level string fields that don't match known SaveContactArgs keys
// (e.g., "email", "phone") are automatically rescued into the Facts
// map or contact_properties, since models frequently flatten them.
func (t *Tools) SaveContact(argsJSON string) (string, error) {
	return t.saveContact(context.Background(), argsJSON, nil, false)
}

// SaveContactFromModel applies contact_save with the current model turn's
// provenance and emits the configured post-commit mutation signal.
func (t *Tools) SaveContactFromModel(ctx context.Context, argsJSON string, provenance *PropertyProvenance) (string, error) {
	return t.saveContact(ctx, argsJSON, provenance, true)
}

func (t *Tools) saveContact(
	ctx context.Context,
	argsJSON string,
	provenance *PropertyProvenance,
	notify bool,
) (string, error) {
	var args SaveContactArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	// Rescue top-level string fields that should be knowledge.
	var raw map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &raw); err == nil {
		if args.Facts == nil {
			args.Facts = make(map[string]string)
		}
		var rescued []string
		for k, v := range raw {
			if saveContactKnownFields[k] {
				continue
			}
			if _, exists := args.Facts[k]; exists {
				continue
			}
			if s, ok := v.(string); ok && s != "" {
				args.Facts[k] = s
				rescued = append(rescued, k)
			}
		}
		if len(rescued) > 0 {
			sort.Strings(rescued)
			slog.Debug("rescued top-level fields as facts",
				"name", args.Name, "fields", rescued)
		}
	}

	if args.Name == "" {
		return "", fmt.Errorf("name is required")
	}

	// Trust zones are operator custody, not contact data: a zone now
	// confers inherited authority on every companion device bound to
	// the contact (#1450), so the everyday save path must never be a
	// promotion path. The operator assigns zones through CardDAV
	// (X-THANE-TRUST-ZONE) or direct curation.
	if args.TrustZone != "" {
		return "", fmt.Errorf("trust_zone cannot be set through contact_save: zones are operator-custodied and confer device authority (#1450); ask the operator to assign the zone, then retry without trust_zone")
	}

	// Look for existing contact by name.
	existing, err := t.store.FindByName(args.Name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("find contact: %w", err)
	}

	created := existing == nil
	var contact *Contact
	if created {
		contact = &Contact{
			FormattedName: args.Name,
			Kind:          args.Kind,
			GivenName:     args.GivenName,
			FamilyName:    args.FamilyName,
			Nickname:      args.Nickname,
			Org:           args.Org,
			Title:         args.Title,
			Role:          args.Role,
			Note:          args.Note,
			AISummary:     args.AISummary,
		}
	} else {
		contact, err = t.store.GetWithProperties(existing.ID)
		if err != nil {
			return "", fmt.Errorf("load contact for update: %w", err)
		}
	}

	changedFields := make(map[string]struct{})
	contactChanged := created
	if created {
		markCreatedContactFields(args, changedFields)
	} else {
		contactChanged = applyContactScalarUpdates(contact, args, changedFields)
	}

	additions := additiveProperties(args.Facts, contact.Properties, provenance)
	for _, property := range additions {
		changedFields["property:"+property.Property] = struct{}{}
	}
	replacements := make(map[string][]Property)
	collectReplacement := func(property string, provided []string) {
		if provided == nil {
			return
		}
		values := cleanOriginValues(provided)
		if propertyValuesEqual(contact.Properties, property, values) {
			return
		}
		props := make([]Property, 0, len(values))
		for _, value := range values {
			props = append(props, Property{Property: property, Value: value, Provenance: provenance})
		}
		replacements[property] = props
		changedFields["property:"+property] = struct{}{}
	}
	collectReplacement(PropertyOriginTag, args.OriginTags)
	collectReplacement(PropertyOriginContextRef, args.OriginContextRefs)

	saved, changed, err := t.store.applyContactSave(contact, contactChanged, additions, replacements)
	if err != nil {
		if created {
			return "", fmt.Errorf("create contact: %w", err)
		}
		return "", fmt.Errorf("update contact: %w", err)
	}
	if !changed {
		return fmt.Sprintf("Contact unchanged: **%s** (%s); no dossier refresh was queued", saved.FormattedName, saved.Kind), nil
	}

	t.generateEmbedding(saved)
	fields := sortedKeys(changedFields)
	if notify && t.mutationSink != nil {
		mutation := ContactMutation{
			ContactID:   saved.ID,
			ContactName: saved.FormattedName,
			Created:     created,
			Fields:      fields,
			Provenance:  provenance,
		}
		if err := t.mutationSink(ctx, mutation); err != nil {
			return "", fmt.Errorf("contact write committed for %s but dossier refresh enqueue failed; do not repeat contact_save: %w", saved.ID, err)
		}
	}

	if created {
		return fmt.Sprintf("Saved new contact: **%s** (%s)", saved.FormattedName, saved.Kind), nil
	}
	return fmt.Sprintf("Updated contact: **%s** (%s)", saved.FormattedName, saved.Kind), nil
}

func markCreatedContactFields(args SaveContactArgs, changed map[string]struct{}) {
	changed["formatted_name"] = struct{}{}
	changed["kind"] = struct{}{}
	for field, value := range map[string]string{
		"given_name":  args.GivenName,
		"family_name": args.FamilyName,
		"nickname":    args.Nickname,
		"org":         args.Org,
		"title":       args.Title,
		"role":        args.Role,
		"note":        args.Note,
		"ai_summary":  args.AISummary,
	} {
		if value != "" {
			changed[field] = struct{}{}
		}
	}
}

func applyContactScalarUpdates(contact *Contact, args SaveContactArgs, changed map[string]struct{}) bool {
	updated := false
	set := func(field string, target *string, value string) {
		if value == "" || *target == value {
			return
		}
		*target = value
		changed[field] = struct{}{}
		updated = true
	}
	set("kind", &contact.Kind, args.Kind)
	set("given_name", &contact.GivenName, args.GivenName)
	set("family_name", &contact.FamilyName, args.FamilyName)
	set("nickname", &contact.Nickname, args.Nickname)
	set("org", &contact.Org, args.Org)
	set("title", &contact.Title, args.Title)
	set("role", &contact.Role, args.Role)
	set("note", &contact.Note, args.Note)
	set("ai_summary", &contact.AISummary, args.AISummary)
	return updated
}

func additiveProperties(facts map[string]string, existing []Property, provenance *PropertyProvenance) []Property {
	keys := make([]string, 0, len(facts))
	for key := range facts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	properties := make([]Property, 0, len(keys))
	for _, key := range keys {
		property := propertyKeys[key]
		if property == "" {
			property = key
		}
		value := facts[key]
		if property == "IMPP" && !strings.HasPrefix(value, key+":") {
			value = key + ":" + value
		}
		if hasProperty(existing, property, value) {
			continue
		}
		properties = append(properties, Property{
			Property:   property,
			Value:      value,
			Provenance: provenance,
		})
	}
	return properties
}

func hasProperty(properties []Property, property, value string) bool {
	for _, candidate := range properties {
		if candidate.Property == property && strings.EqualFold(candidate.Value, value) {
			return true
		}
	}
	return false
}

func propertyValuesEqual(properties []Property, property string, want []string) bool {
	got := make([]string, 0, len(want))
	for _, candidate := range properties {
		if candidate.Property == property {
			got = append(got, candidate.Value)
		}
	}
	return slices.Equal(got, want)
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// LookupContactArgs are arguments for the contact_lookup tool.
type LookupContactArgs struct {
	Name  string `json:"name,omitempty"`
	Query string `json:"query,omitempty"`
	Kind  string `json:"kind,omitempty"`
	Key   string `json:"key,omitempty"`   // property or fact key filter
	Value string `json:"value,omitempty"` // property or fact value filter
}

// LookupContact retrieves contacts from the directory.
func (t *Tools) LookupContact(argsJSON string) (string, error) {
	var args LookupContactArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	// Name lookup (cascading: formatted name → nickname → search).
	if args.Name != "" {
		c, err := t.store.ResolveContact(args.Name)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Sprintf("No contact found named %q", args.Name), nil
		}
		if err != nil {
			return "", fmt.Errorf("resolve contact: %w", err)
		}
		c, err = t.store.GetWithProperties(c.ID)
		if err != nil {
			return "", fmt.Errorf("get contact details: %w", err)
		}
		return t.formatContact(c), nil
	}

	// Property filter.
	if args.Key != "" && args.Value != "" {
		// Map known lowercase keys to their vCard property names.
		propName, isVCard := propertyKeys[args.Key]
		if !isVCard {
			propName = args.Key
		}
		contacts, err := t.store.FindByProperty(propName, args.Value)
		if err != nil {
			return "", fmt.Errorf("find by property: %w", err)
		}
		if len(contacts) == 0 {
			return fmt.Sprintf("No contacts with %s matching %q", args.Key, args.Value), nil
		}
		return formatContactList(contacts), nil
	}

	// Kind filter.
	if args.Kind != "" {
		contacts, err := t.store.ListByKind(args.Kind)
		if err != nil {
			return "", fmt.Errorf("list by kind: %w", err)
		}
		if len(contacts) == 0 {
			return fmt.Sprintf("No %s contacts found", args.Kind), nil
		}
		return formatContactList(contacts), nil
	}

	// Search.
	if args.Query != "" {
		contacts, err := t.store.Search(args.Query)
		if err != nil {
			return "", fmt.Errorf("search: %w", err)
		}
		if len(contacts) == 0 {
			return fmt.Sprintf("No contacts matching %q", args.Query), nil
		}
		return formatContactList(contacts), nil
	}

	// List stats.
	stats := t.store.Stats()
	total, _ := stats["total"].(int)
	kinds, _ := stats["kinds"].(map[string]int)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Contact directory contains %d contacts:\n", total))
	for kind, count := range kinds {
		sb.WriteString(fmt.Sprintf("  - %s: %d\n", kind, count))
	}
	return sb.String(), nil
}

// ForgetContactArgs are arguments for the contact_forget tool.
type ForgetContactArgs struct {
	Name string `json:"name"`
}

// ForgetContact soft-deletes a contact by name.
func (t *Tools) ForgetContact(argsJSON string) (string, error) {
	var args ForgetContactArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	if args.Name == "" {
		return "", fmt.Errorf("name is required")
	}

	if err := t.store.DeleteByName(args.Name); err != nil {
		return "", fmt.Errorf("delete contact: %w", err)
	}

	return fmt.Sprintf("Forgot contact: %s", args.Name), nil
}

// ListContactsArgs are arguments for the contact_list tool.
type ListContactsArgs struct {
	Kind  string `json:"kind,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// ListContacts returns contacts from the directory, optionally filtered
// by kind and capped by a limit.
func (t *Tools) ListContacts(argsJSON string) (string, error) {
	var args ListContactsArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	var contacts []*Contact
	var err error

	if args.Kind != "" {
		contacts, err = t.store.ListByKind(args.Kind)
	} else {
		contacts, err = t.store.ListAll()
	}
	if err != nil {
		return "", fmt.Errorf("list contacts: %w", err)
	}

	if args.Limit > 0 && len(contacts) > args.Limit {
		contacts = contacts[:args.Limit]
	}

	if len(contacts) == 0 {
		if args.Kind != "" {
			return fmt.Sprintf("No %s contacts found", args.Kind), nil
		}
		return "No contacts in directory", nil
	}

	return formatContactList(contacts), nil
}

// GenerateMissingEmbeddings creates embeddings for contacts that don't have them.
func (t *Tools) GenerateMissingEmbeddings() (int, error) {
	if t.embeddings == nil {
		return 0, fmt.Errorf("embedding client not configured")
	}

	contacts, err := t.store.GetContactsWithoutEmbeddings()
	if err != nil {
		return 0, err
	}

	count := 0
	for _, c := range contacts {
		props, _ := t.store.GetProperties(c.ID)
		embText := buildEmbeddingText(c, props)
		emb, err := t.embeddings.Generate(context.Background(), embText)
		if err != nil {
			continue
		}
		if err := t.store.SetEmbedding(c.ID, emb); err != nil {
			continue
		}
		count++
	}

	return count, nil
}

// generateEmbedding creates and stores an embedding for a contact.
func (t *Tools) generateEmbedding(c *Contact) {
	if t.embeddings == nil {
		return
	}

	props, _ := t.store.GetProperties(c.ID)
	embText := buildEmbeddingText(c, props)
	emb, err := t.embeddings.Generate(context.Background(), embText)
	if err != nil {
		return
	}
	_ = t.store.SetEmbedding(c.ID, emb)
}

// buildEmbeddingText creates text for embedding from a contact and its
// properties.
func buildEmbeddingText(c *Contact, props []Property) string {
	var sb strings.Builder
	sb.WriteString(c.FormattedName)
	if c.Kind != "" {
		sb.WriteString(" (" + c.Kind + ")")
	}
	if c.Org != "" {
		sb.WriteString(" - " + c.Org)
	}
	if c.Title != "" {
		sb.WriteString(", " + c.Title)
	}
	if c.AISummary != "" {
		sb.WriteString(": " + c.AISummary)
	}
	if c.Note != "" {
		sb.WriteString("\n" + c.Note)
	}

	for _, p := range props {
		sb.WriteString(fmt.Sprintf("\n%s: %s", p.Property, p.Value))
	}
	return sb.String()
}

// ExportVCFArgs are arguments for the contact_export_vcf tool.
type ExportVCFArgs struct {
	Name               string `json:"name"`
	RecipientTrustZone string `json:"recipient_trust_zone,omitempty"`
	Format             string `json:"format,omitempty"` // "file" (default) or "text"
}

// ExportVCF exports a single contact as a vCard. When name is "self",
// it resolves via the configured self-contact name. The optional
// recipient_trust_zone applies trust-zone field filtering (self-contact
// only).
func (t *Tools) ExportVCF(argsJSON string) (string, error) {
	var args ExportVCFArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	name := args.Name
	if name == "" {
		return "", fmt.Errorf("name is required")
	}

	isSelf := strings.EqualFold(name, "self")
	if isSelf {
		if t.selfContactName == "" {
			return "", fmt.Errorf("self-contact not configured: set identity.contact_name in config")
		}
		name = t.selfContactName
	}

	c, err := t.store.ResolveContact(name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("contact %q not found", name)
	}
	if err != nil {
		return "", fmt.Errorf("resolve contact: %w", err)
	}

	c, err = t.store.GetWithProperties(c.ID)
	if err != nil {
		return "", fmt.Errorf("get contact details: %w", err)
	}

	card := ContactToCard(c)

	// Apply trust-zone filtering for self-contact exports.
	if isSelf && args.RecipientTrustZone != "" {
		card = FilterCardForTrustZone(card, args.RecipientTrustZone, c.Properties)
	}

	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
		return "", fmt.Errorf("encode vcard: %w", err)
	}
	text := buf.String()

	if args.Format == "text" {
		return text, nil
	}

	// Write to temp file.
	f, err := os.CreateTemp("", "thane-vcf-*.vcf")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(text); err != nil {
		return "", fmt.Errorf("write vcf: %w", err)
	}

	return fmt.Sprintf("Exported vCard to %s", f.Name()), nil
}

// ExportAllVCFArgs are arguments for the contact_export_all_vcf tool.
type ExportAllVCFArgs struct {
	Kind      string `json:"kind,omitempty"`
	TrustZone string `json:"trust_zone,omitempty"`
}

// ExportAllVCF exports all contacts (optionally filtered) as a
// multi-vCard file.
func (t *Tools) ExportAllVCF(argsJSON string) (string, error) {
	var args ExportAllVCFArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	var contacts []*Contact
	var err error

	switch {
	case args.TrustZone != "" && args.Kind != "":
		// Both filters: load by trust zone, then filter by kind.
		contacts, err = t.store.FindByTrustZone(args.TrustZone)
		if err == nil {
			filtered := contacts[:0]
			for _, c := range contacts {
				if c.Kind == args.Kind {
					filtered = append(filtered, c)
				}
			}
			contacts = filtered
		}
	case args.TrustZone != "":
		contacts, err = t.store.FindByTrustZone(args.TrustZone)
	case args.Kind != "":
		contacts, err = t.store.ListByKind(args.Kind)
	default:
		contacts, err = t.store.ListAll()
	}
	if err != nil {
		return "", fmt.Errorf("list contacts: %w", err)
	}

	if len(contacts) == 0 {
		return "No contacts to export", nil
	}

	// Load properties for each contact.
	var withProps []*Contact
	for _, c := range contacts {
		full, err := t.store.GetWithProperties(c.ID)
		if err != nil {
			continue
		}
		withProps = append(withProps, full)
	}

	text, err := EncodeVCards(withProps)
	if err != nil {
		return "", fmt.Errorf("encode vcards: %w", err)
	}

	f, err := os.CreateTemp("", "thane-vcf-all-*.vcf")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(text); err != nil {
		return "", fmt.Errorf("write vcf: %w", err)
	}

	return fmt.Sprintf("Exported %d contacts to %s", len(withProps), f.Name()), nil
}

// ImportVCFArgs are arguments for the contact_import_vcf tool.
type ImportVCFArgs struct {
	Path   string `json:"path,omitempty"`
	Text   string `json:"text,omitempty"`
	Merge  *bool  `json:"merge,omitempty"` // default true
	DryRun bool   `json:"dry_run,omitempty"`
}

// ImportVCF imports contacts from a vCard file or text. When merge is
// true (default), existing contacts are matched by EMAIL then by name,
// and only empty fields are filled. TrustZone and AISummary are never
// overwritten during merge. Properties are additive.
func (t *Tools) ImportVCF(argsJSON string) (string, error) {
	var args ImportVCFArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	merge := args.Merge == nil || *args.Merge

	var r io.Reader
	if args.Path != "" {
		f, err := os.Open(args.Path)
		if err != nil {
			return "", fmt.Errorf("open vcf file: %w", err)
		}
		defer f.Close()
		r = f
	} else if args.Text != "" {
		r = strings.NewReader(args.Text)
	} else {
		return "", fmt.Errorf("one of path or text is required")
	}

	decoded, allProps, err := DecodeVCards(r)
	if err != nil {
		return "", fmt.Errorf("decode vcards: %w", err)
	}

	var created, updated, skipped int
	var summary strings.Builder

	for i, incoming := range decoded {
		props := allProps[i]

		// Trust zone is operator custody, not importable data (#1450):
		// a zone confers inherited authority on bound companion devices,
		// so a model-supplied vCard must not mint an elevated contact
		// through X-THANE-TRUST-ZONE. New contacts always start at the
		// default zone; the merge path already never overwrites an
		// existing zone. The operator sets zones through CardDAV, whose
		// backend decodes the same header on an authenticated surface.
		incoming.TrustZone = ZoneKnown

		// Try to find existing contact for merge.
		var existing *Contact
		if merge {
			existing = t.findExistingForMerge(incoming, props)
		}

		if args.DryRun {
			if existing != nil {
				summary.WriteString(fmt.Sprintf("Would merge: %s → %s\n", incoming.FormattedName, existing.FormattedName))
				updated++
			} else {
				summary.WriteString(fmt.Sprintf("Would create: %s\n", incoming.FormattedName))
				created++
			}
			continue
		}

		if existing != nil {
			// Merge: fill empty scalar fields only.
			t.mergeContact(existing, incoming)
			if _, err := t.store.Upsert(existing); err != nil {
				skipped++
				continue
			}
			// Add properties additively.
			for _, p := range props {
				_ = t.store.AddProperty(existing.ID, &p)
			}
			t.generateEmbedding(existing)
			updated++
		} else {
			// Create new contact.
			if incoming.FormattedName == "" {
				skipped++
				continue
			}
			c, err := t.store.Upsert(incoming)
			if err != nil {
				skipped++
				continue
			}
			for _, p := range props {
				_ = t.store.AddProperty(c.ID, &p)
			}
			t.generateEmbedding(c)
			created++
		}
	}

	if args.DryRun {
		return fmt.Sprintf("Dry run — %d would be created, %d would be merged:\n\n%s",
			created, updated, summary.String()), nil
	}

	return fmt.Sprintf("Imported %d contacts: %d created, %d merged, %d skipped",
		created+updated, created, updated, skipped), nil
}

// findExistingForMerge looks for an existing contact that matches the
// incoming contact. It first tries EMAIL matching, then falls back to
// formatted name.
func (t *Tools) findExistingForMerge(incoming *Contact, props []Property) *Contact {
	// Try EMAIL match first (exact, case-insensitive).
	for _, p := range props {
		if p.Property == "EMAIL" && p.Value != "" {
			matches, err := t.store.FindByPropertyExact("EMAIL", p.Value)
			if err == nil && len(matches) == 1 {
				full, err := t.store.GetWithProperties(matches[0].ID)
				if err == nil {
					return full
				}
			}
		}
	}

	// Fall back to name match.
	if incoming.FormattedName != "" {
		existing, err := t.store.FindByName(incoming.FormattedName)
		if err == nil && existing != nil {
			return existing
		}
	}

	return nil
}

// mergeContact fills empty scalar fields on existing from incoming.
// TrustZone and AISummary are never overwritten.
func (t *Tools) mergeContact(existing, incoming *Contact) {
	if existing.Kind == "" && incoming.Kind != "" {
		existing.Kind = incoming.Kind
	}
	// Never overwrite TrustZone.
	// Never overwrite AISummary.
	if existing.GivenName == "" && incoming.GivenName != "" {
		existing.GivenName = incoming.GivenName
	}
	if existing.FamilyName == "" && incoming.FamilyName != "" {
		existing.FamilyName = incoming.FamilyName
	}
	if existing.AdditionalNames == "" && incoming.AdditionalNames != "" {
		existing.AdditionalNames = incoming.AdditionalNames
	}
	if existing.NamePrefix == "" && incoming.NamePrefix != "" {
		existing.NamePrefix = incoming.NamePrefix
	}
	if existing.NameSuffix == "" && incoming.NameSuffix != "" {
		existing.NameSuffix = incoming.NameSuffix
	}
	if existing.Nickname == "" && incoming.Nickname != "" {
		existing.Nickname = incoming.Nickname
	}
	if existing.Birthday == "" && incoming.Birthday != "" {
		existing.Birthday = incoming.Birthday
	}
	if existing.Anniversary == "" && incoming.Anniversary != "" {
		existing.Anniversary = incoming.Anniversary
	}
	if existing.Gender == "" && incoming.Gender != "" {
		existing.Gender = incoming.Gender
	}
	if existing.Org == "" && incoming.Org != "" {
		existing.Org = incoming.Org
	}
	if existing.Title == "" && incoming.Title != "" {
		existing.Title = incoming.Title
	}
	if existing.Role == "" && incoming.Role != "" {
		existing.Role = incoming.Role
	}
	if existing.Note == "" && incoming.Note != "" {
		existing.Note = incoming.Note
	}
	if existing.PhotoURI == "" && incoming.PhotoURI != "" {
		existing.PhotoURI = incoming.PhotoURI
	}
}

// ExportVCFQRArgs are arguments for the contact_export_vcf_qr tool.
type ExportVCFQRArgs struct {
	Name               string `json:"name"`
	RecipientTrustZone string `json:"recipient_trust_zone,omitempty"`
}

// ExportVCFQR generates a QR code PNG containing a vCard for the named
// contact. Returns the path to the generated PNG file. The vCard text
// must fit within QR code capacity (~4KB).
func (t *Tools) ExportVCFQR(argsJSON string) (string, error) {
	var args ExportVCFQRArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	// Generate the vCard text via ExportVCF in text mode.
	exportArgs := ExportVCFArgs{
		Name:               args.Name,
		RecipientTrustZone: args.RecipientTrustZone,
		Format:             "text",
	}
	exportJSON, err := json.Marshal(exportArgs)
	if err != nil {
		return "", fmt.Errorf("marshal export args: %w", err)
	}

	text, err := t.ExportVCF(string(exportJSON))
	if err != nil {
		return "", err
	}

	// Check QR capacity. QR version 40 at Medium error correction
	// holds ~2331 bytes of binary data, matching generateQRCode's
	// use of qrcode.Medium.
	const maxQRBytes = 2331
	if len(text) > maxQRBytes {
		return "", fmt.Errorf("vCard too large for QR code (%d bytes, max %d). "+
			"Use recipient_trust_zone to reduce fields", len(text), maxQRBytes)
	}

	png, err := generateQRCode(text)
	if err != nil {
		return "", err
	}

	f, err := os.CreateTemp("", "thane-vcf-qr-*.png")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(png); err != nil {
		return "", fmt.Errorf("write qr png: %w", err)
	}

	return fmt.Sprintf("QR code vCard written to %s", f.Name()), nil
}

// formatContact formats a single contact with properties and facts for display.
func formatContact(c *Contact) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**%s**", c.FormattedName))
	if c.Org != "" {
		sb.WriteString(fmt.Sprintf(" (%s)", c.Org))
	}
	if c.AISummary != "" {
		sb.WriteString(fmt.Sprintf(" — %s", c.AISummary))
	}
	sb.WriteString(fmt.Sprintf("\nKind: %s", c.Kind))
	if c.TrustZone != "" {
		sb.WriteString(fmt.Sprintf(" | Trust: %s", c.TrustZone))
	}
	if c.Nickname != "" {
		sb.WriteString(fmt.Sprintf(" | Nickname: %s", c.Nickname))
	}
	if c.Title != "" {
		sb.WriteString(fmt.Sprintf("\nTitle: %s", c.Title))
	}

	if c.Note != "" {
		sb.WriteString(fmt.Sprintf("\nNote: %s", c.Note))
	}

	if len(c.Properties) > 0 {
		sb.WriteString("\n")
		for _, p := range c.Properties {
			label := p.Property
			if p.Type != "" {
				label += " (" + p.Type + ")"
			}
			if p.Label != "" {
				label += " [" + p.Label + "]"
			}
			sb.WriteString(fmt.Sprintf("  %s: %s\n", label, p.Value))
		}
	}

	return sb.String()
}

func (t *Tools) formatContact(c *Contact) string {
	formatted := formatContact(c)
	if t == nil || !t.dossiersEnabled || c == nil || c.ID == uuid.Nil {
		return formatted
	}
	if t.DossierReadsEnabled() {
		return fmt.Sprintf("%s\nContact ID: %s\nDossier access: contact_dossier_read(contact_id=%q)", formatted, c.ID, c.ID.String())
	}
	trailhead := "may be absent; probe once with doc_read"
	if t.DossierWritesEnabled() {
		trailhead += "; create or replace with contact_dossier_write"
	}
	return fmt.Sprintf("%s\nContact ID: %s\nDossier target: %s (%s)", formatted, c.ID, DossierRef(c.ID), trailhead)
}

func (t *Tools) formatOwnerActivitySummary() string {
	if t == nil || t.ownerActivity == nil {
		return ""
	}
	channels := t.ownerActivity()
	if len(channels) == 0 {
		return ""
	}

	sort.Slice(channels, func(i, j int) bool {
		return channels[i].LastActive.After(channels[j].LastActive)
	})

	type activityView struct {
		Channel        string `json:"channel"`
		LoopID         string `json:"loop_id,omitempty"`
		LoopName       string `json:"loop_name,omitempty"`
		ConversationID string `json:"conversation_id,omitempty"`
		ContactName    string `json:"contact_name,omitempty"`
		State          string `json:"state,omitempty"`
		LastActive     string `json:"last_active_delta,omitempty"`
	}
	payload := struct {
		ActiveOwnerChannels []activityView `json:"active_owner_channels"`
		ByChannel           map[string]int `json:"by_channel,omitempty"`
		Total               int            `json:"total"`
		Displayed           int            `json:"displayed,omitempty"`
		Omitted             int            `json:"omitted,omitempty"`
		MostRecentActive    string         `json:"most_recent_active_delta,omitempty"`
	}{
		ActiveOwnerChannels: make([]activityView, 0, min(len(channels), ownerActivitySummaryLimit)),
		ByChannel:           make(map[string]int),
		Total:               len(channels),
	}
	for _, ch := range channels {
		payload.ByChannel[ch.Channel]++
	}

	visible := channels
	if len(visible) > ownerActivitySummaryLimit {
		payload.Omitted = len(visible) - ownerActivitySummaryLimit
		visible = visible[:ownerActivitySummaryLimit]
	}
	payload.Displayed = len(visible)

	now := time.Now()
	for _, ch := range visible {
		view := activityView{
			Channel:        ch.Channel,
			LoopID:         ch.LoopID,
			LoopName:       ch.LoopName,
			ConversationID: ch.ConversationID,
			ContactName:    ch.ContactName,
			State:          ch.State,
		}
		if !ch.LastActive.IsZero() {
			view.LastActive = promptfmt.FormatDeltaOnly(ch.LastActive, now)
			if payload.MostRecentActive == "" {
				payload.MostRecentActive = view.LastActive
			}
		}
		payload.ActiveOwnerChannels = append(payload.ActiveOwnerChannels, view)
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ""
	}
	return "Active owner channels:\n```json\n" + string(data) + "\n```"
}

// formatContactList formats multiple contacts for display.
func formatContactList(contacts []*Contact) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d contact(s):\n\n", len(contacts)))
	for _, c := range contacts {
		sb.WriteString(fmt.Sprintf("**%s**", c.FormattedName))
		if c.Org != "" {
			sb.WriteString(fmt.Sprintf(" (%s)", c.Org))
		}
		if c.AISummary != "" {
			sb.WriteString(fmt.Sprintf(" — %s", c.AISummary))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
