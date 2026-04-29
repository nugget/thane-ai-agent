// Package toolcatalog provides compiled-in metadata for tools and capability
// tags, and renders capability surface descriptions for model-facing context
// and the web dashboard.
package toolcatalog

import (
	"encoding/json"
	"maps"
	"sort"
	"strings"
)

// ToolSource identifies where a tool originates.
type ToolSource string

const (
	NativeToolSource ToolSource = "native"
	MCPToolSource    ToolSource = "mcp"
)

// BuiltinToolSpec captures compiled-in metadata for a tool.
type BuiltinToolSpec struct {
	CanonicalID string
	Source      ToolSource
	Tags        []string
}

// BuiltinTagSpec captures compiled-in metadata for a tag/toolset.
type BuiltinTagSpec struct {
	Description  string
	AlwaysActive bool
	Menu         bool
	Protected    bool
}

// CapabilitySurface captures the resolved model-facing view of a
// capability/toolset. It is intentionally transport-agnostic so
// prompt renderers, tool help text, and future caching/freshness
// policies can all work from the same semantic shape.
//
// Tools is the flat sorted list of active tool names — the canonical
// runtime membership consumed by tag filtering. ToolEntries carries
// the same active tools enriched with source attribution for views
// that need to explain where each tool came from. ExcludedTools
// surfaces tools the operator overlay removed from this tag, used by
// API consumers that opt into excluded entries.
type CapabilitySurface struct {
	Tag           string
	Description   string
	Teaser        string
	NextTags      []string
	Tools         []string
	ToolEntries   []CapabilityToolEntry
	ExcludedTools []CapabilityToolEntry
	AlwaysActive  bool
	Menu          bool
	Protected     bool
	Loaded        bool
	KBArticles    int
	LiveContext   bool
	AdHoc         bool
}

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

