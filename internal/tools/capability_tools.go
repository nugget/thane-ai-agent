package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
)

// CapabilityManager controls per-Run tag activation. Implemented by
// agent.Loop. All methods operate on the context-scoped tag scope
// created at the start of each Run().
//
// The "Capability" name on this interface is internal-implementation
// terminology that predates the model-facing rename to "tag"; the
// model surface (tool names, descriptions, JSON) speaks "tag"
// throughout. An internal-only rename is a separate cleanup.
type CapabilityManager interface {
	// RequestCapability activates a tag for the current Run.
	RequestCapability(ctx context.Context, tag string) error
	// DropCapability deactivates a tag for the current Run.
	DropCapability(ctx context.Context, tag string) error
	// ResetCapabilities drops all voluntary tags for the current Run,
	// returning the tags that were removed.
	ResetCapabilities(ctx context.Context) ([]string, error)
	// ActiveTags returns the set of currently active tags for the Run.
	ActiveTags(ctx context.Context) map[string]bool
}

// CapabilityManifest describes a tag for the manifest.
type CapabilityManifest = toolcatalog.CapabilitySurface

// SetCapabilityTools adds tag_activate, tag_deactivate, tag_reset,
// and tag_inspect tools to the registry. These tools let the agent
// inspect and mutate which tags are loaded mid-conversation. For
// "what's currently loaded," the model reads the `## Active Tags`
// section already rendered into every prompt — no tool call needed.
//
// These tools are intentionally not assigned to any tag group. They
// live in the base registry and survive all tag filtering, ensuring
// the agent can always change tag activation regardless of which
// tags are currently active.
func (r *Registry) SetCapabilityTools(mgr CapabilityManager, manifest []CapabilityManifest) {
	// Index manifest by tag for fast lookup in handlers.
	tagManifest := make(map[string]CapabilityManifest, len(manifest))
	for _, m := range manifest {
		tagManifest[m.Tag] = m
	}
	r.registerActivateTag(mgr, manifest, tagManifest)
	r.registerDeactivateTag(mgr, tagManifest)
	r.registerResetTags(mgr, tagManifest)
	r.registerInspectTag(tagManifest)
}

// extractTag extracts the tag parameter from args, accepting common
// misnames ("capability", "name") as aliases for "tag". The
// "capability" alias survives the rename because older models or
// transcripts may still pass it; it's cheap defensive code and does
// not appear in tool descriptions.
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

