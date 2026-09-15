package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

// contactNameRule says what a contact name argument is matched against,
// as [contacts.Store.ResolveContact] matches it, for every tool that
// takes one. Each tool ends it with contactNameRetryByID or
// contactNameRetryByName, because the next move after an ambiguous name
// depends on whether the tool also takes a contact_id.
const contactNameRule = "Matched case-insensitively against each contact's formatted name or nickname; when several contacts hold it that way, the operator's own contact wins, then one above known, and two or more at the same standing (both above known, or both known) are a tie: none of them is chosen, whichever holds it as a formatted name and whichever as a nickname. Only when no contact's formatted name or nickname is exactly the name is it matched against each contact's given name or the first word of its formatted name, and then exactly one contact must fit. Notes, AI summaries and organizations are never used to resolve a name. When a name is tied, or two or more contacts fit by given name or first word, none is chosen: the error lists up to five of them, each with its full formatted name, trust zone and contact_id, and counts the rest, which contact_lookup with the name as query lists ahead of any other match, each with its contact_id, up to 50"

// contactNameRetryByID ends contactNameRule for a tool that also takes
// contact_id.
const contactNameRetryByID = contactNameRule + "; retry with the contact_id of the one you mean."

// contactNameRetryByName ends contactNameRule for a tool that takes only
// a name.
const contactNameRetryByName = contactNameRule + "; retry with the full formatted name of the one you mean, unless that is the tied name itself: then only its contact_id tells it apart, so ask the operator which contact should keep the name."

// SetContactTools adds contact management tools to the registry.
func (r *Registry) SetContactTools(ct *contacts.Tools) {
	r.contactTools = ct
	r.registerContactTools()
}

