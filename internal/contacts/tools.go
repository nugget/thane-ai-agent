package contacts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// EmbeddingClient generates embeddings for semantic search.
type EmbeddingClient interface {
	Generate(ctx context.Context, text string) ([]float32, error)
}

// Tools provides contact-related tools for the agent.
type Tools struct {
	store      *Store
	embeddings EmbeddingClient
}

// NewTools creates contact tools using the given store.
func NewTools(store *Store) *Tools {
	return &Tools{store: store}
}

// SetEmbeddingClient sets the embedding client for semantic search.
func (t *Tools) SetEmbeddingClient(client EmbeddingClient) {
	t.embeddings = client
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
