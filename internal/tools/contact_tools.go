package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/contacts"
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

	r.Register(&Tool{
		Name:        "save_contact",
		Description: "Store or update a person, organization, or group in the contact directory. Properties should be personal attributes: communication preferences, trust levels, aliases, and behavioral patterns. Standard contact info (email, phone) is mapped to vCard property names automatically. Use origin_tags and origin_context_refs only to shape future sessions when this contact is the runtime origin. Do NOT store project knowledge, design philosophy, technical insights, or collaboration patterns here — use remember_fact or workspace files instead. When updating an existing contact, only non-empty scalar fields are overwritten; facts are additive. origin_tags and origin_context_refs are replaced when provided, and an empty array clears that origin policy field.",
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
				"trust_zone": map[string]any{
					"type":        "string",
					"enum":        []string{"admin", "household", "trusted", "known"},
					"description": "Trust zone for this contact. Gates model access, tool permissions, and send policy. admin=full access, household=family members, trusted=established relationship, known=default/gated.",
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
					"description": "Capability tags to pin automatically when this contact is the session origin. Do not use this for owner; owner is asserted from trusted runtime identity.",
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
			return r.contactTools.SaveContact(string(argsJSON))
		},
	})

	r.Register(&Tool{
		Name:        "lookup_contact",
		Description: "Look up contacts from the directory. Search by name, query, kind, or property key/value. With no arguments, returns directory statistics.",
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
		Name:        "owner_contact",
		Description: "Return the primary owner/operator contact record with rich details and contact properties, plus a structured summary of currently active owner-scoped channels. Uses identity.owner_contact_name when configured; otherwise falls back to the sole admin contact if exactly one exists.",
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
		Name:        "forget_contact",
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
		Name:        "list_contacts",
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
		Name:        "export_vcf",
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
		Name:        "export_all_vcf",
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
		Name:        "import_vcf",
		Description: "Import contacts from a vCard (.vcf) file or text. Supports single and multi-contact vCards. By default, merges with existing contacts matched by email or name — only empty fields are filled, TrustZone and AISummary are never overwritten. Use dry_run to preview changes.",
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
		Name:        "export_vcf_qr",
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
