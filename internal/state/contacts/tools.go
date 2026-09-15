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
	dossierExists        func(context.Context, string) (bool, error)
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
// primary human operator. The store learns it too, so a nickname the
// operator shares resolves to the operator on every path.
func (t *Tools) ConfigureOperatorContactID(id uuid.UUID) {
	t.operatorContactID = id
	t.pinStoreOperator()
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

// ConfigureLegacyOperatorContactID pins, for identity custody and
// contact_owner, the record the legacy owner name resolved to (uuid.Nil
// when it resolved to none). The app passes the channel resolver's
// cached answer, so custody, contact_owner and IsOwner agree on the
// operator for the life of the process even if a later record comes to
// match the name. Unpinned, both resolve the name on every call. A
// configured operator_contact_id still takes precedence, for custody
// and for the store's nickname ordering alike.
func (t *Tools) ConfigureLegacyOperatorContactID(id uuid.UUID) {
	t.legacyOperatorID = id
	t.legacyOperatorPinned = true
	t.pinStoreOperator()
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
	if name != "" && t.legacyOperatorPinned {
		// The pinned record is the one IsOwner and custody treat as the
		// operator, so contact_owner names it even after another record
		// comes to match the name, and names none when none was pinned.
		if t.legacyOperatorID == uuid.Nil {
			return nil, fmt.Errorf("configured legacy operator contact name %q not found", name)
		}
		full, err := t.store.GetWithProperties(t.legacyOperatorID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("configured legacy operator contact name %q not found", name)
		}
		if err != nil {
			return nil, fmt.Errorf("get configured operator contact details: %w", err)
		}
		return full, nil
	}
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

// SaveContact creates or updates a contact with the checks
// [Tools.SaveContactFromModel] applies in an unattended turn, with no
// turn provenance and no lift for the operator's own message, and emits
// no dossier refresh. The name and nickname are trimmed of edge space
// first. When a contact with the given name already exists, only
// non-empty fields are overwritten. Facts are additive. Email and phone
// values are stored as vCard properties (EMAIL, TEL) in
// contact_properties.
//
// Top-level string fields that don't match known SaveContactArgs keys
// (e.g., "email", "phone") are automatically rescued into the Facts
// map or contact_properties, since models frequently flatten them.
//
// The whole save is refused, and nothing is written, when it would:
//   - set trust_zone, or a KEY or X-THANE-* fact, on any contact;
//   - use a fact key that is not a plain name or that names a field the
//     record owns, or put a control character in any argument or fact
//     value (note and ai_summary keep plain line breaks);
//   - add an address or number (EMAIL, TEL or IMPP, under any alias or
//     case), or a notification routing fact (ha_companion_app or
//     notification_preference, in any case), to a contact above known
//     or to the operator's own contact;
//   - change the nickname of a contact above known or of the operator's
//     own contact, where a change in ASCII case alone is no change;
//   - add an address or number that a contact above known, or the
//     operator's own, already holds;
//   - give a new contact a formatted name, or any contact a nickname,
//     that an active contact above known or the operator's own already
//     goes by. This rule holds in every turn, the operator's own message
//     included;
//   - under the legacy owner-name selector, give a contact other than
//     the operator's own the owner name.
//
// Re-saving a value the contact already holds, a routing value under
// another spelling of its key included, is a no-op. Delivery uses only
// the first value of each routing fact, reading the lowercase key first.
// When a new routing value lands behind an existing one, the result
// names the value delivery still uses.
func (t *Tools) SaveContact(argsJSON string) (string, error) {
	return t.saveContact(context.Background(), argsJSON, nil, false, false)
}

// SaveContactFromModel applies contact_save with the current model turn's
// provenance and emits the configured post-commit mutation signal.
//
// operatorAttended reports that the turn is the operator's own message,
// as tools.OperatorAttended decides it. It lifts only the rule that keeps
// addresses, numbers, notification routing facts and nickname changes
// off contacts above known and off the operator's own contact; the rules
// against a second holder of a value, or a second contact answering to
// a name, that a contact with authority already has still apply.
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
	// Lookups compare a stored name byte for byte apart from ASCII case,
	// so edge space would hide a name from them. Trim the name and
	// nickname here, so the custody checks below judge the value stored.
	args.Name = strings.TrimSpace(args.Name)
	args.Nickname = strings.TrimSpace(args.Nickname)
	if args.Name == "" {
		return "", fmt.Errorf("name is required")
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

	if err := t.ownerNameClaimRefusal(ctx, args, contact, created); err != nil {
		return "", err
	}

	// Name claims and the nickname snapshot are read before the scalar
	// updates below change contact.
	claims := saveClaims(args, contact, created)
	snapshotNickname := contact.Nickname

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

	guard := identityGuard{
		snapshotZone:      contact.TrustZone,
		snapshotNickname:  snapshotNickname,
		liftTargetCustody: operatorAttended,
		claims:            claims,
	}
	if hasCustodiedProperty(additions) || len(claims) > 0 {
		guard.operatorID, err = t.custodyOperatorID(ctx)
		if err != nil {
			return "", err
		}
	}
	saved, outcome, err := t.store.applyContactSave(contact, contactChanged, additions, replacements, guard)
	if err != nil {
		var custody *IdentityCustodyError
		if errors.As(err, &custody) {
			labelViolationKeys(args.Facts, custody.Violations)
			zone := contact.TrustZone
			if zone == "" {
				zone = ZoneKnown
			}
			logIdentityRefusal("contact_save", contact.ID, zone, custody.Violations, provenance, false)
			return "", identityRefusal(contact.FormattedName, contact.TrustZone, created, custody.Violations)
		}
		if errors.Is(err, errContactChangedConcurrently) {
			return "", fmt.Errorf("%s changed while this contact_save was in flight: the operator reassigned its trust zone, changed its nickname, or deleted it. Nothing was saved. Re-read it with contact_lookup and retry", contact.FormattedName)
		}
		if created {
			return "", fmt.Errorf("create contact: %w", err)
		}
		return "", fmt.Errorf("update contact: %w", err)
	}
	if !outcome.changed {
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
	t.generateEmbedding(ctx, saved)

	// A routing fact recorded behind an existing value does not switch
	// delivery; say so, so the next turn cannot report a switch. The
	// note is judged from the rows the save's own transaction read, not
	// from the snapshot above, so a value another writer committed first
	// is the one it names.
	shadow := outcome.routingNote
	if created {
		return fmt.Sprintf("Saved new contact: **%s** (%s)", saved.FormattedName, saved.Kind) + shadow, nil
	}
	return fmt.Sprintf("Updated contact: **%s** (%s)", saved.FormattedName, saved.Kind) + shadow, nil
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

// labelViolationKeys sets each violation's Key to the fact key that
// produced its row, or to the name or nickname argument for a name
// claim, so a refusal names what the model wrote. Two keys can produce
// the same row (email and EMAIL with one value), so each key labels at
// most one violation.
func labelViolationKeys(facts map[string]string, violations []IdentityViolation) {
	keys := make([]string, 0, len(facts))
	for key := range facts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	used := make(map[string]bool, len(violations))
	for i := range violations {
		v := &violations[i]
		if isClaimProperty(v.Property) {
			v.Key = claimArgument(v.Property)
			continue
		}
		v.Key = v.Property
		for _, key := range keys {
			if used[key] {
				continue
			}
			if p := factProperty(key, facts[key]); strings.EqualFold(p.Property, v.Property) && p.Value == v.Value {
				v.Key = key
				used[key] = true
				break
			}
		}
	}
}

func hasProperty(properties []Property, property, value string) bool {
	for _, candidate := range properties {
		if sameFactProperty(candidate.Property, property) && propertyValueEqual(property, candidate.Value, value) {
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

	// Name lookup (formatted name or nickname, authority first, then the
	// one contact whose given name or first word it is).
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
		contacts, truncated, err := t.store.search(context.Background(), args.Query)
		if err != nil {
			return "", fmt.Errorf("search: %w", err)
		}
		if len(contacts) == 0 {
			return fmt.Sprintf("No contacts matching %q", args.Query), nil
		}
		if truncated {
			return formatSearchResults(contacts, args.Query) + fmt.Sprintf("\nStopped at %d matches; more contacts match %q. Contacts whose formatted name, nickname, given name or first word it is are listed first. contact_lookup with a contact's full formatted name as name reaches one this list left out.\n", SearchLimit, args.Query), nil
		}
		return formatSearchResults(contacts, args.Query), nil
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
	// Name resolves the contact the way contact_lookup does.
	Name string `json:"name,omitempty"`
	// ContactID selects one active record by canonical UUID. Exactly
	// one of Name and ContactID is set.
	ContactID string `json:"contact_id,omitempty"`
}

// ForgetContact soft-deletes one known contact, resolved once by name
// with the same resolution contact_lookup uses or selected by its
// contact_id, and names the record it removed. Contacts above known,
// the operator's own contact and
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
// Resolution and the custody checks run under ctx, and a turn that has
// ended by the delete removes nothing. The guarded delete itself is not
// canceled once it starts, so the result it reports is the truth.
func (t *Tools) ForgetContactFromModel(ctx context.Context, argsJSON string, provenance *PropertyProvenance) (string, error) {
	var args ForgetContactArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	c, byName, err := t.forgetTarget(ctx, args)
	if err != nil {
		return "", err
	}

	reasons, err := t.forgetCustodyReasons(ctx, c)
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
		hint := ""
		if byName {
			hint = t.forgetSiblingHint(ctx, c, args.Name)
		}
		return "", fmt.Errorf("contact_forget refused %s (%s, %s): %s. Contacts above known, the operator's own contact and contacts bound to a Home Assistant person are operator-custodied, because forgetting one turns that person's email and Signal traffic into a stranger's. Nothing was removed. %s%s",
			c.FormattedName, c.TrustZone, c.ID, strings.Join(texts, "; "), forgetRecovery(reasons, c.ID), hint)
	}

	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("contact_forget stopped before removing %s (%s): %w; nothing was removed", c.FormattedName, c.ID, err)
	}
	// Once started, the guarded delete runs to completion: the driver
	// can report a delete that already committed as the context's error,
	// and this result must say truthfully whether the record is gone.
	deleted, err := t.store.deleteIfUncustodied(context.WithoutCancel(ctx), c.ID)
	if err != nil {
		return "", fmt.Errorf("%w; nothing was removed", err)
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
func (t *Tools) forgetCustodyReasons(ctx context.Context, c *Contact) ([]forgetCustodyReason, error) {
	var reasons []forgetCustodyReason
	if c.TrustZone != ZoneKnown {
		reasons = append(reasons, forgetCustodyReason{rule: IdentityReasonZone, text: fmt.Sprintf("it is %s", c.TrustZone)})
	}
	operatorID, err := t.custodyOperatorID(ctx)
	if err != nil {
		return nil, err
	}
	if operatorID != uuid.Nil && c.ID == operatorID {
		reasons = append(reasons, forgetCustodyReason{rule: IdentityReasonOperator, text: "it is the operator's own contact"})
	}
	entity, _, err := t.store.haPersonEntity(ctx, c.ID)
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

// generateEmbedding creates and stores an embedding for a contact,
// under the caller's ctx.
func (t *Tools) generateEmbedding(ctx context.Context, c *Contact) {
	if t.embeddings == nil {
		return
	}

	props, _ := t.store.getProperties(ctx, c.ID)
	embText := buildEmbeddingText(c, props)
	emb, err := t.embeddings.Generate(ctx, embText)
	if err != nil {
		return
	}
	_ = t.store.setEmbedding(ctx, c.ID, emb)
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
	return t.importVCF(context.Background(), argsJSON, &PropertyProvenance{Source: importVCFSource})
}

// ImportVCFFromModel applies contact_import_vcf with the current model
// turn's provenance, stamped on every imported property and on the
// custody log. A vCard is content, not the operator's word, so no turn
// lifts identity custody here: addresses and numbers are dropped from a
// merge into a contact above known or the operator's own, and wherever
// an admin, household, trusted or operator contact already holds them.
// The rest of the card still imports and the result counts the drops.
// The import runs under ctx: once the turn ends it stops reading the
// vCard and starts writing no further card. Each card is written whole
// in one transaction that rechecks custody, so the stop report's
// counts, drops included, match what the store holds, and no operator
// write between the check and the write can give a value a second
// holder.
func (t *Tools) ImportVCFFromModel(ctx context.Context, argsJSON string, provenance *PropertyProvenance) (string, error) {
	return t.importVCF(ctx, argsJSON, provenance)
}

// contextReader fails every read once ctx ends, so a canceled import
// stops decoding a large vCard file.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func (t *Tools) importVCF(ctx context.Context, argsJSON string, provenance *PropertyProvenance) (string, error) {
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

	decoded, allProps, err := DecodeVCards(contextReader{ctx: ctx, r: r})
	if err != nil {
		return "", fmt.Errorf("decode vcards: %w", err)
	}

	var created, updated, skipped int
	var drops, before importDrops
	var summary strings.Builder
	operator := t.lazyCustodyOperatorID(ctx)

	// stop is the result of an import that ends before card i is
	// written, for an ended turn or any other in-loop error: what the
	// cards before it wrote and left out, and how to finish.
	stop := func(i int, cause error) error {
		if args.DryRun {
			return fmt.Errorf("contact_import_vcf stopped before card %d of %d: %w; a dry run writes nothing, so rerun it for the whole preview", i+1, len(decoded), cause)
		}
		return fmt.Errorf("contact_import_vcf stopped before card %d of %d: %w; %d contact(s) were created and %d merged before it stopped (%d skipped).%s To finish, import the cards from card %d on, or rerun the same import with merge on (the default), which merges the cards already written rather than duplicating them",
			i+1, len(decoded), cause, created, updated, skipped, before.notes(), i+1)
	}

	for i, incoming := range decoded {
		before = drops
		if err := ctx.Err(); err != nil {
			return "", stop(i, err)
		}
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
			existing = t.findExistingForMerge(ctx, incoming, props)
		}

		// Under the legacy owner-name selector a record that carries
		// the owner name as its name or nickname could become the
		// operator the next time the name resolves.
		skip, droppedNickname, err := t.withoutOwnerNameClaim(existing, incoming, operator)
		if err != nil {
			return "", stop(i, err)
		}
		if skip || droppedNickname {
			drops.ownerName++
		}
		if skip {
			continue
		}

		target, guard, err := importCustody(existing, incoming, props, operator)
		if err != nil {
			return "", stop(i, err)
		}
		query := func(query string, args ...any) (*sql.Rows, error) {
			return t.store.db.QueryContext(ctx, query, args...)
		}
		// A name or nickname an admin, household, trusted or operator
		// contact goes by stays theirs: a new card claiming one is left
		// out, and a merge leaves the nickname fill out, as does a merge
		// into a contact whose nickname is operator custody.
		refusedNames, err := t.withoutAuthorityNameClaim(query, target, &guard, incoming)
		if err != nil {
			return "", stop(i, err)
		}
		if len(refusedNames) > 0 {
			logIdentityRefusal("contact_import_vcf", target, guard.snapshotZone, refusedNames, provenance, args.DryRun)
			if existing == nil {
				skipped++
				if args.DryRun {
					fmt.Fprintf(&summary, "Would skip card %d: an admin, household, trusted or operator contact already goes by its name or nickname; import it under a fuller name or without that nickname\n", i+1)
				} else {
					drops.nameTaken = append(drops.nameTaken, i+1)
				}
				continue
			}
			drops.naming++
		}
		props, refused, err := t.withoutCustodiedIdentity(ctx, target, guard, props)
		if err != nil {
			return "", stop(i, err)
		}
		if len(refused) > 0 {
			drops.countRefused(refused)
			logIdentityRefusal("contact_import_vcf", target, guard.snapshotZone, refused, provenance, args.DryRun)
		}
		// findExistingForMerge reads a failed lookup as no match, so a
		// turn that ended during the reads above stops here rather than
		// create a card a merge would have found.
		if err := ctx.Err(); err != nil {
			return "", stop(i, err)
		}
		for j := range props {
			props[j].Provenance = provenance
		}

		// A card that would create a contact needs a name, which a
		// control character in FN may have cleared above. The dry run
		// skips it too, so it never promises a contact the import
		// would not create.
		if existing == nil && incoming.FormattedName == "" {
			skipped++
			if args.DryRun {
				fmt.Fprintf(&summary, "Would skip card %d: it has no usable name (FN)\n", i+1)
			} else {
				drops.nameless = append(drops.nameless, i+1)
			}
			continue
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

		// A card whose reads have passed is written whole, in one
		// transaction that rechecks custody against what the store holds
		// then: its writes run to completion even if the turn ends
		// meanwhile, so the counts this import reports match the store.
		// A card the recheck refuses whole (its merge target's zone
		// moved, or it was deleted) writes nothing and is skipped, as is
		// a card whose transaction fails: the transaction begins
		// deferred, so an operator write that commits between its first
		// read and its first write fails it with SQLITE_BUSY_SNAPSHOT.
		// The result names each skipped card with its cause. The
		// embedding is optional maintenance and still stops with the
		// turn.
		c := incoming
		if existing != nil {
			// Merge: fill empty scalar fields only.
			t.mergeContact(existing, incoming)
			c = existing
		}
		raced, failures, err := t.store.applyContactImport(context.WithoutCancel(ctx), c, props, guard)
		if err != nil {
			skipped++
			if errors.Is(err, errContactChangedConcurrently) {
				drops.changed = append(drops.changed, i+1)
			} else {
				drops.unwritten = append(drops.unwritten, i+1)
			}
			slog.Warn("contact_import_vcf card not written",
				"card", i+1,
				"contact_id", target.String(),
				"error", err)
			continue
		}
		if len(raced) > 0 {
			drops.countRefused(raced)
			logIdentityRefusal("contact_import_vcf", c.ID, guard.snapshotZone, raced, provenance, false)
		}
		drops.writeFailures += failures
		t.generateEmbedding(ctx, c)
		if existing != nil {
			updated++
		} else {
			created++
		}
	}

	notes := drops.notes()
	if args.DryRun {
		return fmt.Sprintf("Dry run — %d would be created, %d would be merged, %d would be skipped:%s\n\n%s",
			created, updated, skipped, notes, summary.String()), nil
	}

	return fmt.Sprintf("Imported %d contacts: %d created, %d merged, %d skipped.%s",
		created+updated, created, updated, skipped, notes), nil
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
// would create such a record is skipped (skip); a merge that would fill
// an existing record's empty nickname with it leaves the nickname out
// (droppedNickname). Each is one name the import result counts.
func (t *Tools) withoutOwnerNameClaim(existing, incoming *Contact, operator func() (uuid.UUID, error)) (skip, droppedNickname bool, err error) {
	owner := t.legacyOwnerName()
	if owner == "" {
		return false, false, nil
	}
	if existing == nil {
		return claimsOwnerName(owner, incoming.FormattedName, incoming.Nickname), false, nil
	}
	if existing.Nickname != "" || !claimsOwnerName(owner, incoming.Nickname) {
		return false, false, nil
	}
	operatorID, err := operator()
	if err != nil {
		return false, false, err
	}
	if operatorID != uuid.Nil && existing.ID == operatorID {
		return false, false, nil
	}
	incoming.Nickname = ""
	return false, true, nil
}

// lazyCustodyOperatorID resolves the operator's record under ctx at most
// once per import, and only when a card needs it.
func (t *Tools) lazyCustodyOperatorID(ctx context.Context) func() (uuid.UUID, error) {
	var (
		resolved bool
		id       uuid.UUID
		err      error
	)
	return func() (uuid.UUID, error) {
		if !resolved {
			id, err = t.custodyOperatorID(ctx)
			resolved = true
		}
		return id, err
	}
}

// importCustody returns the record a card writes to (existing's ID, or
// uuid.Nil for a record about to be created) and the guard both of the
// card's custody checks judge it by: the import's own, and the recheck
// inside its write transaction. It is read before the merge fills the
// snapshot. The operator is resolved only when the card carries a
// custodied value or claims a name. A card that carries only names
// survives a failed lookup with every holder counted as authority, so
// the name rules fail closed without stopping the import; a custodied
// value still stops it.
func importCustody(existing, incoming *Contact, props []Property, operator func() (uuid.UUID, error)) (uuid.UUID, identityGuard, error) {
	target, guard := uuid.Nil, identityGuard{snapshotZone: ZoneKnown}
	if existing != nil {
		target, guard.snapshotZone, guard.snapshotNickname = existing.ID, existing.TrustZone, existing.Nickname
	}
	guard.claims = importClaims(existing, incoming)
	custodied := hasCustodiedProperty(props)
	if !custodied && len(guard.claims) == 0 {
		return target, guard, nil
	}
	operatorID, err := operator()
	switch {
	case err == nil:
		guard.operatorID = operatorID
	case custodied:
		return uuid.Nil, identityGuard{}, err
	default:
		slog.Warn("contact_import_vcf could not resolve the operator contact; judging this card's names with every holder as authority",
			"contact_id", target.String(),
			"error", err)
		guard.operatorUnresolved = true
	}
	return target, guard, nil
}

// withoutCustodiedIdentity drops the addresses, numbers and routing
// facts a model-facing import may not write to target under guard and
// returns the kept properties with the refused values. The card's name
// claims were judged already, so they are not judged again here.
func (t *Tools) withoutCustodiedIdentity(ctx context.Context, target uuid.UUID, guard identityGuard, props []Property) ([]Property, []IdentityViolation, error) {
	if !hasCustodiedProperty(props) {
		return props, nil, nil
	}
	query := func(query string, args ...any) (*sql.Rows, error) {
		return t.store.db.QueryContext(ctx, query, args...)
	}
	guard.claims = nil
	violations, err := identityViolations(query, target, guard, props)
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

// findExistingForMerge looks for an existing contact that matches the
// incoming contact. It first tries EMAIL matching, then falls back to
// formatted name.
func (t *Tools) findExistingForMerge(ctx context.Context, incoming *Contact, props []Property) *Contact {
	// Try EMAIL match first (exact, case-insensitive).
	for _, p := range props {
		if p.Property == "EMAIL" && p.Value != "" {
			matches, err := t.store.FindAllByPropertyExact(ctx, "EMAIL", p.Value)
			if err == nil && len(matches) == 1 {
				full, err := t.store.getWithProperties(ctx, matches[0].ID)
				if err == nil {
					return full
				}
			}
		}
	}

	// Fall back to name match.
	if incoming.FormattedName != "" {
		existing, err := t.store.findByName(ctx, incoming.FormattedName)
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
		writeContactRow(&sb, c)
		sb.WriteString("\n")
	}
	return sb.String()
}

// formatSearchResults formats the contacts a query found. Each row
// carries the contact's contact_id and trust zone, since a query is
// where an ambiguous name sends the model to find the candidates the
// error left out, and two of them can share a formatted name. A contact
// that answers to the query as a name also names the field it answers
// by, as the ambiguity error does.
func formatSearchResults(contacts []*Contact, query string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d contact(s):\n\n", len(contacts)))
	for _, c := range contacts {
		writeContactRow(&sb, c)
		sb.WriteString(fmt.Sprintf("\n  contact_id %s | trust zone %s", c.ID, c.TrustZone))
		if field := NameMatchField(c, query); field != "" {
			sb.WriteString(fmt.Sprintf(" | answers to %q by %s", query, field))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// writeContactRow writes the one-line summary a contact list shows: the
// formatted name, then the organization and AI summary when present.
func writeContactRow(sb *strings.Builder, c *Contact) {
	sb.WriteString(fmt.Sprintf("**%s**", c.FormattedName))
	if c.Org != "" {
		sb.WriteString(fmt.Sprintf(" (%s)", c.Org))
	}
	if c.AISummary != "" {
		sb.WriteString(fmt.Sprintf(" — %s", c.AISummary))
	}
}
