package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

// SetContactTools adds contact management tools to the registry.
func (r *Registry) SetContactTools(ct *contacts.Tools) {
	r.contactTools = ct
	r.registerContactTools()
}

func (r *Registry) registerContactTools() {
	if r.contactTools == nil {
		return
	}
	saveDescription := "Store or update structured identity for a person, organization, or group. Properties should be compact personal attributes such as communication coordinates, aliases, roles, and stable preferences. Standard contact info (email, phone) is mapped to vCard property names automatically. Use origin_tags and origin_context_refs only to shape future sessions when this contact is the runtime origin. Evolving person-specific relationship or collaboration synthesis belongs in contact_dossier_write when available; project knowledge, technical decisions, and other non-person knowledge belong in remember_fact or documents. When updating an existing contact, only non-empty scalar fields are overwritten; facts are additive. origin_tags and origin_context_refs are replaced when provided, and an empty array clears that origin policy field."
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
					"description": "Display name of the person or organization (vCard FN)",
				},
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{"individual", "group", "org", "location"},
					"description": "Type of contact (default: individual)",
				},
				"given_name": map[string]any{
					"type":        "string",
					"description": "First/given name (vCard N given-name component)",
				},
				"family_name": map[string]any{
					"type":        "string",
					"description": "Last/family name (vCard N family-name component)",
				},
				"nickname": map[string]any{
					"type":        "string",
					"description": "Preferred nickname or alias (vCard NICKNAME). Used in contact resolution.",
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
					"description":          "Attributes as key-value pairs. All entries are stored as contact properties. Standard keys like 'email' and 'phone' are mapped to vCard property names (EMAIL, TEL); others use their key as-is (e.g., {\"email\": \"alice@example.com\", \"phone\": \"555-1234\", \"ha_companion_app\": \"mobile_app_phone\"}).",
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
			return r.contactTools.SaveContactFromModel(ctx, string(argsJSON), contactPropertyProvenance(ctx))
		},
	})

	registerContactDossierReadTool(r, r.contactTools)
	registerContactDossierWriteTool(r, r.contactTools)

	r.Register(&Tool{
		Name:        "contact_lookup",
		Description: "Look up contacts from the directory. Search by name, query, kind, or property key/value. An exact rich result includes the canonical contact UUID and, when configured, an exact contact_dossier_read call that safely probes the canonical dossier without constructing a document ref. Dossier prose is not structured identity authority. With no arguments, returns directory statistics.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Exact name to look up (case-insensitive, also checks nickname)",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "Search term to find matching contacts",
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
		Description: "Remove a contact from the directory by name.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Name of the contact to remove",
				},
			},
			"required": []string{"name"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			argsJSON, err := json.Marshal(args)
			if err != nil {
				return "", fmt.Errorf("failed to serialize arguments: %w", err)
			}
			return r.contactTools.ForgetContact(string(argsJSON))
		},
	})

	r.Register(&Tool{
		Name:        "contact_list",
		Description: "List contacts from the directory. Optionally filter by kind and limit the number of results.",
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
					"description": "Contact name to export, or \"self\" for the agent's own card",
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
		Description: "Import contacts from a vCard (.vcf) file or text. Supports single and multi-contact vCards. By default, merges with existing contacts matched by email or name — only empty fields are filled, and TrustZone and AISummary are never overwritten. New contacts are always created at the default trust zone; a vCard X-THANE-TRUST-ZONE is ignored on import (trust zones are operator-assigned). Use dry_run to preview changes.",
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
			return r.contactTools.ImportVCF(string(argsJSON))
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
					"description": "Contact name to export, or \"self\" for the agent's own card",
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

func contactPropertyProvenance(ctx context.Context) *contacts.PropertyProvenance {
	provenance := &contacts.PropertyProvenance{
		Source:         "contact_save",
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
		description := field.Guidance + documentfacets.FormatGuidance(field.Format)
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
		Description:        "Create or replace one contact's canonical longitudinal dossier. Pass the canonical contact UUID only as contact_id; do not repeat it or its derived contacts ref or contact tag in any content projection. Omit the contact's canonical name from status_line and teaser because the structured record and dossier title already identify the subject; digest and full may use it when standalone prose needs it. Go verifies the structured contact and owns the document ref, private contact tag, frontmatter, section headings, and ordering. Use this for evolving relationship context, preferences, recurring themes, and evidence synthesis—not structured identity, trust, Home Assistant bindings, or companion attribution. Archive-session evidence must cite the full canonical session UUID so every claim remains checkable. Every projection is validated together and every violation is returned in one error. Call contact_dossier_read first: it reads an existing dossier with revision protection or returns a successful, actionable absence result.",
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
			return contactTools.WriteDossier(ctx, contacts.DossierWriteArgs{
				ContactID:    stringArg(args, "contact_id"),
				StatusLine:   stringArg(args, "status_line"),
				Teaser:       stringArg(args, "teaser"),
				Digest:       stringArg(args, "digest"),
				Full:         stringArg(args, "full"),
				ReceiptScope: documentRevisionScope(ctx),
			})
		},
	})
}