// RenderCapabilityCatalogEntry projects a single resolved surface entry
// into the JSON-tagged CapabilityCatalogEntry. Used by single-tag
// endpoints such as /api/capabilities/:tag.
func RenderCapabilityCatalogEntry(entry CapabilitySurface, opts CatalogViewOptions) CapabilityCatalogEntry {
	return renderCatalogEntry(entry, opts)
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

var builtinToolSpecs = map[string]BuiltinToolSpec{
	"archive_range":               {CanonicalID: "native:archive_range", Source: NativeToolSource, Tags: []string{"archive"}},
	"archive_search":              {CanonicalID: "native:archive_search", Source: NativeToolSource, Tags: []string{"archive"}},
	"archive_session_transcript":  {CanonicalID: "native:archive_session_transcript", Source: NativeToolSource, Tags: []string{"archive"}},
	"archive_sessions":            {CanonicalID: "native:archive_sessions", Source: NativeToolSource, Tags: []string{"archive"}},
	"attachment_describe":         {CanonicalID: "native:attachment_describe", Source: NativeToolSource, Tags: []string{"attachments"}},
	"attachment_list":             {CanonicalID: "native:attachment_list", Source: NativeToolSource, Tags: []string{"attachments"}},
	"attachment_search":           {CanonicalID: "native:attachment_search", Source: NativeToolSource, Tags: []string{"attachments"}},
	"call_service":                {CanonicalID: "native:call_service", Source: NativeToolSource, Tags: []string{"ha", "homeassistant"}},
	"cancel_task":                 {CanonicalID: "native:cancel_task", Source: NativeToolSource, Tags: []string{"scheduler"}},
	"control_device":              {CanonicalID: "native:control_device", Source: NativeToolSource, Tags: []string{"ha", "homeassistant"}},
	"conversation_reset":          {CanonicalID: "native:conversation_reset", Source: NativeToolSource, Tags: []string{"session"}},
	"cost_summary":                {CanonicalID: "native:cost_summary", Source: NativeToolSource, Tags: []string{"diagnostics"}},
	"create_temp_file":            {CanonicalID: "native:create_temp_file", Source: NativeToolSource, Tags: []string{"files"}},
	"doc_browse":                  {CanonicalID: "native:doc_browse", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_commit":                  {CanonicalID: "native:doc_commit", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_copy":                    {CanonicalID: "native:doc_copy", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_copy_section":            {CanonicalID: "native:doc_copy_section", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_delete":                  {CanonicalID: "native:doc_delete", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_edit":                    {CanonicalID: "native:doc_edit", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_intake":                  {CanonicalID: "native:doc_intake", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_journal_update":          {CanonicalID: "native:doc_journal_update", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_links":                   {CanonicalID: "native:doc_links", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_move":                    {CanonicalID: "native:doc_move", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_move_section":            {CanonicalID: "native:doc_move_section", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_outline":                 {CanonicalID: "native:doc_outline", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_read":                    {CanonicalID: "native:doc_read", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_roots":                   {CanonicalID: "native:doc_roots", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_search":                  {CanonicalID: "native:doc_search", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_section":                 {CanonicalID: "native:doc_section", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_values":                  {CanonicalID: "native:doc_values", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_write":                   {CanonicalID: "native:doc_write", Source: NativeToolSource, Tags: []string{"documents"}},
	"email_folders":               {CanonicalID: "native:email_folders", Source: NativeToolSource, Tags: []string{"email"}},
	"email_list":                  {CanonicalID: "native:email_list", Source: NativeToolSource, Tags: []string{"email"}},
	"email_mark":                  {CanonicalID: "native:email_mark", Source: NativeToolSource, Tags: []string{"email"}},
	"email_move":                  {CanonicalID: "native:email_move", Source: NativeToolSource, Tags: []string{"email"}},
	"email_read":                  {CanonicalID: "native:email_read", Source: NativeToolSource, Tags: []string{"email"}},
	"email_reply":                 {CanonicalID: "native:email_reply", Source: NativeToolSource, Tags: []string{"email"}},
	"email_search":                {CanonicalID: "native:email_search", Source: NativeToolSource, Tags: []string{"email"}},
	"email_send":                  {CanonicalID: "native:email_send", Source: NativeToolSource, Tags: []string{"email"}},
	"exec":                        {CanonicalID: "native:exec", Source: NativeToolSource, Tags: []string{"shell"}},
	"export_all_vcf":              {CanonicalID: "native:export_all_vcf", Source: NativeToolSource, Tags: []string{"contacts"}},
	"export_vcf":                  {CanonicalID: "native:export_vcf", Source: NativeToolSource, Tags: []string{"contacts"}},
	"export_vcf_qr":               {CanonicalID: "native:export_vcf_qr", Source: NativeToolSource, Tags: []string{"contacts"}},
	"file_edit":                   {CanonicalID: "native:file_edit", Source: NativeToolSource, Tags: []string{"files"}},
	"file_grep":                   {CanonicalID: "native:file_grep", Source: NativeToolSource, Tags: []string{"files"}},
	"file_list":                   {CanonicalID: "native:file_list", Source: NativeToolSource, Tags: []string{"files"}},
	"file_read":                   {CanonicalID: "native:file_read", Source: NativeToolSource, Tags: []string{"files"}},
	"file_search":                 {CanonicalID: "native:file_search", Source: NativeToolSource, Tags: []string{"files"}},
	"file_stat":                   {CanonicalID: "native:file_stat", Source: NativeToolSource, Tags: []string{"files"}},
	"file_tree":                   {CanonicalID: "native:file_tree", Source: NativeToolSource, Tags: []string{"files"}},
	"file_write":                  {CanonicalID: "native:file_write", Source: NativeToolSource, Tags: []string{"files"}},
	"find_entity":                 {CanonicalID: "native:find_entity", Source: NativeToolSource, Tags: []string{"ha", "homeassistant"}},
	"forget_contact":              {CanonicalID: "native:forget_contact", Source: NativeToolSource, Tags: []string{"contacts"}},
	"forget_fact":                 {CanonicalID: "native:forget_fact", Source: NativeToolSource, Tags: []string{"memory"}},
	"forge_issue_comment":         {CanonicalID: "native:forge_issue_comment", Source: NativeToolSource, Tags: []string{"forge"}},
	"forge_issue_create":          {CanonicalID: "native:forge_issue_create", Source: NativeToolSource, Tags: []string{"forge"}},
	"forge_issue_get":             {CanonicalID: "native:forge_issue_get", Source: NativeToolSource, Tags: []string{"forge"}},
	"forge_issue_list":            {CanonicalID: "native:forge_issue_list", Source: NativeToolSource, Tags: []string{"forge"}},
	"forge_issue_update":          {CanonicalID: "native:forge_issue_update", Source: NativeToolSource, Tags: []string{"forge"}},
	"forge_pr_checks":             {CanonicalID: "native:forge_pr_checks", Source: NativeToolSource, Tags: []string{"forge"}},
	"forge_pr_commits":            {CanonicalID: "native:forge_pr_commits", Source: NativeToolSource, Tags: []string{"forge"}},
	"forge_pr_diff":               {CanonicalID: "native:forge_pr_diff", Source: NativeToolSource, Tags: []string{"forge"}},
	"forge_pr_files":              {CanonicalID: "native:forge_pr_files", Source: NativeToolSource, Tags: []string{"forge"}},
	"forge_pr_get":                {CanonicalID: "native:forge_pr_get", Source: NativeToolSource, Tags: []string{"forge"}},
	"forge_pr_list":               {CanonicalID: "native:forge_pr_list", Source: NativeToolSource, Tags: []string{"forge"}},
	"forge_pr_merge":              {CanonicalID: "native:forge_pr_merge", Source: NativeToolSource, Tags: []string{"forge"}},
	"forge_pr_request_review":     {CanonicalID: "native:forge_pr_request_review", Source: NativeToolSource, Tags: []string{"forge"}},
	"forge_pr_review":             {CanonicalID: "native:forge_pr_review", Source: NativeToolSource, Tags: []string{"forge"}},
	"forge_pr_review_comment":     {CanonicalID: "native:forge_pr_review_comment", Source: NativeToolSource, Tags: []string{"forge"}},
	"forge_pr_reviews":            {CanonicalID: "native:forge_pr_reviews", Source: NativeToolSource, Tags: []string{"forge"}},
	"forge_react":                 {CanonicalID: "native:forge_react", Source: NativeToolSource, Tags: []string{"forge"}},
	"forge_search":                {CanonicalID: "native:forge_search", Source: NativeToolSource, Tags: []string{"forge"}},
	"get_state":                   {CanonicalID: "native:get_state", Source: NativeToolSource, Tags: []string{"ha", "homeassistant"}},
	"get_version":                 {CanonicalID: "native:get_version", Source: NativeToolSource, Tags: []string{"diagnostics"}},
	"ha_automation_create":        {CanonicalID: "native:ha_automation_create", Source: NativeToolSource, Tags: []string{"ha", "homeassistant"}},
	"ha_automation_delete":        {CanonicalID: "native:ha_automation_delete", Source: NativeToolSource, Tags: []string{"ha", "homeassistant"}},
	"ha_automation_get":           {CanonicalID: "native:ha_automation_get", Source: NativeToolSource, Tags: []string{"ha", "homeassistant"}},
	"ha_automation_list":          {CanonicalID: "native:ha_automation_list", Source: NativeToolSource, Tags: []string{"ha", "homeassistant"}},
	"ha_automation_update":        {CanonicalID: "native:ha_automation_update", Source: NativeToolSource, Tags: []string{"ha", "homeassistant"}},
	"ha_notify":                   {CanonicalID: "native:ha_notify", Source: NativeToolSource, Tags: []string{"notifications"}},
	"ha_registry_search":          {CanonicalID: "native:ha_registry_search", Source: NativeToolSource, Tags: []string{"ha", "homeassistant"}},
	"import_vcf":                  {CanonicalID: "native:import_vcf", Source: NativeToolSource, Tags: []string{"contacts"}},
	"list_contacts":               {CanonicalID: "native:list_contacts", Source: NativeToolSource, Tags: []string{"contacts"}},
	"list_entities":               {CanonicalID: "native:list_entities", Source: NativeToolSource, Tags: []string{"ha", "homeassistant"}},
	"list_loaded_capabilities":    {CanonicalID: "native:list_loaded_capabilities", Source: NativeToolSource},
	"inspect_capability":          {CanonicalID: "native:inspect_capability", Source: NativeToolSource},
	"reset_capabilities":          {CanonicalID: "native:reset_capabilities", Source: NativeToolSource},
	"list_tasks":                  {CanonicalID: "native:list_tasks", Source: NativeToolSource, Tags: []string{"scheduler"}},
	"logs_query":                  {CanonicalID: "native:logs_query", Source: NativeToolSource, Tags: []string{"diagnostics"}},
	"lookup_contact":              {CanonicalID: "native:lookup_contact", Source: NativeToolSource, Tags: []string{"contacts"}},
	"owner_contact":               {CanonicalID: "native:owner_contact", Source: NativeToolSource, Tags: []string{"owner"}},
	"set_next_sleep":              {CanonicalID: "native:set_next_sleep", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_status":                 {CanonicalID: "native:loop_status", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_definition_delete":      {CanonicalID: "native:loop_definition_delete", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_definition_get":         {CanonicalID: "native:loop_definition_get", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_definition_lint":        {CanonicalID: "native:loop_definition_lint", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_definition_launch":      {CanonicalID: "native:loop_definition_launch", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_definition_list":        {CanonicalID: "native:loop_definition_list", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_definition_set":         {CanonicalID: "native:loop_definition_set", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_definition_set_policy":  {CanonicalID: "native:loop_definition_set_policy", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_definition_summary":     {CanonicalID: "native:loop_definition_summary", Source: NativeToolSource, Tags: []string{"loops"}},
	"spawn_loop":                  {CanonicalID: "native:spawn_loop", Source: NativeToolSource, Tags: []string{"loops"}},
	"stop_loop":                   {CanonicalID: "native:stop_loop", Source: NativeToolSource, Tags: []string{"loops"}},
	"notify_loop":                 {CanonicalID: "native:notify_loop", Source: NativeToolSource, Tags: []string{"loops"}},
	"thane_assign":                {CanonicalID: "native:thane_assign", Source: NativeToolSource},
	"thane_curate":                {CanonicalID: "native:thane_curate", Source: NativeToolSource, Tags: []string{"loops"}},
	"thane_now":                   {CanonicalID: "native:thane_now", Source: NativeToolSource},
	"thane_wake":                  {CanonicalID: "native:thane_wake", Source: NativeToolSource, Tags: []string{"loops"}},
	"macos_calendar_events":       {CanonicalID: "native:macos_calendar_events", Source: NativeToolSource, Tags: []string{"companion"}},
	"media_feeds":                 {CanonicalID: "native:media_feeds", Source: NativeToolSource, Tags: []string{"feeds"}},
	"media_follow":                {CanonicalID: "native:media_follow", Source: NativeToolSource, Tags: []string{"feeds"}},
	"media_save_analysis":         {CanonicalID: "native:media_save_analysis", Source: NativeToolSource, Tags: []string{"media"}},
	"media_transcript":            {CanonicalID: "native:media_transcript", Source: NativeToolSource, Tags: []string{"media", "web"}},
	"media_unfollow":              {CanonicalID: "native:media_unfollow", Source: NativeToolSource, Tags: []string{"feeds"}},
	"model_deployment_set_policy": {CanonicalID: "native:model_deployment_set_policy", Source: NativeToolSource, Tags: []string{"models"}},
	"model_registry_get":          {CanonicalID: "native:model_registry_get", Source: NativeToolSource, Tags: []string{"models"}},
	"model_registry_list":         {CanonicalID: "native:model_registry_list", Source: NativeToolSource, Tags: []string{"models"}},
	"model_registry_summary":      {CanonicalID: "native:model_registry_summary", Source: NativeToolSource, Tags: []string{"models"}},
	"model_resource_set_policy":   {CanonicalID: "native:model_resource_set_policy", Source: NativeToolSource, Tags: []string{"models"}},
	"model_route_explain":         {CanonicalID: "native:model_route_explain", Source: NativeToolSource, Tags: []string{"models"}},
	"mqtt_wake_add":               {CanonicalID: "native:mqtt_wake_add", Source: NativeToolSource, Tags: []string{"mqtt"}},
	"mqtt_wake_list":              {CanonicalID: "native:mqtt_wake_list", Source: NativeToolSource, Tags: []string{"mqtt"}},
	"mqtt_wake_remove":            {CanonicalID: "native:mqtt_wake_remove", Source: NativeToolSource, Tags: []string{"mqtt"}},
	"recall_fact":                 {CanonicalID: "native:recall_fact", Source: NativeToolSource, Tags: []string{"memory"}},
	"remember_fact":               {CanonicalID: "native:remember_fact", Source: NativeToolSource, Tags: []string{"memory"}},
	"request_ai_escalation":       {CanonicalID: "native:request_ai_escalation", Source: NativeToolSource, Tags: []string{"notifications"}},
	"request_human_decision":      {CanonicalID: "native:request_human_decision", Source: NativeToolSource, Tags: []string{"notifications"}},
	"request_human_escalation":    {CanonicalID: "native:request_human_escalation", Source: NativeToolSource, Tags: []string{"notifications"}},
	"resolve_actionable":          {CanonicalID: "native:resolve_actionable", Source: NativeToolSource, Tags: []string{"notifications"}},
	"save_contact":                {CanonicalID: "native:save_contact", Source: NativeToolSource, Tags: []string{"contacts"}},
	"schedule_task":               {CanonicalID: "native:schedule_task", Source: NativeToolSource, Tags: []string{"scheduler"}},
	"send_reaction":               {CanonicalID: "native:send_reaction", Source: NativeToolSource, Tags: []string{"message_channel"}},
	"session_checkpoint":          {CanonicalID: "native:session_checkpoint", Source: NativeToolSource, Tags: []string{"session"}},
	"session_close":               {CanonicalID: "native:session_close", Source: NativeToolSource, Tags: []string{"session"}},
	"session_split":               {CanonicalID: "native:session_split", Source: NativeToolSource, Tags: []string{"session"}},
	"session_working_memory":      {CanonicalID: "native:session_working_memory", Source: NativeToolSource, Tags: []string{"memory"}},
	"signal_send_message":         {CanonicalID: "native:signal_send_message", Source: NativeToolSource, Tags: []string{"signal"}},
	"signal_send_reaction":        {CanonicalID: "native:signal_send_reaction", Source: NativeToolSource, Tags: []string{"signal"}},
	"web_fetch":                   {CanonicalID: "native:web_fetch", Source: NativeToolSource, Tags: []string{"web"}},
	"web_search":                  {CanonicalID: "native:web_search", Source: NativeToolSource, Tags: []string{"web"}},
	"add_context_entity":          {CanonicalID: "native:add_context_entity", Source: NativeToolSource, Tags: []string{"awareness"}},
	"list_context_entities":       {CanonicalID: "native:list_context_entities", Source: NativeToolSource, Tags: []string{"awareness"}},
	"remove_context_entity":       {CanonicalID: "native:remove_context_entity", Source: NativeToolSource, Tags: []string{"awareness"}},
}

// LookupBuiltinToolSpec returns the compiled-in tool spec for a tool name.
func LookupBuiltinToolSpec(name string) (BuiltinToolSpec, bool) {
	spec, ok := builtinToolSpecs[name]
	if !ok {
		return BuiltinToolSpec{}, false
	}
	spec.Tags = append([]string(nil), spec.Tags...)
	return spec, true
}

// BuiltinTagSpecs returns a copy of the compiled-in tag catalog.
func BuiltinTagSpecs() map[string]BuiltinTagSpec {
	return maps.Clone(builtinTagSpecs)
}

// HasBuiltinTag reports whether the name is a compiled-in tag.
func HasBuiltinTag(name string) bool {
	_, ok := builtinTagSpecs[name]
	return ok
}

// BuildCapabilitySurface builds a sorted capability surface from
// tag membership and descriptions.
func BuildCapabilitySurface(tags map[string][]string, descriptions map[string]string, alwaysActive map[string]bool, protected map[string]bool) []CapabilitySurface {
	surface := make([]CapabilitySurface, 0, len(tags))
	for tag, toolNames := range tags {
		copiedTools := append([]string(nil), toolNames...)
		sort.Strings(copiedTools)
		spec := builtinTagSpecs[tag]
		surface = append(surface, CapabilitySurface{
			Tag:          tag,
			Description:  descriptions[tag],
			Tools:        copiedTools,
			AlwaysActive: alwaysActive[tag],
			Menu:         spec.Menu,
			Protected:    protected[tag],
		})
	}
	return SortCapabilitySurface(surface)
}

// SortCapabilitySurface returns a sorted copy of the capability surface.
func SortCapabilitySurface(entries []CapabilitySurface) []CapabilitySurface {
	if len(entries) == 0 {
		return nil
	}
	sorted := make([]CapabilitySurface, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Tag < sorted[j].Tag
	})
	return sorted
}

// RenderLoadedCapabilitySummary renders the currently loaded
// capabilities for always-on prompt context.
func RenderLoadedCapabilitySummary(entries []CapabilitySurface, activeTags map[string]bool) string {
	names := make([]string, 0, len(activeTags))
	for tag := range activeTags {
		names = append(names, tag)
	}

	payload := struct {
		Kind               string                  `json:"kind"`
		LoadedCapabilities []LoadedCapabilityEntry `json:"loaded_capabilities"`
	}{
		Kind:               "loaded_capabilities",
		LoadedCapabilities: BuildLoadedCapabilityEntries(entries, names),
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return "{\"kind\":\"loaded_capabilities\",\"error\":\"summary marshal failed\"}"
	}
	return string(data)
}