func (r *Registry) registerContactTools() {
	if r.contactTools == nil {
		return
	}
	saveDescription := "Store or update structured identity for a person, organization, or group. Properties should be compact personal attributes such as communication coordinates, aliases, roles, and stable preferences. Standard contact info (email, phone) is mapped to vCard property names automatically. Use origin_tags and origin_context_refs only to shape future sessions when this contact is the runtime origin. Evolving person-specific relationship or collaboration synthesis belongs in contact_dossier_write when available; project knowledge, technical decisions, and other non-person knowledge belong in remember_fact or documents. When updating an existing contact, only non-empty scalar fields are overwritten; facts are additive. origin_tags and origin_context_refs are replaced when provided, and an empty array clears that origin policy field. Custody: the trust_zone argument, and KEY and X-THANE-* fact keys, are refused on every contact, and a new contact starts at known. Addresses and numbers (the email, phone, signal and matrix facts, or EMAIL, TEL and IMPP in any case) are how email and Signal recognize a contact and what the send gate trusts, so contact_save refuses to add one to an existing contact above known (admin, household or trusted) or to the operator's own contact at any zone. The same rule covers adding notification_preference or ha_companion_app (in any case), which pick the channel and the Home Assistant device that carry a contact's notifications and answer their decision requests, and changing the nickname or given name of such a contact, since lookups fall back to a given name. The operator's own message (sent through Thane's native API, or written in the operator's own channel conversation) lifts that one rule; there, add a value only when the operator says it belongs to that person. Notifications use only the first value of each routing fact, so a second value does not switch delivery: the result then names the value still used, and the operator removes it through CardDAV or the contacts API. In every turn, contact_save refuses a value an admin, household, trusted or operator contact already holds (email in any case; a phone number as phone or signal, with or without a leading '+'); refuses a new contact's name, or any contact's nickname, that one of those contacts already goes by as its name or nickname; and refuses to give any contact but the operator's own the name Thane recognizes the operator by, as its name or nickname, or to take that name off the operator's own contact. Outside the operator's own message it also refuses a new contact's name, or any contact's nickname, that one of those contacts answers to by its given name or the first word of its formatted name (Bob, while a household Bob Smith has no nickname Bob): a contact holding a name as its formatted name or nickname is found by it before any contact that has it as a first name, so it would take that person's notifications. Argument values may not contain carriage returns or other control characters; note and ai_summary may contain plain line breaks. When anything is refused nothing is saved, and the error lists each refused value, why, and what to do; the operator adds refused values through CardDAV or the contacts API."
	if r.contactTools.ContactRefreshesEnabled() {
		saveDescription += " A committed change is queued once for later archivist dossier reconsideration; an identical no-op is not, so do not duplicate structured identity into dossier prose."
	} else {
		saveDescription += " No archivist refresh consumer is enabled in this runtime, so committed changes are not queued for dossier reconsideration; do not duplicate structured identity into dossier prose."
	}

	r.Register(&Tool{
		Name:        "contact_save",
		Description: saveDescription,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Display name of the person or organization (vCard FN). It matches an existing contact by formatted name only, in any ASCII case; any other name creates a new contact at known. Notifications, decision requests, lookups and conversation context find a contact by its formatted name or nickname, and two or more at the same standing holding one name are a tie that reaches none of them, so a new contact named what a known contact already goes by, as its formatted name or nickname, makes that name reach neither; check with contact_lookup first.",
				},
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{"individual", "group", "org", "location"},
					"description": "Type of contact (default: individual)",
				},
				"given_name": map[string]any{
					"type":        "string",
					"description": "First/given name (vCard N given-name component). When no contact's formatted name or nickname is a name, lookups find the one contact whose given name or first word it is, so outside the operator's own message a change to the given name of a contact above known or of the operator's own contact is refused; a change only in edge space or the case of ASCII letters is not a change.",
				},
				"family_name": map[string]any{
					"type":        "string",
					"description": "Last/family name (vCard N family-name component)",
				},
				"nickname": map[string]any{
					"type":        "string",
					"description": "Another name this contact answers to (vCard NICKNAME). Notifications, decision requests, lookups and conversation context find a contact by its formatted name or nickname; when several contacts hold one name that way, the operator's own contact wins, then one above known, and two or more at the same standing are a tie that reaches none of them, so a nickname another contact at this contact's standing already goes by makes the name reach neither; check with contact_lookup first. Refused in every turn when an admin, household, trusted or operator contact already goes by it as its name or nickname. Outside the operator's own message, also refused when one of those contacts answers to it by its given name or the first word of its formatted name, and as a change to the nickname of a contact above known or of the operator's own contact; a change only in the case of ASCII letters is not a change.",
				},
				"org": map[string]any{
					"type":        "string",
					"description": "Organization name (vCard ORG)",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "Job title (vCard TITLE, e.g., 'Backend Engineer')",
				},
				"role": map[string]any{
					"type":        "string",
					"description": "Functional role (vCard ROLE, e.g., 'Engineering Lead')",
				},
				"note": map[string]any{
					"type":        "string",
					"description": "Free-form notes about this contact (vCard NOTE)",
				},
				"ai_summary": map[string]any{
					"type":        "string",
					"description": "AI-generated one-line context summary (e.g., 'Backend engineer at Anthropic, prefers Signal')",
				},
				"origin_tags": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Capability tags to pin automatically when this contact is the session origin. Do not use this for owner or message_channel; owner is asserted from trusted runtime identity, and message_channel is asserted by trusted current-run evidence.",
				},
				"origin_context_refs": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Supplemental managed document refs to inject when this contact is the session origin, such as kb:projects/current.md. Store person identity in the contact fields and ai_summary instead.",
				},
				"facts": map[string]any{
					"type":                 "object",
					"description":          "Attributes as key-value pairs, each stored as a contact property. The keys email, phone, signal and matrix are addresses and numbers: email maps to EMAIL, phone to TEL, and signal and matrix to IMPP with a signal: or matrix: prefix; EMAIL, TEL and IMPP written in any case land on the same property. Every other key is stored as written and must be a plain name of letters, digits, '-' and '_' that starts with a letter, at most 64 characters, because '.', ';', ':', spaces and line breaks are vCard syntax; KEY and X-THANE-* keys are refused, and values may not contain line breaks or other control characters. A key naming a field the contact record owns (note, title, role, org, nickname, kind, or the vCard names FN, N, BDAY, ANNIVERSARY, GENDER, PHOTO, UID, REV and VERSION) is refused too, because a fact under that name would be lost on the operator's next edit; use the matching argument. Outside the operator's own message, addresses and numbers are refused on a contact above known and on the operator's own contact; in every turn they are refused when an admin, household, trusted or operator contact already holds them. The routing facts notification_preference and ha_companion_app, in any case, pick the channel and the Home Assistant device that carry a contact's notifications and answer their decision requests; they follow the first of those rules only, and delivery uses only the first value of each, so adding a second does not switch it. Example: {\"email\": \"alice@example.com\", \"phone\": \"555-1234\", \"ha_companion_app\": \"mobile_app_phone\"}.",
					"additionalProperties": map[string]any{"type": "string"},
				},
			},
			"required": []string{"name"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			argsJSON, err := json.Marshal(args)
			if err != nil {
				return "", fmt.Errorf("failed to serialize arguments: %w", err)
			}
			return r.contactTools.SaveContactFromModel(ctx, string(argsJSON), contactPropertyProvenance(ctx, "contact_save"), OperatorAttended(ctx))
		},
	})

	registerContactDossierReadTool(r, r.contactTools)
	registerContactDossierWriteTool(r, r.contactTools)

	r.Register(&Tool{
		Name:        "contact_lookup",
		Description: "Look up contacts from the directory. Search by name, query, kind, or property key/value. A name finds the contact whose formatted name or nickname it is, case-insensitive; when several contacts hold it that way, the operator's own contact wins, then one above known. So a known duplicate whose formatted name or nickname a contact above known also goes by is never what that name returns. Two or more holders at the same standing, both above known or both known, are a tie: it returns none of them, whichever holds the name as a formatted name and whichever as a nickname, tries no given name or first word, and the error lists up to five of them with each one's contact_id, trust zone and the field it holds the name by, and counts the rest. Only when no contact's formatted name or nickname is the name does it try each contact's given name and the first word of its formatted name, and then exactly one contact must fit: when two or more do, whatever their zones, the error lists up to five of them with each one's contact_id, trust zone and the field it matched, counts the rest, and returns none of them. query set to the same name lists every contact that fits ahead of any other match, those holding it as a formatted name or nickname first, up to 50, so it reaches the ones the error left out; retry with the full formatted name of the one you mean, and pass its contact_id to tools that take one; when the one you mean has the tied name as its formatted name, only its contact_id tells it apart. A name never matches notes, summaries or organizations; query searches those as well as formatted names, nicknames and given names, lists up to 50 matches, and says so when more match than it lists. Each query row carries the contact's contact_id and trust zone, and the name field it answers by when it answers to the query as a name. Every list it returns, by query, kind or key/value, stays within 16 KB: a formatted name or organization over 256 bytes, or an AI summary over 512, is cut and ends with …[cut], and rows past the limit are left off the end and counted; narrow the query, or look one up by name, to list them. A whole name still beats a first name: \"Bob\" returns a known contact named just Bob, not a household Bob Smith, so check a first-name result against the contact_directory row of system_health, which names such pairs with their UUIDs. When contact dossiers are configured, a name result also carries the canonical contact UUID and an exact contact_dossier_read call that safely probes the canonical dossier without constructing a document ref. Dossier prose is not structured identity authority. With no arguments, returns directory statistics.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "A contact's name. " + contactNameRetryByName + " Use query to search notes and summaries.",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "Words to search for in formatted names, nicknames, given names, notes, AI summaries and organizations; returns up to 50 matching contacts as a list, those whose formatted name, nickname, given name or first word it is listed first, and says so when more match than it lists. Each row carries the contact's contact_id and trust zone, and the name field it answers by when it answers to the query as a name. The result stays within 16 KB: long names, organizations and summaries are cut and end with …[cut], and rows past the limit are left off the end and counted.",
				},
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{"individual", "group", "org", "location"},
					"description": "Filter by contact type",
				},
				"key": map[string]any{
					"type":        "string",
					"description": "Property key to filter by (e.g., 'email', 'phone', 'EMAIL', 'TEL', 'ha_companion_app'). Requires value.",
				},
				"value": map[string]any{
					"type":        "string",
					"description": "Value to match for the given key (requires key)",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			argsJSON, err := json.Marshal(args)
			if err != nil {
				return "", fmt.Errorf("failed to serialize arguments: %w", err)
			}
			return r.contactTools.LookupContact(string(argsJSON))
		},
	})

	r.Register(&Tool{
		Name:        "contact_owner",
		Description: "Return the primary operator contact record with rich details and contact properties, its canonical UUID and canonical dossier trailhead when configured, plus a structured summary of currently active operator-scoped channels. Use contact_dossier_read to inspect or discover an absent dossier; its prose is longitudinal synthesis, while this contact record remains authoritative for structured identity and bindings. Uses identity.operator_contact_id when configured; otherwise supports the legacy name selector and finally the sole admin contact if exactly one exists.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			argsJSON, err := json.Marshal(args)
			if err != nil {
				return "", fmt.Errorf("failed to serialize arguments: %w", err)
			}
			return r.contactTools.OwnerContact(string(argsJSON))
		},
	})

	r.Register(&Tool{
		Name:        "contact_forget",
		Description: "Remove one known contact from the directory; a soft delete that no model-facing tool can undo. Pass exactly one of name or contact_id. A name is resolved once, as contact_lookup resolves it: the contact whose formatted name or nickname it is, the operator's own contact first, then one above known, and none when two or more at the same standing hold it; else the one contact whose given name or first word it is, and none when two or more contacts share it. A name that resolves to none removes nothing, and the error lists up to five of their contact_id values. A contact_id removes exactly that active contact. The result names the record removed as \"Forgot contact: <name> (<zone>, <uuid>)\". Contacts above known, the operator's own contact and contacts bound to a Home Assistant person are operator-custodied and refused in every turn, by name or by contact_id, the operator's own message included, because forgetting one turns that person's email and Signal traffic into a stranger's; nothing is removed, and the operator deletes or demotes those through CardDAV or DELETE /v1/contacts/{id}. A known duplicate whose formatted name or nickname a contact above known also goes by resolves to that contact, so forgetting it by name is refused; the refusal names up to three known, unbound contacts the name also fits, with their UUIDs, and contact_forget with one of those contact_id values removes it. Forgetting the only contact whose formatted name or nickname is a name leaves that name with no exact holder: it then reaches the one contact whose given name or first word it is, or no one when two or more have it, so when the forgotten record held a name another contact should keep answering to, tell the operator which contact should carry it as a nickname. A known contact whose whole name is another's first word (\"Bob\" beside \"Bob Smith\") is what \"Bob\" resolves to, so check that the result names the record you meant.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Name of the contact to remove, resolved as contact_lookup resolves it. " + contactNameRetryByID + " Confirm the record with contact_lookup first. Omit it when passing contact_id.",
				},
				"contact_id": map[string]any{
					"type":        "string",
					"description": "Canonical UUID, lowercase with hyphens, of the one active contact to remove, instead of name. Pass exactly one of name or contact_id. Use it when the name resolves to a different record than the one to remove, such as a known duplicate whose formatted name or nickname a contact above known also goes by; a refused contact_forget by name, a contact_dossier_write refusal, and the contact_directory row of system_health give such a duplicate's UUID. Custody refuses the same contacts by contact_id as by name.",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			argsJSON, err := json.Marshal(args)
			if err != nil {
				return "", fmt.Errorf("failed to serialize arguments: %w", err)
			}
			return r.contactTools.ForgetContactFromModel(ctx, string(argsJSON), contactPropertyProvenance(ctx, "contact_forget"))
		},
	})

	r.Register(&Tool{
		Name:        "contact_list",
		Description: "List contacts from the directory in formatted-name order, up to 100. Optionally filter by kind and limit the number of results. The list stays within 16 KB: a formatted name or organization over 256 bytes, or an AI summary over 512, is cut and ends with …[cut], and rows past the limit are left off the end and counted; contact_lookup by name or query reaches them.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{"individual", "group", "org", "location"},
					"description": "Filter by contact type",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of contacts to return",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			argsJSON, err := json.Marshal(args)
			if err != nil {
				return "", fmt.Errorf("failed to serialize arguments: %w", err)
			}
			return r.contactTools.ListContacts(string(argsJSON))
		},
	})

	r.Register(&Tool{
		Name:        "contact_export_vcf",
		Description: "Export a contact as a vCard (.vcf) file or text. Use name=\"self\" to export the agent's own contact card. When exporting the self-contact, recipient_trust_zone controls which fields are included (e.g., a known contact gets fewer details than a trusted one).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Contact name to export, or \"self\" for the agent's own card. " + contactNameRetryByName,
				},
				"recipient_trust_zone": map[string]any{
					"type":        "string",
					"enum":        []string{"admin", "household", "trusted", "known", "unknown"},
					"description": "Trust zone of the recipient (self-contact only). Filters fields based on trust level.",
				},
				"format": map[string]any{
					"type":        "string",
					"enum":        []string{"file", "text"},
					"description": "Output format: \"file\" writes a .vcf temp file (default), \"text\" returns vCard inline",
				},
			},
			"required": []string{"name"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			argsJSON, err := json.Marshal(args)
			if err != nil {
				return "", fmt.Errorf("failed to serialize arguments: %w", err)
			}
			return r.contactTools.ExportVCF(string(argsJSON))
		},
	})

	r.Register(&Tool{
		Name:        "contact_export_all_vcf",
		Description: "Export all contacts (or a filtered subset) as a multi-vCard .vcf file. Useful for backups or bulk transfer.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{"individual", "group", "org", "location"},
					"description": "Filter by contact type",
				},
				"trust_zone": map[string]any{
					"type":        "string",
					"enum":        []string{"admin", "household", "trusted", "known"},
					"description": "Filter by trust zone",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			argsJSON, err := json.Marshal(args)
			if err != nil {
				return "", fmt.Errorf("failed to serialize arguments: %w", err)
			}
			return r.contactTools.ExportAllVCF(string(argsJSON))
		},
	})

	r.Register(&Tool{
		Name:        "contact_import_vcf",
		Description: "Import contacts from a vCard (.vcf) file or text. Supports single and multi-contact vCards. By default, merges with existing contacts matched by email or name — only empty fields are filled, and TrustZone and AISummary are never overwritten. New contacts are always created at the default trust zone; a vCard X-THANE-TRUST-ZONE is ignored on import (trust zones are operator-assigned), and KEY and X-THANE-KEY-* properties are dropped (keys that authenticate a contact's messages are operator custody). Addresses and numbers (EMAIL, TEL, IMPP) and the notification routing facts (NOTIFICATION_PREFERENCE and HA_COMPANION_APP, in any case) are dropped from a merge into a contact above known or the operator's own contact, and so is a nickname or given name the merge would fill in there, since lookups find a contact by either; addresses and numbers are also dropped from any contact, new or merged, when an admin, household, trusted or operator contact already holds them. A card that would create a contact under a name or nickname one of those contacts already goes by, or answers to by its given name or the first word of its formatted name, is left out, and a merge does not fill in such a nickname. A vCard is content, not the operator's word, so no turn lifts this, and the operator adds dropped values through CardDAV or the contacts API. Properties whose decoded names are not plain names (a nested group such as a.b.EMAIL, spaces, or more than 64 characters) are dropped; a single group such as item1.EMAIL imports as EMAIL under the address rules above. Values carrying a carriage return or other control character are dropped, and a card that would create a contact, or fill a nickname, under the name Thane recognizes the operator by is left out. Each card is written in one transaction that rechecks these rules and its merge target's zone, nickname and given name, so a value the operator gives a contact mid-import is still dropped, and a card is skipped when the operator re-zones or deletes its merge target or changes its nickname or given name mid-import, when a contact with authority takes the card's name or nickname mid-import, when its write fails (as it can when an operator change collides with it), or when it has no usable name. The result counts each kind of drop and names each skipped card with its cause and remedy; dry_run reports the same counts. Use dry_run to preview changes.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to a .vcf file to import",
				},
				"text": map[string]any{
					"type":        "string",
					"description": "Raw vCard text to import (alternative to path)",
				},
				"merge": map[string]any{
					"type":        "boolean",
					"description": "Merge with existing contacts (default: true). When false, always creates new contacts.",
				},
				"dry_run": map[string]any{
					"type":        "boolean",
					"description": "Preview import without writing to database",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			argsJSON, err := json.Marshal(args)
			if err != nil {
				return "", fmt.Errorf("failed to serialize arguments: %w", err)
			}
			return r.contactTools.ImportVCFFromModel(ctx, string(argsJSON), contactPropertyProvenance(ctx, "contact_import_vcf"))
		},
	})

	r.Register(&Tool{
		Name:        "contact_export_vcf_qr",
		Description: "Generate a QR code PNG containing a vCard for the named contact. The QR code can be scanned by mobile devices to add the contact. Use recipient_trust_zone to control which fields are included (reduces size for QR capacity).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Contact name to export, or \"self\" for the agent's own card. " + contactNameRetryByName,
				},
				"recipient_trust_zone": map[string]any{
					"type":        "string",
					"enum":        []string{"admin", "household", "trusted", "known", "unknown"},
					"description": "Trust zone of the recipient. Filters fields for smaller QR code.",
				},
			},
			"required": []string{"name"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			argsJSON, err := json.Marshal(args)
			if err != nil {
				return "", fmt.Errorf("failed to serialize arguments: %w", err)
			}
			return r.contactTools.ExportVCFQR(string(argsJSON))
		},
	})
}

