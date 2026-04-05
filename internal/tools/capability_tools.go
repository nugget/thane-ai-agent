package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/toolcatalog"
)

// CapabilityManager controls per-Run capability tag activation.
// Implemented by agent.Loop. All methods operate on the
// context-scoped capability scope created at the start of each Run().
type CapabilityManager interface {
	// RequestCapability activates a capability tag for the current Run.
	RequestCapability(ctx context.Context, tag string) error
	// DropCapability deactivates a capability tag for the current Run.
	DropCapability(ctx context.Context, tag string) error
	// ActiveTags returns the set of currently active tags for the Run.
	ActiveTags(ctx context.Context) map[string]bool
}

// CapabilityManifest describes a capability tag for the manifest.
type CapabilityManifest = toolcatalog.CapabilitySurface

// SetCapabilityTools adds activate_capability and deactivate_capability
// tools to the registry. These tools let the agent dynamically activate
// or deactivate capability tags mid-conversation.
//
// These tools are intentionally not assigned to any tag group. They
// live in the base registry and survive all tag filtering, ensuring the
// agent can always activate or deactivate capabilities regardless of
// which tags are currently active.
func (r *Registry) SetCapabilityTools(mgr CapabilityManager, manifest []CapabilityManifest) {
	// Index manifest by tag for fast lookup in handlers.
	tagManifest := make(map[string]CapabilityManifest, len(manifest))
	for _, m := range manifest {
		tagManifest[m.Tag] = m
	}
	r.registerActivateCapability(mgr, manifest, tagManifest)
	r.registerDeactivateCapability(mgr, tagManifest)
}

// extractTag extracts the tag parameter from args, accepting common
// misnames ("capability", "name") as aliases for "tag".
func extractTag(args map[string]any) string {
	if tag, ok := args["tag"].(string); ok {
		if t := strings.TrimSpace(tag); t != "" {
			return t
		}
	}
	if v, ok := args["capability"].(string); ok {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	if v, ok := args["name"].(string); ok {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// registerActivateCapability registers the activate_capability tool.
func (r *Registry) registerActivateCapability(mgr CapabilityManager, manifest []CapabilityManifest, tagManifest map[string]CapabilityManifest) {
	r.Register(&Tool{
		Name:            "activate_capability",
		AlwaysAvailable: true,
		Description:     toolcatalog.RenderCapabilityActivationDescription(manifest),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tag": map[string]any{
					"type":        "string",
					"description": "The capability tag to activate (e.g., \"forge\", \"ha\", \"email\")",
				},
			},
			"required": []string{"tag"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			tag := extractTag(args)
			if tag == "" {
				return "", fmt.Errorf("tag is required (e.g., activate_capability(tag: \"forge\"))")
			}

			if err := mgr.RequestCapability(ctx, tag); err != nil {
				return "", err
			}

			var result strings.Builder
			fmt.Fprintf(&result, "Capability **%s** activated.", tag)
			if m, ok := tagManifest[tag]; ok {
				if len(m.Tools) > 0 {
					fmt.Fprintf(&result, " %d tools now available.", len(m.Tools))
				}
			} else {
				result.WriteString(" Ad-hoc tag — tagged KB articles, talents, and live providers matching this tag will be loaded.")
			}
			return result.String(), nil
		},
	})
}

// registerDeactivateCapability registers the deactivate_capability tool.
func (r *Registry) registerDeactivateCapability(mgr CapabilityManager, tagManifest map[string]CapabilityManifest) {
	r.Register(&Tool{
		Name:            "deactivate_capability",
		AlwaysAvailable: true,
		Description: "Deactivate a capability to remove its tools and context from YOUR current conversation. " +
			"Always-active tags cannot be deactivated. Use when you no longer need a capability's tools to keep your context focused.",
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
			tag := extractTag(args)
			if tag == "" {
				return "", fmt.Errorf("tag is required (e.g., deactivate_capability(tag: \"forge\"))")
			}

			if err := mgr.DropCapability(ctx, tag); err != nil {
				return "", err
			}

			var result strings.Builder
			fmt.Fprintf(&result, "Capability **%s** deactivated.", tag)
			if m, ok := tagManifest[tag]; ok && len(m.Tools) > 0 {
				fmt.Fprintf(&result, " %d tools removed.", len(m.Tools))
			}
			if remaining := mgr.ActiveTags(ctx); len(remaining) > 0 {
				tags := make([]string, 0, len(remaining))
				for t := range remaining {
					tags = append(tags, t)
				}
				sort.Strings(tags)
				fmt.Fprintf(&result, " Active: %s.", strings.Join(tags, ", "))
			}
			return result.String(), nil
		},
	})
}

// BuildCapabilityManifest creates a sorted list of capability descriptions
// from the config map. This is used both for the tool description and for
// generating the capability manifest talent.
func BuildCapabilityManifest(tags map[string][]string, descriptions map[string]string, alwaysActive map[string]bool) []CapabilityManifest {
	return toolcatalog.BuildCapabilitySurface(tags, descriptions, alwaysActive)
}
