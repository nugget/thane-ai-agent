package toolcatalog

import (
	"sort"
	"strings"
)

// CapabilityToolSource identifies where a tool comes from in a
// resolved capability tag — native catalog, an MCP bridge, or the
// operator overlay. Origin carries the concrete locator (server name
// for MCP, config path like "capability_tags.<tag>.include" for
// overlay). Native tools leave Origin empty since their declaration
// is recoverable from the catalog metadata.
type CapabilityToolSource struct {
	Kind   string `json:"kind"`
	Origin string `json:"origin,omitempty"`
}

// CapabilityToolState describes a non-active state for a tool. Active
// tools omit State entirely. Status grows over time as the catalog
// gains lifecycle states (excluded, deprecated, unhealthy, …); the
// struct shape lets new fields land without breaking the wire format.
type CapabilityToolState struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// CapabilityToolEntry is the rich per-tool view for a tag with full
// source attribution. Used by inspect_capability, the CLI, and the
// /api/capabilities endpoints.
type CapabilityToolEntry struct {
	Name   string               `json:"name"`
	Source CapabilityToolSource `json:"source"`
	State  *CapabilityToolState `json:"state,omitempty"`
}

// Tool source kinds used in CapabilityToolSource.Kind.
const (
	ToolSourceNative  = "native"
	ToolSourceMCP     = "mcp"
	ToolSourceOverlay = "overlay"
)

// Tool state statuses used in CapabilityToolState.Status.
const (
	ToolStateExcluded = "excluded"
)

// CapabilityContextSummary describes the optional context payload
// associated with a capability entry (KB articles and live context).
type CapabilityContextSummary struct {
	KBArticles int  `json:"kb_articles,omitempty"`
	Live       bool `json:"live,omitempty"`
}

// CapabilityCatalogEntry is the API/model-facing representation of one
// capability in the full catalog view, including status and tool list.
//
// ToolEntries carries per-tool source attribution (native, mcp, or
// overlay) so consumers can render where each tool comes from.
// ExcludedTools surfaces tools the operator overlay removed from this
// tag, populated only when the caller opts in (e.g.
// /api/capabilities?include=excluded).
type CapabilityCatalogEntry struct {
	Tag           string                    `json:"tag"`
	Status        string                    `json:"status"`
	Description   string                    `json:"description"`
	Teaser        string                    `json:"teaser,omitempty"`
	NextTags      []string                  `json:"next_tags,omitempty"`
	ToolCount     int                       `json:"tool_count,omitempty"`
	Tools         []string                  `json:"tools,omitempty"`
	ToolEntries   []CapabilityToolEntry     `json:"tool_entries,omitempty"`
	ExcludedTools []CapabilityToolEntry     `json:"excluded_tools,omitempty"`
	AlwaysActive  bool                      `json:"always_active,omitempty"`
	Protected     bool                      `json:"protected,omitempty"`
	AdHoc         bool                      `json:"ad_hoc,omitempty"`
	Context       *CapabilityContextSummary `json:"context,omitempty"`
}

// LoadedCapabilityEntry is the API/model-facing representation of one
// currently loaded (active) capability in the session.
type LoadedCapabilityEntry struct {
	Tag          string                    `json:"tag"`
	Description  string                    `json:"description,omitempty"`
	ToolCount    int                       `json:"tool_count,omitempty"`
	AlwaysActive bool                      `json:"always_active,omitempty"`
	Protected    bool                      `json:"protected,omitempty"`
	AdHoc        bool                      `json:"ad_hoc,omitempty"`
	Context      *CapabilityContextSummary `json:"context,omitempty"`
}

// CapabilityActionTools lists the tool names the model should use for
// capability lifecycle actions (activate, deactivate, reset, list,
// inspect).
type CapabilityActionTools struct {
	Activate   string `json:"activate"`
	Deactivate string `json:"deactivate"`
	Reset      string `json:"reset,omitempty"`
	List       string `json:"list,omitempty"`
	Inspect    string `json:"inspect,omitempty"`
	Delegate   string `json:"delegate,omitempty"`
}

// CapabilityCatalogView is the top-level JSON-serializable view of the
// full capability catalog including activation tool names.
type CapabilityCatalogView struct {
	Kind            string                   `json:"kind"`
	ActivationTools CapabilityActionTools    `json:"activation_tools"`
	Capabilities    []CapabilityCatalogEntry `json:"capabilities"`
}

// LoadedCapabilityView is the JSON-serializable view of the currently
// loaded capabilities in a session.
type LoadedCapabilityView struct {
	Kind               string                  `json:"kind"`
	LoadedCapabilities []LoadedCapabilityEntry `json:"loaded_capabilities"`
}

// CatalogViewOptions controls what optional sections appear in a
// generated CapabilityCatalogView. Defaults are conservative: only
// active tool members are surfaced. Callers opt in to nuances such as
// excluded tools via this struct.
type CatalogViewOptions struct {
	// IncludeDelegate adds the thane_delegate tool to the activation
	// tools block in the rendered view. Set false for surfaces where
	// delegation is not relevant.
	IncludeDelegate bool

	// IncludeExcluded surfaces operator-excluded tools per tag in
	// CapabilityCatalogEntry.ExcludedTools. Active tools are always
	// included regardless of this setting.
	IncludeExcluded bool
}

