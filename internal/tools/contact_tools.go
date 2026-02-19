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
		Description: "Store or update a person or organization in the contact directory. Use for people, companies, or organizations you interact with. Supports structured attributes like email, phone, role, etc. When updating an existing contact, only non-empty fields are overwritten.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Full name of the person or organization",
				},
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{"person", "company", "organization"},
					"description": "Type of contact (default: person)",
				},
				"relationship": map[string]any{
					"type":        "string",
					"description": "Relationship to the user (e.g., friend, colleague, family, vendor)",
				},
				"summary": map[string]any{
					"type":        "string",
					"description": "One-line summary (e.g., 'Backend engineer at Anthropic')",
				},
				"details": map[string]any{
					"type":        "string",
					"description": "Extended notes or context about this contact",
				},
				"facts": map[string]any{
					"type":                 "object",
					"description":          "Structured attributes as key-value pairs (e.g., {\"email\": \"alice@example.com\", \"phone\": \"555-1234\"})",
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
		Description: "Look up contacts from the directory. Search by name, query, kind, or structured attributes. With no arguments, returns directory statistics.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Exact name to look up (case-insensitive)",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "Search term to find matching contacts",
				},
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{"person", "company", "organization"},
					"description": "Filter by contact type",
				},
				"key": map[string]any{
					"type":        "string",
					"description": "Fact key to filter by (requires value)",
				},
				"value": map[string]any{
					"type":        "string",
					"description": "Fact value to match (requires key)",
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
					"enum":        []string{"person", "company", "organization"},
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
