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
	AlwaysActive bool
}

// SetCapabilityTools adds request_capability and drop_capability tools
// to the registry. These tools let the agent dynamically activate or
// deactivate capability tags mid-session.
func (r *Registry) SetCapabilityTools(mgr CapabilityManager, manifest []CapabilityManifest) {
	r.registerRequestCapability(mgr, manifest)
	r.registerDropCapability(mgr)
}

// registerRequestCapability registers the request_capability tool.
func (r *Registry) registerRequestCapability(mgr CapabilityManager, manifest []CapabilityManifest) {
	// Build the available tags list for the description.
	var availableDesc strings.Builder
	availableDesc.WriteString("Activate a capability tag to gain access to additional tools. ")
	availableDesc.WriteString("Available capabilities:\n")
	for _, m := range manifest {
		if m.AlwaysActive {
			continue // Don't list always-active tags — they can't be toggled.
		}
		availableDesc.WriteString(fmt.Sprintf("- **%s**: %s (tools: %s)\n",
			m.Tag, m.Description, strings.Join(m.Tools, ", ")))
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

			return fmt.Sprintf("Capability **%s** activated. Tools for this tag are now available.", tag), nil
		},
	})
}

// registerDropCapability registers the drop_capability tool.
func (r *Registry) registerDropCapability(mgr CapabilityManager) {
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

			return fmt.Sprintf("Capability **%s** deactivated. Its tools are no longer available.", tag), nil
		},
	})
}

// BuildCapabilityManifest creates a sorted list of capability descriptions
// from the config map. This is used both for the tool description and for
// generating the capability manifest talent.
func BuildCapabilityManifest(tags map[string][]string, descriptions map[string]string, alwaysActive map[string]bool) []CapabilityManifest {
	manifest := make([]CapabilityManifest, 0, len(tags))
	for tag, toolNames := range tags {
		manifest = append(manifest, CapabilityManifest{
			Tag:          tag,
			Description:  descriptions[tag],
			Tools:        toolNames,
			AlwaysActive: alwaysActive[tag],
		})
	}
	sort.Slice(manifest, func(i, j int) bool {
		return manifest[i].Tag < manifest[j].Tag
	})
	return manifest
}
