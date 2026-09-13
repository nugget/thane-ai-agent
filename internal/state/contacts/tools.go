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
	// legacyOperatorID is the record the legacy owner name resolved to
	// when the app pinned it; legacyOperatorPinned says it was pinned,
	// since uuid.Nil is a valid pinned answer.
	legacyOperatorID     uuid.UUID
	legacyOperatorPinned bool
	ownerActivity        func() []OwnerChannelActivity
	dossiersEnabled      bool
	dossiersWritable     bool
	dossierRead          func(context.Context, documents.RefArgs) (string, error)
	dossierWrite         func(context.Context, documents.FacetedWriteArgs) (string, error)
	mutationSink         func(context.Context, ContactMutation) error
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

// ContactRefreshesEnabled reports whether committed model-authored changes
// have a configured downstream dossier-refresh consumer.
func (t *Tools) ContactRefreshesEnabled() bool {
	return t != nil && t.mutationSink != nil
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

// ConfigureLegacyOperatorContactID pins, for identity custody, the record
// the legacy owner name resolved to (uuid.Nil when it resolved to none).
// The app passes the channel resolver's cached answer, so custody and
// IsOwner agree on the operator for the life of the process even if a
// later record comes to match the name. Unpinned, custody resolves the
// name on every check.
func (t *Tools) ConfigureLegacyOperatorContactID(id uuid.UUID) {
	t.legacyOperatorID = id
	t.legacyOperatorPinned = true
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

// reservedKeyProperty is the vCard KEY property (RFC 6350 §6.8.1) and
// the X-THANE-KEY-* family: public keys and certificates that
// authenticate a contact's messages. Like trust zones, they are
// operator custody. A model tool that could write them would let a
// message that says "here is my key" install the key that verifies
// its own sender, so contact_save refuses them by name.
const reservedKeyProperty = "KEY"

// reservedKeyPrefix is the Thane-specific key family reserved alongside
// KEY.
const reservedKeyPrefix = "X-THANE-KEY-"

// withoutReservedKeys drops key-custody properties from a decoded vCard
// and reports how many it dropped.
func withoutReservedKeys(props []Property) ([]Property, int) {
	kept := make([]Property, 0, len(props))
	for _, p := range props {
		if isReservedKeyProperty(p.Property) {
			continue
		}
		kept = append(kept, p)
	}
	return kept, len(props) - len(kept)
}

// isReservedKeyProperty reports whether a fact key, as the model wrote
// it, would land on a key-custody property.
func isReservedKeyProperty(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	return upper == reservedKeyProperty || strings.HasPrefix(upper, reservedKeyPrefix)
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
//
// Identity custody applies as it does to an unattended model turn: no
// address or number is added to a contact above known or to the
// operator's own contact, nor when a contact with authority already
// holds it.
func (t *Tools) SaveContact(argsJSON string) (string, error) {
	return t.saveContact(context.Background(), argsJSON, nil, false, false)
}

// SaveContactFromModel applies contact_save with the current model turn's
// provenance and emits the configured post-commit mutation signal.
//
// operatorAttended reports that the turn is the operator's own message,
// as tools.OperatorAttended decides it. It lifts only the rule that keeps
// addresses and numbers off contacts above known and off the operator's
// own contact; the rule against a second holder of a value a contact with
// authority already holds still applies.
func (t *Tools) SaveContactFromModel(ctx context.Context, argsJSON string, provenance *PropertyProvenance, operatorAttended bool) (string, error) {
	return t.saveContact(ctx, argsJSON, provenance, true, operatorAttended)
}

func (t *Tools) saveContact(
	ctx context.Context,
	argsJSON string,
	provenance *PropertyProvenance,
	notify bool,
	operatorAttended bool,
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
	// Keys are custody in the same sense: the key that verifies a
	// contact's messages must not be installable by a message, and
	// neither may Thane's own X-THANE-* headers. Every fact key becomes
	// a vCard property name on the operator's next CardDAV read, so it
	// must also be a plain name that cannot decode as another property.
	if err := factRefusals(args.Facts); err != nil {
		return "", err
	}
	if err := argumentRefusals(args); err != nil {
		return "", err
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

	if err := t.ownerNameClaimRefusal(args, contact, created); err != nil {
		return "", err
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
		values := cleanReplacementValues(property, provided)
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

	guard := identityGuard{snapshotZone: contact.TrustZone, liftTargetCustody: operatorAttended}
	if hasIdentityProperty(additions) {
		guard.operatorID, err = t.custodyOperatorID()
		if err != nil {
			return "", err
		}
	}
	saved, changed, err := t.store.applyContactSave(contact, contactChanged, additions, replacements, guard)
	if err != nil {
		var custody *IdentityCustodyError
		if errors.As(err, &custody) {
			for i := range custody.Violations {
				custody.Violations[i].Key = factKeyFor(args.Facts, custody.Violations[i].Property, custody.Violations[i].Value)
			}
			zone := contact.TrustZone
			if zone == "" {
				zone = ZoneKnown
			}
			logIdentityRefusal("contact_save", contact.ID, zone, custody.Violations, provenance, false)
			return "", identityRefusal(contact.FormattedName, contact.TrustZone, custody.Violations)
		}
		if errors.Is(err, errContactChangedConcurrently) {
			return "", fmt.Errorf("%s changed while this contact_save was in flight: the operator reassigned its trust zone or deleted it. Nothing was saved. Re-read it with contact_lookup and retry", contact.FormattedName)
		}
		if created {
			return "", fmt.Errorf("create contact: %w", err)
		}
		return "", fmt.Errorf("update contact: %w", err)
	}
	if !changed {
		return fmt.Sprintf("Contact unchanged: **%s** (%s); no dossier refresh was queued", saved.FormattedName, saved.Kind), nil
	}

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
	// The durable refresh is part of the authoritative-write contract;
	// optional embedding maintenance must never delay or prevent it.
	t.generateEmbedding(saved)

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
		property := factProperty(key, facts[key])
		if hasProperty(existing, property.Property, property.Value) {
			continue
		}
		property.Provenance = provenance
		properties = append(properties, property)
	}
	return properties
}

// factProperty maps one fact to the property row it is stored as.
// Identity keys (email, phone, signal and matrix, or EMAIL, TEL and IMPP
// in any case) land on their canonical vCard property, and the signal
// and matrix aliases prefix the value with their URI scheme unless it
// already carries it. Every other key is stored as written.
func factProperty(key, value string) Property {
	property, identity := identityPropertyFor(key)
	if !identity {
		return Property{Property: key, Value: value}
	}
	if scheme := imppSchemeFor(key); scheme != "" && !strings.HasPrefix(strings.ToLower(value), scheme+":") {
		value = scheme + ":" + value
	}
	return Property{Property: property, Value: value}
}

// factKeyFor returns the fact key that produced a property row, so a
// refusal names the key the model wrote.
func factKeyFor(facts map[string]string, property, value string) string {
	keys := make([]string, 0, len(facts))
	for key := range facts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		p := factProperty(key, facts[key])
		if p.Property == property && strings.EqualFold(p.Value, value) {
			return key
		}
	}
	return property
}

func hasProperty(properties []Property, property, value string) bool {
	for _, candidate := range properties {
		if candidate.Property == property && propertyValueEqual(property, candidate.Value, value) {
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
	if len(got) != len(want) {
		return false
	}
	matched := make([]bool, len(want))
	for _, actual := range got {
		found := false
		for i, expected := range want {
			if !matched[i] && propertyValueEqual(property, actual, expected) {
				matched[i] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func cleanReplacementValues(property string, values []string) []string {
	cleaned := cleanOriginValues(values)
	if len(cleaned) < 2 {
		return cleaned
	}
	deduplicated := make([]string, 0, len(cleaned))
	for _, value := range cleaned {
		duplicate := false
		for _, existing := range deduplicated {
			if propertyValueEqual(property, existing, value) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			deduplicated = append(deduplicated, value)
		}
	}
	return deduplicated
}

// propertyValueEqual is the single equality contract for model-authored
// properties, whether they arrive through additive facts or replace-style
// origin fields. Existing additive contact facts are case-insensitive;
// document refs remain byte-exact because case can select a different path on
// a case-sensitive root.
func propertyValueEqual(property, left, right string) bool {
	if property == PropertyOriginContextRef {
		return left == right
	}
	return strings.EqualFold(left, right)
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

// ForgetContact soft-deletes one known contact, resolved by name once
// with the same cascade contact_lookup uses, and names the record it
// removed. Contacts above known, the operator's own contact and
// contacts bound to a Home Assistant person are operator custody and
// are refused, because forgetting one turns that person's email and
// Signal traffic into a stranger's. No turn lifts this, not even the
// operator's own.
func (t *Tools) ForgetContact(argsJSON string) (string, error) {
	return t.ForgetContactFromModel(context.Background(), argsJSON, nil)
}

// ForgetContactFromModel applies contact_forget for the current model
// turn, with the rules of [Tools.ForgetContact]. The provenance keys a
// custody refusal's log line to the turn that asked for the removal.
func (t *Tools) ForgetContactFromModel(_ context.Context, argsJSON string, provenance *PropertyProvenance) (string, error) {
	var args ForgetContactArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	if args.Name == "" {
		return "", fmt.Errorf("name is required")
	}

	c, err := t.store.ResolveContact(args.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("no contact matches %q; nothing was removed. Check the name with contact_lookup", args.Name)
	}
	if err != nil {
		return "", fmt.Errorf("resolve contact: %w; nothing was removed", err)
	}

	reasons, err := t.forgetCustodyReasons(c)
	if err != nil {
		return "", err
	}
	if len(reasons) > 0 {
		rules := make([]string, 0, len(reasons))
		texts := make([]string, 0, len(reasons))
		for _, reason := range reasons {
			rules = append(rules, reason.rule)
			texts = append(texts, reason.text)
		}
		if provenance == nil {
			provenance = &PropertyProvenance{}
		}
		slog.Warn("contact identity custody refused",
			"tool", "contact_forget",
			"contact_id", c.ID.String(),
			"zone", c.TrustZone,
			"rule", strings.Join(rules, ","),
			"request_id", provenance.RequestID,
			"conversation_id", provenance.ConversationID,
			"loop_id", provenance.LoopID)
		return "", fmt.Errorf("contact_forget refused %s (%s, %s): %s. Contacts above known, the operator's own contact and contacts bound to a Home Assistant person are operator-custodied, because forgetting one turns that person's email and Signal traffic into a stranger's. Nothing was removed. %s",
			c.FormattedName, c.TrustZone, c.ID, strings.Join(texts, "; "), forgetRecovery(reasons, c.ID))
	}

	deleted, err := t.store.deleteIfUncustodied(c.ID)
	if err != nil {
		return "", err
	}
	if !deleted {
		return "", fmt.Errorf("%s (%s) changed while this contact_forget was in flight: the operator reassigned its trust zone, bound it to a Home Assistant person, or deleted it. Nothing was removed. Re-read it with contact_lookup", c.FormattedName, c.ID)
	}
	return fmt.Sprintf("Forgot contact: %s (%s, %s)", c.FormattedName, c.TrustZone, c.ID), nil
}

// forgetRecovery is the operator's recovery for a refused forget. Only
// deletion clears the operator's own contact; a zone above known clears
// by demotion, and a Home Assistant person binding by removing it.
func forgetRecovery(reasons []forgetCustodyReason, id uuid.UUID) string {
	var operator, zone, bound bool
	for _, reason := range reasons {
		switch reason.rule {
		case IdentityReasonOperator:
			operator = true
		case IdentityReasonZone:
			zone = true
		case forgetReasonHAPerson:
			bound = true
		}
	}
	remedy := ""
	switch {
	case operator:
	case zone && bound:
		remedy = ", or demote it to known and remove its Home Assistant person binding"
	case zone:
		remedy = ", or demote it to known"
	case bound:
		remedy = ", or remove its Home Assistant person binding"
	}
	return fmt.Sprintf("Ask the operator to delete it through CardDAV or DELETE /v1/contacts/%s%s", id, remedy)
}

// forgetReasonHAPerson is the forget rule for a contact bound to a Home
// Assistant person.
const forgetReasonHAPerson = "ha_person"

// forgetCustodyReason is one reason contact_forget refuses a record: a
// rule name for the log and a clause for the model.
type forgetCustodyReason struct {
	rule string
	text string
}

// forgetCustodyReasons lists every reason the record is operator custody
// for removal, or none when contact_forget may remove it.
func (t *Tools) forgetCustodyReasons(c *Contact) ([]forgetCustodyReason, error) {
	var reasons []forgetCustodyReason
	if c.TrustZone != ZoneKnown {
		reasons = append(reasons, forgetCustodyReason{rule: IdentityReasonZone, text: fmt.Sprintf("it is %s", c.TrustZone)})
	}
	operatorID, err := t.custodyOperatorID()
	if err != nil {
		return nil, err
	}
	if operatorID != uuid.Nil && c.ID == operatorID {
		reasons = append(reasons, forgetCustodyReason{rule: IdentityReasonOperator, text: "it is the operator's own contact"})
	}
	entity, _, err := t.store.HAPersonEntity(c.ID)
	if err != nil {
		return nil, fmt.Errorf("check identity custody: %w", err)
	}
	if entity != "" {
		reasons = append(reasons, forgetCustodyReason{rule: forgetReasonHAPerson, text: fmt.Sprintf("it is bound to Home Assistant person %s", entity)})
	}
	return reasons, nil
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

// importVCFSource is the provenance source stamped on every property a
// vCard import writes.
const importVCFSource = "contact_import_vcf"

// ImportVCF imports contacts from a vCard file or text. When merge is
// true (default), existing contacts are matched by EMAIL then by name,
// and only empty fields are filled. TrustZone and AISummary are never
// overwritten during merge. Properties are additive and carry Source
// contact_import_vcf. Identity custody applies exactly as it does to
// [Tools.ImportVCFFromModel].
func (t *Tools) ImportVCF(argsJSON string) (string, error) {
	return t.importVCF(argsJSON, &PropertyProvenance{Source: importVCFSource})
}

// ImportVCFFromModel applies contact_import_vcf with the current model
// turn's provenance, stamped on every imported property and on the
// custody log. A vCard is content, not the operator's word, so no turn
// lifts identity custody here: addresses and numbers are dropped from a
// merge into a contact above known or the operator's own, and wherever
// an admin, household, trusted or operator contact already holds them.
// The rest of the card still imports and the result counts the drops.
func (t *Tools) ImportVCFFromModel(_ context.Context, argsJSON string, provenance *PropertyProvenance) (string, error) {
	return t.importVCF(argsJSON, provenance)
}

func (t *Tools) importVCF(argsJSON string, provenance *PropertyProvenance) (string, error) {
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
	var drops importDrops
	var summary strings.Builder
	operator := t.lazyCustodyOperatorID()

	for i, incoming := range decoded {
		// Key properties are operator custody, like trust zones: a vCard
		// a message carried must not install the key that would verify
		// that message's sender. The CardDAV backend, the operator's
		// authenticated surface, decodes vCards on its own path.
		props, dropped := withoutReservedKeys(allProps[i])
		drops.keys += dropped
		// A name carrying vCard syntax (a nested group decodes as
		// "B.EMAIL") would become a different property on the next
		// CardDAV round trip.
		props, dropped = withoutMalformedNames(props)
		drops.names += dropped
		// go-vcard escapes only LF on output, so a decoded CR or other
		// control character in a value would split the line in the
		// operator's contacts client on the next CardDAV read.
		props, dropped = withoutUnescapedControls(incoming, props)
		drops.values += dropped

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

		// Under the legacy owner-name selector a record that carries
		// the owner name as its name or nickname could become the
		// operator the next time the name resolves.
		skip, err := t.withoutOwnerNameClaim(existing, incoming, operator)
		if err != nil {
			return "", err
		}
		if skip {
			drops.ownerName++
			continue
		}

		props, refused, err := t.withoutCustodiedIdentity(existing, props, operator)
		if err != nil {
			return "", err
		}
		if len(refused) > 0 {
			drops.identity += len(refused)
			target, zone := uuid.Nil, ZoneKnown
			if existing != nil {
				target, zone = existing.ID, existing.TrustZone
			}
			logIdentityRefusal("contact_import_vcf", target, zone, refused, provenance, args.DryRun)
		}
		for j := range props {
			props[j].Provenance = provenance
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
			drops.writeFailures += t.addImportedProperties(existing.ID, props)
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
			drops.writeFailures += t.addImportedProperties(c.ID, props)
			t.generateEmbedding(c)
			created++
		}
	}

	notes := drops.notes()
	if args.DryRun {
		return fmt.Sprintf("Dry run — %d would be created, %d would be merged:%s\n\n%s",
			created, updated, notes, summary.String()), nil
	}

	return fmt.Sprintf("Imported %d contacts: %d created, %d merged, %d skipped.%s",
		created+updated, created, updated, skipped, notes), nil
}

// importDrops counts what one vCard import left out, by reason.
type importDrops struct {
	keys          int
	names         int
	values        int
	ownerName     int
	identity      int
	writeFailures int
}

// notes renders the drop counts as the sentences an import result ends
// with, each in the shape of the key note.
func (d importDrops) notes() string {
	var b strings.Builder
	if d.keys > 0 {
		fmt.Fprintf(&b, " %d key propert(ies) were not imported: KEY and X-THANE-KEY-* are operator custody and never come in through a model tool.", d.keys)
	}
	if d.names > 0 {
		fmt.Fprintf(&b, " %d propert(ies) were not imported: their names are not plain vCard property names (a nested group such as a.b.EMAIL, a space, or more than 64 characters) and would become a different property on the next CardDAV round trip.", d.names)
	}
	if d.values > 0 {
		fmt.Fprintf(&b, " %d value(s) were not imported: they carry a carriage return or other control character, which a contacts client would read as the start of another property.", d.values)
	}
	if d.ownerName > 0 {
		fmt.Fprintf(&b, " %d name(s) were not imported: a card that would create a contact, or give an existing one a nickname, under the name Thane recognizes the operator by was left out, because that contact could take the operator's identity; ask the operator to add it through CardDAV or the contacts API.", d.ownerName)
	}
	if d.identity > 0 {
		fmt.Fprintf(&b, " %d address(es)/number(s) were not imported: EMAIL, TEL and IMPP values are operator custody on an admin, household, trusted or operator contact, and a value one of those already holds keeps its single holder; ask the operator to add them through CardDAV or the contacts API.", d.identity)
	}
	if d.writeFailures > 0 {
		fmt.Fprintf(&b, " %d propert(ies) failed to write and were not imported; the log names each one.", d.writeFailures)
	}
	return b.String()
}

// withoutMalformedNames drops decoded properties whose names fail the
// fact-key grammar and reports how many it dropped.
func withoutMalformedNames(props []Property) ([]Property, int) {
	kept := make([]Property, 0, len(props))
	for _, p := range props {
		if validFactKey.MatchString(p.Property) {
			kept = append(kept, p)
		}
	}
	return kept, len(props) - len(kept)
}

// withoutUnescapedControls clears scalar fields and drops properties
// whose values carry a control character the vCard encoder emits raw,
// and reports how many values it left out.
func withoutUnescapedControls(c *Contact, props []Property) ([]Property, int) {
	dropped := 0
	for _, field := range []*string{
		&c.FormattedName, &c.Kind, &c.GivenName, &c.FamilyName, &c.AdditionalNames,
		&c.NamePrefix, &c.NameSuffix, &c.Nickname, &c.Birthday, &c.Anniversary,
		&c.Gender, &c.Org, &c.Title, &c.Role, &c.Note, &c.PhotoURI, &c.AISummary,
	} {
		if hasUnescapedControl(*field) {
			*field = ""
			dropped++
		}
	}
	kept := make([]Property, 0, len(props))
	for _, p := range props {
		if hasUnescapedControl(p.Value) || hasUnescapedControl(p.Type) || hasUnescapedControl(p.Label) || hasUnescapedControl(p.MediaType) {
			dropped++
			continue
		}
		kept = append(kept, p)
	}
	return kept, dropped
}

// withoutOwnerNameClaim keeps a vCard import from giving the legacy
// owner name to a record other than the operator's own. A card that
// would create such a record is skipped (true); a merge that would fill
// an existing record's empty nickname with it leaves the nickname out.
func (t *Tools) withoutOwnerNameClaim(existing, incoming *Contact, operator func() (uuid.UUID, error)) (bool, error) {
	owner := t.legacyOwnerName()
	if owner == "" {
		return false, nil
	}
	if existing == nil {
		return claimsOwnerName(owner, incoming.FormattedName, incoming.Nickname), nil
	}
	if existing.Nickname != "" || !claimsOwnerName(owner, incoming.Nickname) {
		return false, nil
	}
	operatorID, err := operator()
	if err != nil {
		return false, err
	}
	if operatorID == uuid.Nil || existing.ID != operatorID {
		incoming.Nickname = ""
	}
	return false, nil
}

// lazyCustodyOperatorID resolves the operator's record at most once per
// import, and only when a card carries identity.
func (t *Tools) lazyCustodyOperatorID() func() (uuid.UUID, error) {
	var (
		resolved bool
		id       uuid.UUID
		err      error
	)
	return func() (uuid.UUID, error) {
		if !resolved {
			id, err = t.custodyOperatorID()
			resolved = true
		}
		return id, err
	}
}

// withoutCustodiedIdentity drops the identity properties a model-facing
// import may not write to its target (existing, or a record about to be
// created when nil) and returns the kept properties with the refused
// values.
func (t *Tools) withoutCustodiedIdentity(existing *Contact, props []Property, operator func() (uuid.UUID, error)) ([]Property, []IdentityViolation, error) {
	if !hasIdentityProperty(props) {
		return props, nil, nil
	}
	operatorID, err := operator()
	if err != nil {
		return nil, nil, err
	}
	target, guard := uuid.Nil, identityGuard{operatorID: operatorID, snapshotZone: ZoneKnown}
	if existing != nil {
		target, guard.snapshotZone = existing.ID, existing.TrustZone
	}
	violations, err := identityViolations(t.store.db.Query, target, guard, props)
	if err != nil {
		return nil, nil, fmt.Errorf("check identity custody: %w", err)
	}
	if len(violations) == 0 {
		return props, nil, nil
	}
	kept := make([]Property, 0, len(props))
	for _, p := range props {
		if !refusedIdentity(p, violations) {
			kept = append(kept, p)
		}
	}
	return kept, violations, nil
}

// refusedIdentity reports whether p is one of the refused values.
func refusedIdentity(p Property, violations []IdentityViolation) bool {
	for _, v := range violations {
		if strings.EqualFold(strings.TrimSpace(p.Property), v.Property) && p.Value == v.Value {
			return true
		}
	}
	return false
}

// addImportedProperties writes imported properties one at a time and
// returns how many failed, logging each failure rather than dropping it
// silently.
func (t *Tools) addImportedProperties(contactID uuid.UUID, props []Property) int {
	failures := 0
	for _, p := range props {
		if err := t.store.AddProperty(contactID, &p); err != nil {
			failures++
			slog.Warn("contact_import_vcf property write failed",
				"contact_id", contactID.String(),
				"property", p.Property,
				"error", err)
		}
	}
	return failures
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