// registerActivateTag registers the tag_activate tool.
//
// Core-tool rationale: this is the bootstrap primitive for opening
// tag scopes. If it required a tag to be loaded first, there would
// be no way to widen the model's surface from any starting state —
// a chicken-and-egg that would leave a tightly scoped loop unable
// to ever ask for more.
func (r *Registry) registerActivateTag(mgr CapabilityManager, manifest []CapabilityManifest, tagManifest map[string]CapabilityManifest) {
	r.Register(&Tool{
		Name:        "tag_activate",
		Core:        true,
		Description: toolcatalog.RenderCapabilityActivationDescription(manifest),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tag": map[string]any{
					"type":        "string",
					"description": "The tag to activate (e.g., \"forge\", \"ha\", \"email\")",
				},
			},
			"required": []string{"tag"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			rawTag := extractTag(args)
			if rawTag == "" {
				return "", fmt.Errorf("tag is required (e.g., tag_activate(tag: \"forge\"))")
			}
			// Resolve aliases before activation so the canonical name is
			// what flows through the scope, the prompt's ## Active Tags
			// section, and the persistence layer.
			tag := toolcatalog.CanonicalTagName(rawTag)

			if err := mgr.RequestCapability(ctx, tag); err != nil {
				return "", err
			}

			var result strings.Builder
			if tag != rawTag {
				fmt.Fprintf(&result, "Tag **%s** activated (alias for **%s**).", rawTag, tag)
			} else {
				fmt.Fprintf(&result, "Tag **%s** activated.", tag)
			}
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

// registerDeactivateTag registers the tag_deactivate tool.
//
// Core-tool rationale: symmetric counterpart to tag_activate. A loop
// that widened its surface for one phase of work needs to be able
// to narrow back without keeping a tag loaded just for the release
// primitive. Locking this behind a tag would create stuck-wide
// states.
func (r *Registry) registerDeactivateTag(mgr CapabilityManager, tagManifest map[string]CapabilityManifest) {
	r.Register(&Tool{
		Name: "tag_deactivate",
		Core: true,
		Description: "Deactivate a tag to remove its tools and context from YOUR current conversation. " +
			"Core and protected tags cannot be deactivated. Use when you no longer need a tag's tools to keep your context focused.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tag": map[string]any{
					"type":        "string",
					"description": "The tag to deactivate",
				},
			},
			"required": []string{"tag"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			rawTag := extractTag(args)
			if rawTag == "" {
				return "", fmt.Errorf("tag is required (e.g., tag_deactivate(tag: \"forge\"))")
			}
			// Resolve aliases so deactivating by an alternate name
			// (e.g. `homeassistant`) hits the canonical tag in scope.
			tag := toolcatalog.CanonicalTagName(rawTag)

			if err := mgr.DropCapability(ctx, tag); err != nil {
				return "", err
			}

			var result strings.Builder
			if tag != rawTag {
				fmt.Fprintf(&result, "Tag **%s** deactivated (alias for **%s**).", rawTag, tag)
			} else {
				fmt.Fprintf(&result, "Tag **%s** deactivated.", tag)
			}
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

// registerResetTags registers the tag_reset tool.
//
// Core-tool rationale: the emergency hatch for returning to baseline
// when the loop has accumulated voluntary tags it no longer needs.
// Same bootstrap argument as activate/deactivate — must work from
// any state, including states where the model intentionally dropped
// most of its surface.
func (r *Registry) registerResetTags(mgr CapabilityManager, tagManifest map[string]CapabilityManifest) {
	r.Register(&Tool{
		Name: "tag_reset",
		Core: true,
		Description: "Reset your current conversation back to baseline tag state by deactivating all voluntary tags at once. " +
			"Core, protected, and channel-pinned tags remain loaded. Use when the loop feels too widened or you want to return to the channel's default stance.",
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
					return "Tag state is already at baseline.", nil
				}
				return fmt.Sprintf("Tag state is already at baseline. Active: %s.", strings.Join(remaining, ", ")), nil
			}

			var result strings.Builder
			fmt.Fprintf(&result, "Tag state reset to baseline. Deactivated: %s.", strings.Join(dropped, ", "))
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

// registerInspectTag registers the tag_inspect tool, which returns
// the full per-tool breakdown of a single tag — description, status,
// active tools with their source attribution (native / mcp /
// overlay), and optionally operator-excluded tools. Use this to
// audit "where did this tool come from" or "what's actually in the
// ha tag at this site".
//
// Core-tool rationale: auditing a tag must work *before* the tag is
// activated. The whole point of inspection is to decide whether
// opening the tag is worth the surface cost, which means the answer
// can't depend on the tag already being in scope.
func (r *Registry) registerInspectTag(tagManifest map[string]CapabilityManifest) {
	r.Register(&Tool{
		Name:        "tag_inspect",
		Core:        true,
		Description: "Inspect a single tag and return a structured breakdown of its tools with source attribution (native, mcp, overlay). Use to audit what a tag exposes and where each tool came from. Pass include_excluded: true to also surface operator-disabled tools.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tag": map[string]any{
					"type":        "string",
					"description": "The tag to inspect (e.g., \"ha\", \"forge\").",
				},
				"include_excluded": map[string]any{
					"type":        "boolean",
					"description": "When true, the response includes tools the operator has disabled at this site.",
				},
			},
			"required": []string{"tag"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			rawTag := extractTag(args)
			if rawTag == "" {
				return "", fmt.Errorf("tag is required (e.g., tag_inspect(tag: \"ha\"))")
			}
			// Resolve aliases so tag_inspect("homeassistant") returns
			// ha's breakdown.
			tag := toolcatalog.CanonicalTagName(rawTag)
			manifest, ok := tagManifest[tag]
			if !ok {
				return "", fmt.Errorf("unknown tag %q; read the ## Active Tags section of your prompt to see what's already loaded, or use tag_activate to discover available tags", rawTag)
			}
			includeExcluded, _ := args["include_excluded"].(bool)
			entry := toolcatalog.RenderCapabilityCatalogEntry(manifest, toolcatalog.CatalogViewOptions{
				IncludeExcluded: includeExcluded,
			})
			out, err := json.Marshal(entry)
			if err != nil {
				return "", fmt.Errorf("marshal tag inspection: %w", err)
			}
			return string(out), nil
		},
	})
}

// BuildCapabilityManifest creates a sorted list of tag descriptions
// from the config map. This is used both for the tool description and
// for generating the tag manifest talent.
func BuildCapabilityManifest(tags map[string][]string, descriptions map[string]string, core map[string]bool, protected map[string]bool) []CapabilityManifest {
	return toolcatalog.BuildCapabilitySurface(tags, descriptions, core, protected)
}
