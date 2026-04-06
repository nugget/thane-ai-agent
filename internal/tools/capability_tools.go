package tools

import (
	"context"
	"encoding/json"
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
	// ResetCapabilities drops all voluntary tags for the current Run,
	// returning the tags that were removed.
	ResetCapabilities(ctx context.Context) ([]string, error)
	// ActiveTags returns the set of currently active tags for the Run.
	ActiveTags(ctx context.Context) map[string]bool
}

// CapabilityManifest describes a capability tag for the manifest.
type CapabilityManifest = toolcatalog.CapabilitySurface

// SetCapabilityTools adds activate_capability, deactivate_capability,
// reset_capabilities, and list_loaded_capabilities tools to the
// registry. These tools let the agent inspect and mutate capability
// tags mid-conversation.
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
	r.registerResetCapabilities(mgr, tagManifest)
	r.registerListLoadedCapabilities(mgr, tagManifest)
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

const maxResetRemovedToolNames = 8

func summarizeRemovedTools(tags []string, tagManifest map[string]CapabilityManifest) string {
	if len(tags) == 0 {
		return ""
	}

	seen := make(map[string]struct{})
	var removedTools []string
	for _, tag := range tags {
		manifest, ok := tagManifest[tag]
		if !ok {
			continue
		}
		for _, tool := range manifest.Tools {
			if _, dup := seen[tool]; dup {
				continue
			}
			seen[tool] = struct{}{}
			removedTools = append(removedTools, tool)
		}
	}
	if len(removedTools) == 0 {
		return ""
	}

	sort.Strings(removedTools)
	if len(removedTools) <= maxResetRemovedToolNames {
		return fmt.Sprintf(" Tools removed: %s.", strings.Join(removedTools, ", "))
	}

	shown := removedTools[:maxResetRemovedToolNames]
	remaining := len(removedTools) - len(shown)
	return fmt.Sprintf(" Tools removed: %s, and %d more.", strings.Join(shown, ", "), remaining)
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
			"Always-active and protected tags cannot be deactivated. Use when you no longer need a capability's tools to keep your context focused.",
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

// registerResetCapabilities registers the reset_capabilities tool.
func (r *Registry) registerResetCapabilities(mgr CapabilityManager, tagManifest map[string]CapabilityManifest) {
	r.Register(&Tool{
		Name:            "reset_capabilities",
		AlwaysAvailable: true,
		Description: "Reset your current conversation back to baseline capability state by deactivating all voluntary tags at once. " +
			"Always-active, protected, and channel-pinned tags remain loaded. Use when the loop feels too widened or you want to return to the channel's default stance.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			dropped, err := mgr.ResetCapabilities(ctx)
			if err != nil {
				return "", err
			}

			active := mgr.ActiveTags(ctx)
			remaining := make([]string, 0, len(active))
			for tag, enabled := range active {
				if enabled {
					remaining = append(remaining, tag)
				}
			}
			sort.Strings(remaining)

			if len(dropped) == 0 {
				if len(remaining) == 0 {
					return "Capability state is already at baseline.", nil
				}
				return fmt.Sprintf("Capability state is already at baseline. Active: %s.", strings.Join(remaining, ", ")), nil
			}

			var result strings.Builder
			fmt.Fprintf(&result, "Capability state reset to baseline. Deactivated: %s.", strings.Join(dropped, ", "))
			if removedSummary := summarizeRemovedTools(dropped, tagManifest); removedSummary != "" {
				result.WriteString(removedSummary)
			}
			if len(remaining) > 0 {
				fmt.Fprintf(&result, " Active: %s.", strings.Join(remaining, ", "))
			}
			return result.String(), nil
		},
	})
}

// registerListLoadedCapabilities registers the list_loaded_capabilities tool.
func (r *Registry) registerListLoadedCapabilities(mgr CapabilityManager, tagManifest map[string]CapabilityManifest) {
	r.Register(&Tool{
		Name:            "list_loaded_capabilities",
		AlwaysAvailable: true,
		Description:     "List the capability tags currently loaded in YOUR current conversation runtime. Use when asked which capabilities or tags are active right now.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			active := mgr.ActiveTags(ctx)
			tags := make([]string, 0, len(active))
			for tag, enabled := range active {
				if enabled {
					tags = append(tags, tag)
				}
			}
			sort.Strings(tags)

			type loadedCapability struct {
				Tag          string `json:"tag"`
				AlwaysActive bool   `json:"always_active,omitempty"`
				Protected    bool   `json:"protected,omitempty"`
				AdHoc        bool   `json:"ad_hoc,omitempty"`
			}
			payload := struct {
				LoadedCapabilities []loadedCapability `json:"loaded_capabilities"`
			}{
				LoadedCapabilities: make([]loadedCapability, 0, len(tags)),
			}
			for _, tag := range tags {
				entry := loadedCapability{Tag: tag}
				if manifest, ok := tagManifest[tag]; ok {
					entry.AlwaysActive = manifest.AlwaysActive
					entry.Protected = manifest.Protected
					entry.AdHoc = manifest.AdHoc
				}
				payload.LoadedCapabilities = append(payload.LoadedCapabilities, entry)
			}
			out, err := json.Marshal(payload)
			if err != nil {
				return "", fmt.Errorf("marshal loaded capabilities: %w", err)
			}
			return string(out), nil
		},
	})
}

// BuildCapabilityManifest creates a sorted list of capability descriptions
// from the config map. This is used both for the tool description and for
// generating the capability manifest talent.
func BuildCapabilityManifest(tags map[string][]string, descriptions map[string]string, alwaysActive map[string]bool, protected map[string]bool) []CapabilityManifest {
	return toolcatalog.BuildCapabilitySurface(tags, descriptions, alwaysActive, protected)
}