// contactPropertyProvenance stamps the current model turn onto the rows a
// contact tool writes, under the tool's own source name.
func contactPropertyProvenance(ctx context.Context, source string) *contacts.PropertyProvenance {
	provenance := &contacts.PropertyProvenance{
		Source:         source,
		Model:          strings.TrimSpace(ModelFromContext(ctx)),
		LoopID:         strings.TrimSpace(LoopIDFromContext(ctx)),
		ConversationID: strings.TrimSpace(ConversationIDFromContext(ctx)),
		SessionID:      strings.TrimSpace(SessionIDFromContext(ctx)),
		RequestID:      strings.TrimSpace(RequestIDFromContext(ctx)),
		ToolCallID:     strings.TrimSpace(ToolCallIDFromContext(ctx)),
	}
	if provenance.ConversationID == "default" {
		provenance.ConversationID = ""
	}
	if iteration, ok := IterationIndexFromContext(ctx); ok {
		provenance.Iteration = &iteration
	}
	return provenance
}

func registerContactDossierReadTool(r *Registry, contactTools *contacts.Tools) {
	if contactTools == nil || !contactTools.DossierReadsEnabled() {
		return
	}
	r.Register(&Tool{
		Name:        "contact_dossier_read",
		Description: "Read or probe one contact's canonical longitudinal dossier. Pass only the canonical contact UUID returned by contact_lookup or contact_owner; Go derives and validates the document ref and records the revision receipt needed for a safe later contact_dossier_write. Every success has the same envelope: dossier.exists is authoritative, dossier.ref is canonical, dossier.document contains the document payload or null, and next_action is null unless absence requires guidance. Do not retry an absent dossier read until a write succeeds, and never manually construct contacts:<uuid>.md for doc_read.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"contact_id": map[string]any{
					"type":        "string",
					"description": "Canonical contact UUID returned by contact_lookup or contact_owner. Go derives the dossier ref.",
				},
			},
			"required": []string{"contact_id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return contactTools.ReadDossier(ctx, contacts.DossierReadArgs{
				ContactID:    stringArg(args, "contact_id"),
				ReceiptScope: documentRevisionScope(ctx),
			})
		},
	})
}

