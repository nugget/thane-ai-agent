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
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/google/uuid"
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
	store            *Store
	embeddings       EmbeddingClient
	selfContactName  string
	ownerContactName string
	ownerActivity    func() []OwnerChannelActivity
}

// NewTools creates contact tools using the given store.
func NewTools(store *Store) *Tools {
	return &Tools{store: store}
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

// SetOwnerContactName sets the contact name used to resolve the
// primary human owner/operator contact.
func (t *Tools) SetOwnerContactName(name string) {
	t.ownerContactName = name
}

// SetOwnerActivitySource configures a source of active owner-scoped
// channel activity for the owner_contact helper.
func (t *Tools) SetOwnerActivitySource(src func() []OwnerChannelActivity) {
	t.ownerActivity = src
}

// OwnerContact returns the configured owner contact, or falls back to
// the sole admin contact when no explicit owner contact name is set.
func (t *Tools) OwnerContact(_ string) (string, error) {
	c, err := t.resolveOwnerContact()
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString(formatContact(c))
	if summary := t.formatOwnerActivitySummary(); summary != "" {
		sb.WriteString("\n\n")
		sb.WriteString(summary)
	}
	return sb.String(), nil
}

func (t *Tools) resolveOwnerContact() (*Contact, error) {
	name := strings.TrimSpace(t.ownerContactName)
	if name != "" {
		c, err := t.store.ResolveContact(name)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("configured owner contact %q not found", name)
		}
		if err != nil {
			return nil, fmt.Errorf("resolve configured owner contact: %w", err)
		}
		full, err := t.store.GetWithProperties(c.ID)
		if err != nil {
			return nil, fmt.Errorf("get configured owner contact details: %w", err)
		}
		return full, nil
	}

	admins, err := t.store.FindByTrustZone(ZoneAdmin)
	if err != nil {
		return nil, fmt.Errorf("list admin contacts: %w", err)
	}
	switch len(admins) {
	case 0:
		return nil, fmt.Errorf("owner contact not configured: set identity.owner_contact_name or mark exactly one admin contact")
	case 1:
		full, err := t.store.GetWithProperties(admins[0].ID)
		if err != nil {
			return nil, fmt.Errorf("get owner contact details: %w", err)
		}
		return full, nil
	default:
		names := make([]string, 0, len(admins))
		for _, admin := range admins {
			names = append(names, admin.FormattedName)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("owner contact is ambiguous: multiple admin contacts found (%s); set identity.owner_contact_name", strings.Join(names, ", "))
	}
}

// SaveContactArgs are arguments for the save_contact tool.
type SaveContactArgs struct {
	Name       string            `json:"name"`                  // maps to FormattedName
	Kind       string            `json:"kind,omitempty"`        // individual, group, org, location
	TrustZone  string            `json:"trust_zone,omitempty"`  // admin, household, trusted, known
	GivenName  string            `json:"given_name,omitempty"`  // vCard N given name
	FamilyName string            `json:"family_name,omitempty"` // vCard N family name
	Nickname   string            `json:"nickname,omitempty"`    // vCard NICKNAME
	Org        string            `json:"org,omitempty"`         // vCard ORG
	Title      string            `json:"title,omitempty"`       // vCard TITLE
	Role       string            `json:"role,omitempty"`        // vCard ROLE
	Note       string            `json:"note,omitempty"`        // vCard NOTE
	AISummary  string            `json:"ai_summary,omitempty"`  // AI-generated context
	Facts      map[string]string `json:"facts,omitempty"`       // freeform AI metadata
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
	"note": true, "ai_summary": true, "facts": true,
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

	// Look for existing contact by name.
	existing, err := t.store.FindByName(args.Name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("find contact: %w", err)
	}

	if existing != nil {
		// Update existing contact — only non-empty fields overwrite.
		if args.Kind != "" {
			existing.Kind = args.Kind
		}
		if args.TrustZone != "" {
			existing.TrustZone = args.TrustZone
		}
		if args.GivenName != "" {
			existing.GivenName = args.GivenName
		}
		if args.FamilyName != "" {
			existing.FamilyName = args.FamilyName
		}
		if args.Nickname != "" {
			existing.Nickname = args.Nickname
		}
		if args.Org != "" {
			existing.Org = args.Org
		}
		if args.Title != "" {
			existing.Title = args.Title
		}
		if args.Role != "" {
			existing.Role = args.Role
		}
		if args.Note != "" {
			existing.Note = args.Note
		}
		if args.AISummary != "" {
			existing.AISummary = args.AISummary
		}

		updated, err := t.store.Upsert(existing)
		if err != nil {
			return "", fmt.Errorf("update contact: %w", err)
		}

		if err := t.saveProperties(updated.ID, args.Facts); err != nil {
			return "", err
		}

		t.generateEmbedding(updated)

		return fmt.Sprintf("Updated contact: **%s** (%s)", updated.FormattedName, updated.Kind), nil
	}

	// Create new contact.
	c := &Contact{
		FormattedName: args.Name,
		Kind:          args.Kind,
		TrustZone:     args.TrustZone,
		GivenName:     args.GivenName,
		FamilyName:    args.FamilyName,
		Nickname:      args.Nickname,
		Org:           args.Org,
		Title:         args.Title,
		Role:          args.Role,
		Note:          args.Note,
		AISummary:     args.AISummary,
	}

	created, err := t.store.Upsert(c)
	if err != nil {
		return "", fmt.Errorf("create contact: %w", err)
	}

	if err := t.saveProperties(created.ID, args.Facts); err != nil {
		return "", err
	}

	t.generateEmbedding(created)

	return fmt.Sprintf("Saved new contact: **%s** (%s)", created.FormattedName, created.Kind), nil
}

// saveProperties stores all fact entries as contact_properties. Known
// vCard keys (email, phone, signal, matrix) are mapped to their
// standard property names (EMAIL, TEL, IMPP); all others are stored
// with their original key as the property name.
func (t *Tools) saveProperties(contactID uuid.UUID, facts map[string]string) error {
	for k, v := range facts {
		propName, isVCard := propertyKeys[k]
		if !isVCard {
			propName = k
		}
		value := v
		// For IMPP properties, prefix with the protocol scheme if not
		// already present.  Use HasPrefix rather than Contains so that
		// Matrix IDs like @user:server.com still get the scheme prepended.
		if propName == "IMPP" && !strings.HasPrefix(v, k+":") {
			value = k + ":" + v
		}
		if err := t.store.AddProperty(contactID, &Property{
			Property: propName,
			Value:    value,
		}); err != nil {
			return fmt.Errorf("add property %s: %w", propName, err)
		}
	}
	return nil
}

// LookupContactArgs are arguments for the lookup_contact tool.
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
		return formatContact(c), nil
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

// ForgetContactArgs are arguments for the forget_contact tool.
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

// ListContactsArgs are arguments for the list_contacts tool.
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

// ExportVCFArgs are arguments for the export_vcf tool.
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

// ExportAllVCFArgs are arguments for the export_all_vcf tool.
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

// ImportVCFArgs are arguments for the import_vcf tool.
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

// ExportVCFQRArgs are arguments for the export_vcf_qr tool.
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
		LastActive     string `json:"last_active,omitempty"`
	}
	payload := struct {
		ActiveOwnerChannels []activityView `json:"active_owner_channels"`
		ByChannel           map[string]int `json:"by_channel,omitempty"`
		Total               int            `json:"total"`
		Displayed           int            `json:"displayed,omitempty"`
		Omitted             int            `json:"omitted,omitempty"`
		MostRecentActive    string         `json:"most_recent_active,omitempty"`
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
			view.LastActive = ch.LastActive.UTC().Format(time.RFC3339)
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
