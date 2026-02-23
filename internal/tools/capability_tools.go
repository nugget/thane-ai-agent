package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// CapabilityManager controls per-session capability tag activation.
// Implemented by agent.Loop.
type CapabilityManager interface {
	// RequestCapability activates a capability tag for the session.
	RequestCapability(tag string) error
	// DropCapability deactivates a capability tag for the session.
	DropCapability(tag string) error
	// ActiveTags returns the set of currently active tags.
	ActiveTags() map[string]bool
}

// CapabilityManifest describes a capability tag for the manifest.
type CapabilityManifest struct {
	Tag          string
	Description  string
	Tools        []string
	Context      []string // resolved context file paths
	AlwaysActive bool
}

// SetCapabilityTools adds request_capability and drop_capability tools
// to the registry. These tools let the agent dynamically activate or
// deactivate capability tags mid-session.
//
// These tools are intentionally not assigned to any tag group. They
// live in the base registry and survive all tag filtering, ensuring the
// agent can always request or shed capabilities regardless of which
// tags are currently active.
func (r *Registry) SetCapabilityTools(mgr CapabilityManager, manifest []CapabilityManifest) {
	// Index manifest by tag for fast lookup in handlers.
	tagManifest := make(map[string]CapabilityManifest, len(manifest))
	for _, m := range manifest {
		tagManifest[m.Tag] = m
	}
	r.registerRequestCapability(mgr, manifest, tagManifest)
	r.registerDropCapability(mgr, tagManifest)
}

// registerRequestCapability registers the request_capability tool.
func (r *Registry) registerRequestCapability(mgr CapabilityManager, manifest []CapabilityManifest, tagManifest map[string]CapabilityManifest) {
	// Build the available tags list for the description.
	var availableDesc strings.Builder
	availableDesc.WriteString("Activate a capability tag to gain access to additional tools. ")
	availableDesc.WriteString("Available capabilities:\n")
	for _, m := range manifest {
		if m.AlwaysActive {
			continue // Don't list always-active tags — they can't be toggled.
		}
		if len(m.Context) > 0 {
			availableDesc.WriteString(fmt.Sprintf("- **%s**: %s (tools: %s, context: %d files)\n",
				m.Tag, m.Description, strings.Join(m.Tools, ", "), len(m.Context)))
		} else {
			availableDesc.WriteString(fmt.Sprintf("- **%s**: %s (tools: %s)\n",
				m.Tag, m.Description, strings.Join(m.Tools, ", ")))
		}
	}
	availableDesc.WriteString("Use drop_capability to deactivate a tag when you no longer need those tools.")

	r.Register(&Tool{
		Name:        "request_capability",
		Description: availableDesc.String(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tag": map[string]any{
					"type":        "string",
					"description": "The capability tag to activate",
				},
			},
			"required": []string{"tag"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tag, _ := args["tag"].(string)
			if tag == "" {
				return "", fmt.Errorf("tag is required")
			}

			if err := mgr.RequestCapability(tag); err != nil {
				return "", err
			}

			// List the tools and context now available from this tag.
			var result strings.Builder
			fmt.Fprintf(&result, "Capability **%s** activated.", tag)
			if m, ok := tagManifest[tag]; ok {
				if len(m.Tools) > 0 {
					fmt.Fprintf(&result, " Tools now available: %s.", strings.Join(m.Tools, ", "))
				}
				if len(m.Context) > 0 {
					fmt.Fprintf(&result, " Context loaded: %d files.", len(m.Context))
				}
			}
			return result.String(), nil
		},
	})
}

// registerDropCapability registers the drop_capability tool.
func (r *Registry) registerDropCapability(mgr CapabilityManager, tagManifest map[string]CapabilityManifest) {
	r.Register(&Tool{
		Name:        "drop_capability",
		Description: "Deactivate a capability tag to remove its tools from the active set. Always-active tags cannot be dropped. Use when you no longer need a capability's tools to keep the tool set focused.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tag": map[string]any{
					"type":        "string",
					"description": "The capability tag to deactivate",
				},
			},
			"required": []string{"tag"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tag, _ := args["tag"].(string)
			if tag == "" {
				return "", fmt.Errorf("tag is required")
			}

			if err := mgr.DropCapability(tag); err != nil {
				return "", err
			}

			// List the tools that were unloaded.
			var result strings.Builder
			fmt.Fprintf(&result, "Capability **%s** deactivated.", tag)
			if m, ok := tagManifest[tag]; ok && len(m.Tools) > 0 {
				fmt.Fprintf(&result, " Tools removed: %s.", strings.Join(m.Tools, ", "))
			}
			return result.String(), nil
		},
	})
}

// BuildCapabilityManifest creates a sorted list of capability descriptions
// from the config map. This is used both for the tool description and for
// generating the capability manifest talent. The contextFiles parameter
// maps tag names to resolved context file paths (may be nil).
func BuildCapabilityManifest(tags map[string][]string, descriptions map[string]string, alwaysActive map[string]bool, contextFiles map[string][]string) []CapabilityManifest {
	manifest := make([]CapabilityManifest, 0, len(tags))
	for tag, toolNames := range tags {
		manifest = append(manifest, CapabilityManifest{
			Tag:          tag,
			Description:  descriptions[tag],
			Tools:        toolNames,
			Context:      contextFiles[tag],
			AlwaysActive: alwaysActive[tag],
		})
	}
	sort.Slice(manifest, func(i, j int) bool {
		return manifest[i].Tag < manifest[j].Tag
	})
	return manifest
}