func registerContactDossierWriteTool(r *Registry, contactTools *contacts.Tools) {
	if contactTools == nil || !contactTools.DossierWritesEnabled() {
		return
	}

	fields := contacts.DossierFacetFields()
	properties := make(map[string]any, len(fields)+1)
	properties["contact_id"] = map[string]any{
		"type":        "string",
		"description": "Canonical contact UUID returned by contact_lookup or contact_owner. Go derives the contacts:<uuid>.md ref and matching private subject tag.",
	}
	required := []string{"contact_id"}
	for _, field := range fields {
		description := field.Guidance + documentfacets.FormatGuidance(field.Format) + documentfacets.RelativeTimeGuidance
		if field.Key == "status_line" || field.Key == "teaser" {
			description += " Omit the contact's canonical name: the structured record and dossier title already identify the subject."
		}
		if field.Key == "full" {
			description += " Cite archive-session evidence as archive:session:<full-session-uuid>; the full canonical session UUID is required because short prefixes can be ambiguous."
		}
		if field.MaxRunes > 0 {
			description = fmt.Sprintf("%s Maximum %d characters — a ceiling, not a target; compose comfortably under it.", description, field.MaxRunes)
		}
		properties[field.Key] = map[string]any{
			"type":        "string",
			"description": description,
		}
		required = append(required, field.Key)
	}
	allowedParameters := make(map[string]struct{}, len(required))
	for _, name := range required {
		allowedParameters[name] = struct{}{}
	}

	r.Register(&Tool{
		Name:               "contact_dossier_write",
		Description:        "Create or replace one contact's canonical longitudinal dossier. Pass the canonical contact UUID only as contact_id; do not repeat it or its derived contacts ref or contact tag in any content projection. Omit the contact's canonical name from status_line and teaser because the structured record and dossier title already identify the subject; digest and full may use it when standalone prose needs it. Go verifies the structured contact and owns the document ref, private contact tag, frontmatter, section headings, and ordering. Use this for evolving relationship context, preferences, recurring themes, and evidence synthesis—not structured identity, trust, Home Assistant bindings, or companion attribution. Archive-session evidence must cite the full canonical session UUID so every claim remains checkable. Every projection is validated together: a rejected write stores nothing and lists every violation in one error, and an over-budget field carries its overage and whether rewording closes it or whole items must go, so fix them all in the next call. Call contact_dossier_read first: it reads an existing dossier with revision protection or returns a successful, actionable absence result. Replacing an existing dossier with no read of it on record, or after it changed since that read, is refused with an error that stores nothing: read it with contact_dossier_read, fold in any intervening change the error carries, and call again. The first write of a contact's dossier is refused, and nothing is written, when another active contact that shares a name with it and looks like the same person already has a dossier. They share a name when one answers to the other's formatted name or nickname, or one's whole formatted name is the other's given name or the first word of its formatted name. They look like one person when they share an address or number, when the one with no more authority holds no real address or number of its own, or when one is a known contact bound to a Home Assistant person; people who merely share a name each keep their own dossier. The refusal names both UUIDs and the evidence, and says what to do if they are one person: write into the existing dossier when its contact carries as much authority, or, when this contact carries more and the holder is a known, unbound duplicate, read the duplicate's dossier, forget the duplicate by contact_id, then write this one. If they are different people, write nothing and report both to the operator. Replacing a dossier that already exists is never refused.",
		SkipContentResolve: true,
		Parameters: map[string]any{
			"type":       "object",
			"properties": properties,
			"required":   required,
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			unexpected := make([]string, 0)
			for name := range args {
				if _, allowed := allowedParameters[name]; !allowed {
					unexpected = append(unexpected, name)
				}
			}
			if len(unexpected) > 0 {
				sort.Strings(unexpected)
				return "", fmt.Errorf("contact_dossier_write accepts only contact_id, status_line, teaser, digest, and full; remove unsupported parameter(s) [%s]—Go derives document identity and structure, and tracks revisions automatically", strings.Join(unexpected, ", "))
			}
			result, err := contactTools.WriteDossier(ctx, contacts.DossierWriteArgs{
				ContactID:    stringArg(args, "contact_id"),
				StatusLine:   stringArg(args, "status_line"),
				Teaser:       stringArg(args, "teaser"),
				Digest:       stringArg(args, "digest"),
				Full:         stringArg(args, "full"),
				ReceiptScope: documentRevisionScope(ctx),
			})
			if errors.Is(err, contacts.ErrDossierTargetRefused) {
				// The contact_id was refused, not the dossier: a loop must
				// not wait for that contact_id to land.
				return result, &ErrTargetRefused{Err: err}
			}
			return result, err
		},
	})
}
