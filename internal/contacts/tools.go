package contacts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
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
	Name         string            `json:"name"`
	Kind         string            `json:"kind,omitempty"`         // person, company, organization
	Relationship string            `json:"relationship,omitempty"` // friend, colleague, family, vendor
	Summary      string            `json:"summary,omitempty"`
	Details      string            `json:"details,omitempty"`
	Facts        map[string]string `json:"facts,omitempty"` // key-value attributes (email, phone, etc.)
}

// SaveContact creates or updates a contact. When a contact with the
// given name already exists, only non-empty fields are overwritten.
// Facts are additive (use ReplaceFact for replacement semantics).
func (t *Tools) SaveContact(argsJSON string) (string, error) {
	var args SaveContactArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
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
		// Update existing contact.
		if args.Kind != "" {
			existing.Kind = args.Kind
		}
		if args.Relationship != "" {
			existing.Relationship = args.Relationship
		}
		if args.Summary != "" {
			existing.Summary = args.Summary
		}
		if args.Details != "" {
			existing.Details = args.Details
		}

		updated, err := t.store.Upsert(existing)
		if err != nil {
			return "", fmt.Errorf("update contact: %w", err)
		}

		// Set any facts.
		for k, v := range args.Facts {
			if err := t.store.SetFact(updated.ID, k, v); err != nil {
				return "", fmt.Errorf("set fact %q: %w", k, err)
			}
		}

		t.generateEmbedding(updated, args.Facts)

		return fmt.Sprintf("Updated contact: **%s** (%s)", updated.Name, updated.Kind), nil
	}

	// Create new contact.
	c := &Contact{
		Name:         args.Name,
		Kind:         args.Kind,
		Relationship: args.Relationship,
		Summary:      args.Summary,
		Details:      args.Details,
	}

	created, err := t.store.Upsert(c)
	if err != nil {
		return "", fmt.Errorf("create contact: %w", err)
	}

	for k, v := range args.Facts {
		if err := t.store.SetFact(created.ID, k, v); err != nil {
			return "", fmt.Errorf("set fact %q: %w", k, err)
		}
	}

	t.generateEmbedding(created, args.Facts)

	return fmt.Sprintf("Saved new contact: **%s** (%s)", created.Name, created.Kind), nil
}

// LookupContactArgs are arguments for the lookup_contact tool.
type LookupContactArgs struct {
	Name  string `json:"name,omitempty"`
	Query string `json:"query,omitempty"`
	Kind  string `json:"kind,omitempty"`
	Key   string `json:"key,omitempty"`   // fact key filter
	Value string `json:"value,omitempty"` // fact value filter
}

// LookupContact retrieves contacts from the directory.
func (t *Tools) LookupContact(argsJSON string) (string, error) {
	var args LookupContactArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	// Name lookup.
	if args.Name != "" {
		c, err := t.store.FindByName(args.Name)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Sprintf("No contact found named %q", args.Name), nil
		}
		if err != nil {
			return "", fmt.Errorf("find contact: %w", err)
		}
		c, err = t.store.GetWithFacts(c.ID)
		if err != nil {
			return "", fmt.Errorf("get with facts: %w", err)
		}
		return formatContact(c), nil
	}

	// Fact filter.
	if args.Key != "" && args.Value != "" {
		contacts, err := t.store.FindByFact(args.Key, args.Value)
		if err != nil {
			return "", fmt.Errorf("find by fact: %w", err)
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
// Returns count of contacts embedded.
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
		facts, _ := t.store.GetFacts(c.ID)
		embText := buildEmbeddingText(c, facts)
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
func (t *Tools) generateEmbedding(c *Contact, extraFacts map[string]string) {
	if t.embeddings == nil {
		return
	}

	// Merge stored facts with any just-provided ones.
	facts, _ := t.store.GetFacts(c.ID)
	if facts == nil {
		facts = make(map[string][]string)
	}
	for k, v := range extraFacts {
		facts[k] = []string{v}
	}

	embText := buildEmbeddingText(c, facts)
	emb, err := t.embeddings.Generate(context.Background(), embText)
	if err != nil {
		return
	}
	_ = t.store.SetEmbedding(c.ID, emb)
}

// buildEmbeddingText creates text for embedding from a contact and its facts.
func buildEmbeddingText(c *Contact, facts map[string][]string) string {
	var sb strings.Builder
	sb.WriteString(c.Name)
	if c.Kind != "" {
		sb.WriteString(" (" + c.Kind + ")")
	}
	if c.Relationship != "" {
		sb.WriteString(" - " + c.Relationship)
	}
	if c.Summary != "" {
		sb.WriteString(": " + c.Summary)
	}
	if c.Details != "" {
		sb.WriteString("\n" + c.Details)
	}
	keys := make([]string, 0, len(facts))
	for k := range facts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sb.WriteString(fmt.Sprintf("\n%s: %s", k, strings.Join(facts[k], ", ")))
	}
	return sb.String()
}

// formatContact formats a single contact with facts for display.
func formatContact(c *Contact) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**%s**", c.Name))
	if c.Relationship != "" {
		sb.WriteString(fmt.Sprintf(" (%s)", c.Relationship))
	}
	if c.Summary != "" {
		sb.WriteString(fmt.Sprintf(" — %s", c.Summary))
	}
	sb.WriteString(fmt.Sprintf("\nKind: %s", c.Kind))

	if c.Details != "" {
		sb.WriteString(fmt.Sprintf("\nDetails: %s", c.Details))
	}

	if len(c.Facts) > 0 {
		sb.WriteString("\n")
		fkeys := make([]string, 0, len(c.Facts))
		for k := range c.Facts {
			fkeys = append(fkeys, k)
		}
		sort.Strings(fkeys)
		for _, k := range fkeys {
			sb.WriteString(fmt.Sprintf("  %s: %s\n", k, strings.Join(c.Facts[k], ", ")))
		}
	}

	return sb.String()
}

// formatContactList formats multiple contacts for display.
func formatContactList(contacts []*Contact) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d contact(s):\n\n", len(contacts)))
	for _, c := range contacts {
		sb.WriteString(fmt.Sprintf("**%s**", c.Name))
		if c.Relationship != "" {
			sb.WriteString(fmt.Sprintf(" (%s)", c.Relationship))
		}
		if c.Summary != "" {
			sb.WriteString(fmt.Sprintf(" — %s", c.Summary))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