func defaultCapabilityActionTools(includeDelegate bool) CapabilityActionTools {
	tools := CapabilityActionTools{
		Activate:   "activate_capability",
		Deactivate: "deactivate_capability",
		Reset:      "reset_capabilities",
		List:       "list_loaded_capabilities",
		Inspect:    "inspect_capability",
	}
	if includeDelegate {
		tools.Delegate = "thane_delegate"
	}
	return tools
}

// BuildCapabilityCatalogView assembles the full capability catalog
// view from resolved surface entries, ready for JSON serialization.
func BuildCapabilityCatalogView(entries []CapabilitySurface, opts CatalogViewOptions) CapabilityCatalogView {
	view := CapabilityCatalogView{
		Kind:            "capability_catalog",
		ActivationTools: defaultCapabilityActionTools(opts.IncludeDelegate),
		Capabilities:    make([]CapabilityCatalogEntry, 0, len(entries)),
	}

	for _, entry := range SortCapabilitySurface(entries) {
		view.Capabilities = append(view.Capabilities, renderCatalogEntry(entry, opts))
	}

	return view
}

// RenderCapabilityCatalogEntry projects a single resolved surface entry
// into the JSON-tagged CapabilityCatalogEntry. Used by single-tag
// endpoints such as /api/capabilities/:tag.
func RenderCapabilityCatalogEntry(entry CapabilitySurface, opts CatalogViewOptions) CapabilityCatalogEntry {
	return renderCatalogEntry(entry, opts)
}

// renderCatalogEntry projects a CapabilitySurface into the JSON-tagged
// CapabilityCatalogEntry, optionally including operator-excluded tool
// entries based on opts.
func renderCatalogEntry(entry CapabilitySurface, opts CatalogViewOptions) CapabilityCatalogEntry {
	status := "available"
	switch {
	case entry.AdHoc:
		status = "discoverable"
	case entry.AlwaysActive:
		status = "always_active"
	case entry.Protected:
		status = "protected"
	}

	rendered := CapabilityCatalogEntry{
		Tag:          entry.Tag,
		Status:       status,
		Description:  capabilityDescription(entry),
		Teaser:       strings.TrimSpace(entry.Teaser),
		NextTags:     append([]string(nil), entry.NextTags...),
		ToolCount:    len(entry.Tools),
		Tools:        append([]string(nil), entry.Tools...),
		ToolEntries:  cloneToolEntries(entry.ToolEntries),
		AlwaysActive: entry.AlwaysActive,
		Protected:    entry.Protected,
		AdHoc:        entry.AdHoc,
	}
	if opts.IncludeExcluded {
		rendered.ExcludedTools = cloneToolEntries(entry.ExcludedTools)
	}
	if entry.KBArticles > 0 || entry.LiveContext {
		rendered.Context = &CapabilityContextSummary{
			KBArticles: entry.KBArticles,
			Live:       entry.LiveContext,
		}
	}
	return rendered
}

func cloneToolEntries(src []CapabilityToolEntry) []CapabilityToolEntry {
	if len(src) == 0 {
		return nil
	}
	out := make([]CapabilityToolEntry, len(src))
	for i, e := range src {
		out[i] = e
		if e.State != nil {
			s := *e.State
			out[i].State = &s
		}
	}
	return out
}

// BuildLoadedCapabilityEntries returns the loaded-capability entries for
// the given active tags, enriched with descriptions and context from the
// full surface.
func BuildLoadedCapabilityEntries(entries []CapabilitySurface, activeTags []string) []LoadedCapabilityEntry {
	if len(activeTags) == 0 {
		return []LoadedCapabilityEntry{}
	}

	byTag := make(map[string]CapabilitySurface, len(entries))
	for _, entry := range entries {
		byTag[entry.Tag] = entry
	}

	names := append([]string(nil), activeTags...)
	sort.Strings(names)

	loaded := make([]LoadedCapabilityEntry, 0, len(names))
	for _, tag := range names {
		entry, ok := byTag[tag]
		if !ok {
			loaded = append(loaded, LoadedCapabilityEntry{Tag: tag})
			continue
		}
		rendered := LoadedCapabilityEntry{
			Tag:          tag,
			Description:  capabilityDescription(entry),
			ToolCount:    len(entry.Tools),
			AlwaysActive: entry.AlwaysActive,
			Protected:    entry.Protected,
			AdHoc:        entry.AdHoc,
		}
		if entry.KBArticles > 0 || entry.LiveContext {
			rendered.Context = &CapabilityContextSummary{
				KBArticles: entry.KBArticles,
				Live:       entry.LiveContext,
			}
		}
		loaded = append(loaded, rendered)
	}

	return loaded
}

// BuildLoadedCapabilityView assembles the loaded-capability JSON view
// for the given active tags.
func BuildLoadedCapabilityView(entries []CapabilitySurface, activeTags []string, includeDelegate bool) LoadedCapabilityView {
	_ = includeDelegate
	return LoadedCapabilityView{
		Kind:               "loaded_capabilities",
		LoadedCapabilities: BuildLoadedCapabilityEntries(entries, activeTags),
	}
}
