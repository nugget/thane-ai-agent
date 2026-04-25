package tools

import (
	"context"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

func registerDocumentLifecycleTools(r *Registry, dt *documents.Tools) {
	r.Register(&Tool{
		Name:                 "doc_delete",
		Description:          "Delete one managed markdown document by semantic ref like `kb:article.md`. Use when a document should leave the managed corpus entirely; the tool removes the file and updates the document index for you.",
		ContentResolveExempt: []string{"ref"},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ref": map[string]any{
					"type":        "string",
					"description": "Canonical document ref to remove, like `kb:old-notes.md`.",
				},
			},
			"required": []string{"ref"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			ref, _ := args["ref"].(string)
			if ref == "" {
				return "", fmt.Errorf("ref is required")
			}
			return dt.Delete(ctx, documents.DeleteArgs{Ref: ref})
		},
	})

	r.Register(&Tool{
		Name:                 "doc_move",
		Description:          "Move or rename a managed markdown document to a new semantic ref. Use when the document should live under a new root or path without dropping down to raw file operations.",
		ContentResolveExempt: []string{"ref", "destination_ref", "overwrite"},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ref": map[string]any{
					"type":        "string",
					"description": "Current canonical document ref, like `kb:notes/idea.md`.",
				},
				"destination_ref": map[string]any{
					"type":        "string",
					"description": "New semantic ref for the document, like `kb:ideas/idea.md` or `dossiers:people/alice.md`.",
				},
				"overwrite": map[string]any{
					"type":        "boolean",
					"description": "Set true only when an existing document at the destination should be replaced.",
				},
			},
			"required": []string{"ref", "destination_ref"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			ref, _ := args["ref"].(string)
			destinationRef, _ := args["destination_ref"].(string)
			if ref == "" {
				return "", fmt.Errorf("ref is required")
			}
			if destinationRef == "" {
				return "", fmt.Errorf("destination_ref is required")
			}
			overwrite, _ := args["overwrite"].(bool)
			return dt.Move(ctx, documents.MoveArgs{
				Ref:            ref,
				DestinationRef: destinationRef,
				Overwrite:      overwrite,
			})
		},
	})

	r.Register(&Tool{
		Name:                 "doc_copy",
		Description:          "Copy one managed markdown document to a new semantic ref while keeping the source intact. Use for branching, templating, or creating a variant without leaving the document abstraction.",
		ContentResolveExempt: []string{"ref", "destination_ref", "overwrite"},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ref": map[string]any{
					"type":        "string",
					"description": "Current canonical document ref, like `kb:notes/idea.md`.",
				},
				"destination_ref": map[string]any{
					"type":        "string",
					"description": "New semantic ref for the copied document.",
				},
				"overwrite": map[string]any{
					"type":        "boolean",
					"description": "Set true only when an existing document at the destination should be replaced.",
				},
			},
			"required": []string{"ref", "destination_ref"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			ref, _ := args["ref"].(string)
			destinationRef, _ := args["destination_ref"].(string)
			if ref == "" {
				return "", fmt.Errorf("ref is required")
			}
			if destinationRef == "" {
				return "", fmt.Errorf("destination_ref is required")
			}
			overwrite, _ := args["overwrite"].(bool)
			return dt.Copy(ctx, documents.CopyArgs{
				Ref:            ref,
				DestinationRef: destinationRef,
				Overwrite:      overwrite,
			})
		},
	})

	r.Register(&Tool{
		Name:                 "doc_copy_section",
		Description:          "Copy one named section from a source managed document into a destination managed document. The destination section is upserted, so this is a good fit for reorganizing or curating knowledge without losing the source.",
		ContentResolveExempt: []string{"ref", "section", "destination_ref", "destination_section", "destination_level"},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ref": map[string]any{
					"type":        "string",
					"description": "Source document ref containing the section to copy.",
				},
				"section": map[string]any{
					"type":        "string",
					"description": "Heading text or slug of the source section to copy.",
				},
				"destination_ref": map[string]any{
					"type":        "string",
					"description": "Destination document ref. The document is created if it does not exist yet.",
				},
				"destination_section": map[string]any{
					"type":        "string",
					"description": "Optional destination section heading/slug. Defaults to the source section heading.",
				},
				"destination_level": map[string]any{
					"type":        "integer",
					"description": "Optional heading level for the destination section. Defaults to the source section level.",
				},
			},
			"required": []string{"ref", "section", "destination_ref"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			ref, _ := args["ref"].(string)
			section, _ := args["section"].(string)
			destinationRef, _ := args["destination_ref"].(string)
			if ref == "" {
				return "", fmt.Errorf("ref is required")
			}
			if section == "" {
				return "", fmt.Errorf("section is required")
			}
			if destinationRef == "" {
				return "", fmt.Errorf("destination_ref is required")
			}
			destinationSection, _ := args["destination_section"].(string)
			return dt.CopySection(ctx, documents.SectionTransferArgs{
				Ref:                ref,
				Section:            section,
				DestinationRef:     destinationRef,
				DestinationSection: destinationSection,
				DestinationLevel:   numericArg(args["destination_level"], 0, 6),
			})
		},
	})

	r.Register(&Tool{
		Name:                 "doc_move_section",
		Description:          "Move one named section from a source managed document into a destination managed document. This copies the section into the destination and then removes it from the source, so it is ideal for refactoring a corpus without falling back to raw file edits.",
		ContentResolveExempt: []string{"ref", "section", "destination_ref", "destination_section", "destination_level"},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ref": map[string]any{
					"type":        "string",
					"description": "Source document ref containing the section to move.",
				},
				"section": map[string]any{
					"type":        "string",
					"description": "Heading text or slug of the source section to move.",
				},
				"destination_ref": map[string]any{
					"type":        "string",
					"description": "Destination document ref. The document is created if it does not exist yet.",
				},
				"destination_section": map[string]any{
					"type":        "string",
					"description": "Optional destination section heading/slug. Defaults to the source section heading.",
				},
				"destination_level": map[string]any{
					"type":        "integer",
					"description": "Optional heading level for the destination section. Defaults to the source section level.",
				},
			},
			"required": []string{"ref", "section", "destination_ref"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			ref, _ := args["ref"].(string)
			section, _ := args["section"].(string)
			destinationRef, _ := args["destination_ref"].(string)
			if ref == "" {
				return "", fmt.Errorf("ref is required")
			}
			if section == "" {
				return "", fmt.Errorf("section is required")
			}
			if destinationRef == "" {
				return "", fmt.Errorf("destination_ref is required")
			}
			destinationSection, _ := args["destination_section"].(string)
			return dt.MoveSection(ctx, documents.SectionTransferArgs{
				Ref:                ref,
				Section:            section,
				DestinationRef:     destinationRef,
				DestinationSection: destinationSection,
				DestinationLevel:   numericArg(args["destination_level"], 0, 6),
			})
		},
	})
}
