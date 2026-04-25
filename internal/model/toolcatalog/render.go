package toolcatalog

import (
	"encoding/json"
	"fmt"
	"strings"
)

func selectCapabilityMenuEntries(entries []CapabilitySurface) []CapabilitySurface {
	menu := make([]CapabilitySurface, 0, len(entries))
	for _, entry := range SortCapabilitySurface(entries) {
		if entry.Menu {
			menu = append(menu, entry)
		}
	}
	if len(menu) > 0 {
		return menu
	}
	return SortCapabilitySurface(entries)
}

// RenderCapabilityActivationDescription renders the activate_capability
// tool help text from the shared capability surface.
func RenderCapabilityActivationDescription(entries []CapabilitySurface) string {
	actionTools := defaultCapabilityActionTools(true)
	var sb strings.Builder
	sb.WriteString("Activate a capability to load its tools and context into YOUR current conversation. ")
	sb.WriteString("This modifies your own runtime — it cannot be delegated. ")
	sb.WriteString(fmt.Sprintf("The only valid capability tools are `%s`, `%s`, `%s`, and `%s`; do not invent per-capability tool names. ",
		actionTools.Activate, actionTools.Deactivate, actionTools.Reset, actionTools.List))
	sb.WriteString(fmt.Sprintf("Delegates get capabilities via the tags parameter on `%s`.\n\n", actionTools.Delegate))
	sb.WriteString("Treat capability activation like a coarse-to-fine menu: start with one broad tag, read the newly loaded context, and only then decide whether to activate a narrower tag.\n\n")
	sb.WriteString("Capability menu:\n")

	for _, entry := range selectCapabilityMenuEntries(entries) {
		if entry.AlwaysActive {
			continue
		}
		desc := strings.TrimSpace(entry.Teaser)
		if desc == "" {
			desc = capabilityDescription(entry)
		}
		if entry.Protected {
			sb.WriteString(fmt.Sprintf("- **%s**: %s (%d tools; protected, trustworthy when present, not manually activatable)\n",
				entry.Tag, desc, len(entry.Tools)))
		} else {
			sb.WriteString(fmt.Sprintf("- **%s**: %s (%d tools)\n",
				entry.Tag, desc, len(entry.Tools)))
		}
		if len(entry.NextTags) > 0 {
			sb.WriteString(fmt.Sprintf("  next: %s\n", strings.Join(entry.NextTags, ", ")))
		}
	}

	sb.WriteString(fmt.Sprintf("\nUse %s to see which tags are currently loaded, %s to return to baseline, and %s when you only want to drop one specific tag.",
		actionTools.List, actionTools.Reset, actionTools.Deactivate))
	return sb.String()
}

// RenderCapabilityManifestMarkdown renders the model-facing capability
// menu as a heading plus a compact JSON payload.
func RenderCapabilityManifestMarkdown(entries []CapabilitySurface) string {
	if len(entries) == 0 {
		return ""
	}

	type capabilityMenuEntry struct {
		Status      string                    `json:"status"`
		Description string                    `json:"description"`
		Teaser      string                    `json:"teaser,omitempty"`
		NextTags    []string                  `json:"next_tags,omitempty"`
		ToolCount   int                       `json:"tool_count,omitempty"`
		Context     *CapabilityContextSummary `json:"context,omitempty"`
	}

	payload := struct {
		Kind            string                         `json:"kind"`
		ActivationTools CapabilityActionTools          `json:"activation_tools"`
		CapabilityMenu  map[string]capabilityMenuEntry `json:"capability_menu"`
	}{
		Kind:            "capability_menu",
		ActivationTools: defaultCapabilityActionTools(true),
		CapabilityMenu:  make(map[string]capabilityMenuEntry, len(entries)),
	}

	for _, rendered := range BuildCapabilityCatalogView(selectCapabilityMenuEntries(entries), true).Capabilities {
		payload.CapabilityMenu[rendered.Tag] = capabilityMenuEntry{
			Status:      rendered.Status,
			Description: rendered.Description,
			Teaser:      rendered.Teaser,
			NextTags:    append([]string(nil), rendered.NextTags...),
			ToolCount:   rendered.ToolCount,
			Context:     rendered.Context,
		}
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return "### Capability Menu\n\n{\"kind\":\"capability_menu\",\"error\":\"manifest marshal failed\"}"
	}

	var sb strings.Builder
	sb.WriteString("### Capability Menu\n\n")
	sb.Write(data)
	return sb.String()
}

func capabilityDescription(entry CapabilitySurface) string {
	if desc := strings.TrimSpace(entry.Description); desc != "" {
		return desc
	}
	if entry.AdHoc {
		parts := make([]string, 0, 2)
		if entry.KBArticles > 0 {
			parts = append(parts, fmt.Sprintf("%d tagged KB article(s)", entry.KBArticles))
		}
		if entry.LiveContext {
			parts = append(parts, "live context")
		}
		if len(parts) == 0 {
			return "Ad-hoc capability discovered from tagged context."
		}
		return "Ad-hoc capability with " + strings.Join(parts, " and ") + "."
	}
	return ""
}
