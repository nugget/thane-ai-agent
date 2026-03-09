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
		Description: "Store or update a person, organization, or group in the contact directory. Properties should be personal attributes: communication preferences, trust levels, aliases, and behavioral patterns. Standard contact info (email, phone) is mapped to vCard property names automatically. Do NOT store project knowledge, design philosophy, technical insights, or collaboration patterns here — use remember_fact or workspace files instead. When updating an existing contact, only non-empty fields are overwritten.",
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
}
