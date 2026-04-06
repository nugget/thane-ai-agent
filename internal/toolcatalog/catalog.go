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
	DefaultTags []string
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
type CapabilitySurface struct {
	Tag          string
	Description  string
	Teaser       string
	NextTags     []string
	Tools        []string
	AlwaysActive bool
	Menu         bool
	Protected    bool
	Loaded       bool
	KBArticles   int
	LiveContext  bool
	AdHoc        bool
}

type CapabilityContextSummary struct {
	KBArticles int  `json:"kb_articles,omitempty"`
	Live       bool `json:"live,omitempty"`
}

type CapabilityCatalogEntry struct {
	Tag          string                    `json:"tag"`
	Status       string                    `json:"status"`
	Description  string                    `json:"description"`
	Teaser       string                    `json:"teaser,omitempty"`
	NextTags     []string                  `json:"next_tags,omitempty"`
	ToolCount    int                       `json:"tool_count,omitempty"`
	Tools        []string                  `json:"tools,omitempty"`
	AlwaysActive bool                      `json:"always_active,omitempty"`
	Protected    bool                      `json:"protected,omitempty"`
	AdHoc        bool                      `json:"ad_hoc,omitempty"`
	Context      *CapabilityContextSummary `json:"context,omitempty"`
}

type LoadedCapabilityEntry struct {
	Tag          string                    `json:"tag"`
	Description  string                    `json:"description,omitempty"`
	ToolCount    int                       `json:"tool_count,omitempty"`
	AlwaysActive bool                      `json:"always_active,omitempty"`
	Protected    bool                      `json:"protected,omitempty"`
	AdHoc        bool                      `json:"ad_hoc,omitempty"`
	Context      *CapabilityContextSummary `json:"context,omitempty"`
}

type CapabilityActionTools struct {
	Activate   string `json:"activate"`
	Deactivate string `json:"deactivate"`
	List       string `json:"list,omitempty"`
	Delegate   string `json:"delegate,omitempty"`
}

type CapabilityCatalogView struct {
	Kind            string                   `json:"kind"`
	ActivationTools CapabilityActionTools    `json:"activation_tools"`
	Capabilities    []CapabilityCatalogEntry `json:"capabilities"`
}

type LoadedCapabilityView struct {
	Kind               string                  `json:"kind"`
	ActivationTools    CapabilityActionTools   `json:"activation_tools"`
	LoadedCapabilities []LoadedCapabilityEntry `json:"loaded_capabilities"`
}

func defaultCapabilityActionTools(includeDelegate bool) CapabilityActionTools {
	tools := CapabilityActionTools{
		Activate:   "activate_capability",
		Deactivate: "deactivate_capability",
		List:       "list_loaded_capabilities",
	}
	if includeDelegate {
		tools.Delegate = "thane_delegate"
	}
	return tools
}

func BuildCapabilityCatalogView(entries []CapabilitySurface, includeDelegate bool) CapabilityCatalogView {
	view := CapabilityCatalogView{
		Kind:            "capability_catalog",
		ActivationTools: defaultCapabilityActionTools(includeDelegate),
		Capabilities:    make([]CapabilityCatalogEntry, 0, len(entries)),
	}

	for _, entry := range SortCapabilitySurface(entries) {
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
		view.Capabilities = append(view.Capabilities, rendered)
	}

	return view
}

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

func BuildLoadedCapabilityView(entries []CapabilitySurface, activeTags []string, includeDelegate bool) LoadedCapabilityView {
	return LoadedCapabilityView{
		Kind:               "loaded_capabilities",
		ActivationTools:    defaultCapabilityActionTools(includeDelegate),
		LoadedCapabilities: BuildLoadedCapabilityEntries(entries, activeTags),
	}
}

var builtinToolSpecs = map[string]BuiltinToolSpec{
	"archive_search":              {CanonicalID: "native:archive_search", Source: NativeToolSource, DefaultTags: []string{"archive"}},
	"archive_session_transcript":  {CanonicalID: "native:archive_session_transcript", Source: NativeToolSource, DefaultTags: []string{"archive"}},
	"archive_sessions":            {CanonicalID: "native:archive_sessions", Source: NativeToolSource, DefaultTags: []string{"archive"}},
	"attachment_describe":         {CanonicalID: "native:attachment_describe", Source: NativeToolSource, DefaultTags: []string{"attachments"}},
	"attachment_list":             {CanonicalID: "native:attachment_list", Source: NativeToolSource, DefaultTags: []string{"attachments"}},
	"attachment_search":           {CanonicalID: "native:attachment_search", Source: NativeToolSource, DefaultTags: []string{"attachments"}},
	"call_service":                {CanonicalID: "native:call_service", Source: NativeToolSource, DefaultTags: []string{"ha", "homeassistant"}},
	"cancel_task":                 {CanonicalID: "native:cancel_task", Source: NativeToolSource, DefaultTags: []string{"scheduler"}},
	"control_device":              {CanonicalID: "native:control_device", Source: NativeToolSource, DefaultTags: []string{"ha", "homeassistant"}},
	"conversation_reset":          {CanonicalID: "native:conversation_reset", Source: NativeToolSource, DefaultTags: []string{"session"}},
	"cost_summary":                {CanonicalID: "native:cost_summary", Source: NativeToolSource, DefaultTags: []string{"diagnostics"}},
	"create_temp_file":            {CanonicalID: "native:create_temp_file", Source: NativeToolSource, DefaultTags: []string{"files"}},
	"email_folders":               {CanonicalID: "native:email_folders", Source: NativeToolSource, DefaultTags: []string{"email"}},
	"email_list":                  {CanonicalID: "native:email_list", Source: NativeToolSource, DefaultTags: []string{"email"}},
	"email_mark":                  {CanonicalID: "native:email_mark", Source: NativeToolSource, DefaultTags: []string{"email"}},
	"email_move":                  {CanonicalID: "native:email_move", Source: NativeToolSource, DefaultTags: []string{"email"}},
	"email_read":                  {CanonicalID: "native:email_read", Source: NativeToolSource, DefaultTags: []string{"email"}},
	"email_reply":                 {CanonicalID: "native:email_reply", Source: NativeToolSource, DefaultTags: []string{"email"}},
	"email_search":                {CanonicalID: "native:email_search", Source: NativeToolSource, DefaultTags: []string{"email"}},
	"email_send":                  {CanonicalID: "native:email_send", Source: NativeToolSource, DefaultTags: []string{"email"}},
	"exec":                        {CanonicalID: "native:exec", Source: NativeToolSource, DefaultTags: []string{"shell"}},
	"export_all_vcf":              {CanonicalID: "native:export_all_vcf", Source: NativeToolSource, DefaultTags: []string{"contacts"}},
	"export_vcf":                  {CanonicalID: "native:export_vcf", Source: NativeToolSource, DefaultTags: []string{"contacts"}},
	"export_vcf_qr":               {CanonicalID: "native:export_vcf_qr", Source: NativeToolSource, DefaultTags: []string{"contacts"}},
	"file_edit":                   {CanonicalID: "native:file_edit", Source: NativeToolSource, DefaultTags: []string{"files"}},
	"file_grep":                   {CanonicalID: "native:file_grep", Source: NativeToolSource, DefaultTags: []string{"files"}},
	"file_list":                   {CanonicalID: "native:file_list", Source: NativeToolSource, DefaultTags: []string{"files"}},
	"file_read":                   {CanonicalID: "native:file_read", Source: NativeToolSource, DefaultTags: []string{"files"}},
	"file_search":                 {CanonicalID: "native:file_search", Source: NativeToolSource, DefaultTags: []string{"files"}},
	"file_stat":                   {CanonicalID: "native:file_stat", Source: NativeToolSource, DefaultTags: []string{"files"}},
	"file_tree":                   {CanonicalID: "native:file_tree", Source: NativeToolSource, DefaultTags: []string{"files"}},
	"file_write":                  {CanonicalID: "native:file_write", Source: NativeToolSource, DefaultTags: []string{"files"}},
	"find_entity":                 {CanonicalID: "native:find_entity", Source: NativeToolSource, DefaultTags: []string{"ha", "homeassistant"}},
	"forget_contact":              {CanonicalID: "native:forget_contact", Source: NativeToolSource, DefaultTags: []string{"contacts"}},
	"forget_fact":                 {CanonicalID: "native:forget_fact", Source: NativeToolSource, DefaultTags: []string{"memory"}},
	"forge_issue_comment":         {CanonicalID: "native:forge_issue_comment", Source: NativeToolSource, DefaultTags: []string{"forge"}},
	"forge_issue_create":          {CanonicalID: "native:forge_issue_create", Source: NativeToolSource, DefaultTags: []string{"forge"}},
	"forge_issue_get":             {CanonicalID: "native:forge_issue_get", Source: NativeToolSource, DefaultTags: []string{"forge"}},
	"forge_issue_list":            {CanonicalID: "native:forge_issue_list", Source: NativeToolSource, DefaultTags: []string{"forge"}},
	"forge_issue_update":          {CanonicalID: "native:forge_issue_update", Source: NativeToolSource, DefaultTags: []string{"forge"}},
	"forge_pr_checks":             {CanonicalID: "native:forge_pr_checks", Source: NativeToolSource, DefaultTags: []string{"forge"}},
	"forge_pr_commits":            {CanonicalID: "native:forge_pr_commits", Source: NativeToolSource, DefaultTags: []string{"forge"}},
	"forge_pr_diff":               {CanonicalID: "native:forge_pr_diff", Source: NativeToolSource, DefaultTags: []string{"forge"}},
	"forge_pr_files":              {CanonicalID: "native:forge_pr_files", Source: NativeToolSource, DefaultTags: []string{"forge"}},
	"forge_pr_get":                {CanonicalID: "native:forge_pr_get", Source: NativeToolSource, DefaultTags: []string{"forge"}},
	"forge_pr_list":               {CanonicalID: "native:forge_pr_list", Source: NativeToolSource, DefaultTags: []string{"forge"}},
	"forge_pr_merge":              {CanonicalID: "native:forge_pr_merge", Source: NativeToolSource, DefaultTags: []string{"forge"}},
	"forge_pr_request_review":     {CanonicalID: "native:forge_pr_request_review", Source: NativeToolSource, DefaultTags: []string{"forge"}},
	"forge_pr_review":             {CanonicalID: "native:forge_pr_review", Source: NativeToolSource, DefaultTags: []string{"forge"}},
	"forge_pr_review_comment":     {CanonicalID: "native:forge_pr_review_comment", Source: NativeToolSource, DefaultTags: []string{"forge"}},
	"forge_pr_reviews":            {CanonicalID: "native:forge_pr_reviews", Source: NativeToolSource, DefaultTags: []string{"forge"}},
	"forge_react":                 {CanonicalID: "native:forge_react", Source: NativeToolSource, DefaultTags: []string{"forge"}},
	"forge_search":                {CanonicalID: "native:forge_search", Source: NativeToolSource, DefaultTags: []string{"forge"}},
	"get_state":                   {CanonicalID: "native:get_state", Source: NativeToolSource, DefaultTags: []string{"ha", "homeassistant"}},
	"get_version":                 {CanonicalID: "native:get_version", Source: NativeToolSource, DefaultTags: []string{"diagnostics"}},
	"ha_automation_create":        {CanonicalID: "native:ha_automation_create", Source: NativeToolSource, DefaultTags: []string{"ha", "homeassistant"}},
	"ha_automation_delete":        {CanonicalID: "native:ha_automation_delete", Source: NativeToolSource, DefaultTags: []string{"ha", "homeassistant"}},
	"ha_automation_get":           {CanonicalID: "native:ha_automation_get", Source: NativeToolSource, DefaultTags: []string{"ha", "homeassistant"}},
	"ha_automation_list":          {CanonicalID: "native:ha_automation_list", Source: NativeToolSource, DefaultTags: []string{"ha", "homeassistant"}},
	"ha_automation_update":        {CanonicalID: "native:ha_automation_update", Source: NativeToolSource, DefaultTags: []string{"ha", "homeassistant"}},
	"ha_notify":                   {CanonicalID: "native:ha_notify", Source: NativeToolSource, DefaultTags: []string{"notifications"}},
	"ha_registry_search":          {CanonicalID: "native:ha_registry_search", Source: NativeToolSource, DefaultTags: []string{"ha", "homeassistant"}},
	"import_vcf":                  {CanonicalID: "native:import_vcf", Source: NativeToolSource, DefaultTags: []string{"contacts"}},
	"list_contacts":               {CanonicalID: "native:list_contacts", Source: NativeToolSource, DefaultTags: []string{"contacts"}},
	"list_entities":               {CanonicalID: "native:list_entities", Source: NativeToolSource, DefaultTags: []string{"ha", "homeassistant"}},
	"list_loaded_capabilities":    {CanonicalID: "native:list_loaded_capabilities", Source: NativeToolSource},
	"list_tasks":                  {CanonicalID: "native:list_tasks", Source: NativeToolSource, DefaultTags: []string{"scheduler"}},
	"logs_query":                  {CanonicalID: "native:logs_query", Source: NativeToolSource, DefaultTags: []string{"diagnostics"}},
	"lookup_contact":              {CanonicalID: "native:lookup_contact", Source: NativeToolSource, DefaultTags: []string{"contacts"}},
	"owner_contact":               {CanonicalID: "native:owner_contact", Source: NativeToolSource, DefaultTags: []string{"owner"}},
	"loop_definition_delete":      {CanonicalID: "native:loop_definition_delete", Source: NativeToolSource, DefaultTags: []string{"loops"}},
	"loop_definition_get":         {CanonicalID: "native:loop_definition_get", Source: NativeToolSource, DefaultTags: []string{"loops"}},
	"loop_definition_launch":      {CanonicalID: "native:loop_definition_launch", Source: NativeToolSource, DefaultTags: []string{"loops"}},
	"loop_definition_list":        {CanonicalID: "native:loop_definition_list", Source: NativeToolSource, DefaultTags: []string{"loops"}},
	"loop_definition_set":         {CanonicalID: "native:loop_definition_set", Source: NativeToolSource, DefaultTags: []string{"loops"}},
	"loop_definition_set_policy":  {CanonicalID: "native:loop_definition_set_policy", Source: NativeToolSource, DefaultTags: []string{"loops"}},
	"loop_definition_summary":     {CanonicalID: "native:loop_definition_summary", Source: NativeToolSource, DefaultTags: []string{"loops"}},
	"macos_calendar_events":       {CanonicalID: "native:macos_calendar_events", Source: NativeToolSource, DefaultTags: []string{"platform"}},
	"media_feeds":                 {CanonicalID: "native:media_feeds", Source: NativeToolSource, DefaultTags: []string{"feeds"}},
	"media_follow":                {CanonicalID: "native:media_follow", Source: NativeToolSource, DefaultTags: []string{"feeds"}},
	"media_save_analysis":         {CanonicalID: "native:media_save_analysis", Source: NativeToolSource, DefaultTags: []string{"media"}},
	"media_transcript":            {CanonicalID: "native:media_transcript", Source: NativeToolSource, DefaultTags: []string{"media", "search"}},
	"media_unfollow":              {CanonicalID: "native:media_unfollow", Source: NativeToolSource, DefaultTags: []string{"feeds"}},
	"model_deployment_set_policy": {CanonicalID: "native:model_deployment_set_policy", Source: NativeToolSource, DefaultTags: []string{"models"}},
	"model_registry_get":          {CanonicalID: "native:model_registry_get", Source: NativeToolSource, DefaultTags: []string{"models"}},
	"model_registry_list":         {CanonicalID: "native:model_registry_list", Source: NativeToolSource, DefaultTags: []string{"models"}},
	"model_registry_summary":      {CanonicalID: "native:model_registry_summary", Source: NativeToolSource, DefaultTags: []string{"models"}},
	"model_resource_set_policy":   {CanonicalID: "native:model_resource_set_policy", Source: NativeToolSource, DefaultTags: []string{"models"}},
	"model_route_explain":         {CanonicalID: "native:model_route_explain", Source: NativeToolSource, DefaultTags: []string{"models"}},
	"mqtt_wake_add":               {CanonicalID: "native:mqtt_wake_add", Source: NativeToolSource, DefaultTags: []string{"mqtt"}},
	"mqtt_wake_list":              {CanonicalID: "native:mqtt_wake_list", Source: NativeToolSource, DefaultTags: []string{"mqtt"}},
	"mqtt_wake_remove":            {CanonicalID: "native:mqtt_wake_remove", Source: NativeToolSource, DefaultTags: []string{"mqtt"}},
	"recall_fact":                 {CanonicalID: "native:recall_fact", Source: NativeToolSource, DefaultTags: []string{"memory"}},
	"remember_fact":               {CanonicalID: "native:remember_fact", Source: NativeToolSource, DefaultTags: []string{"memory"}},
	"request_ai_escalation":       {CanonicalID: "native:request_ai_escalation", Source: NativeToolSource, DefaultTags: []string{"notifications"}},
	"request_human_decision":      {CanonicalID: "native:request_human_decision", Source: NativeToolSource, DefaultTags: []string{"notifications"}},
	"request_human_escalation":    {CanonicalID: "native:request_human_escalation", Source: NativeToolSource, DefaultTags: []string{"notifications"}},
	"resolve_actionable":          {CanonicalID: "native:resolve_actionable", Source: NativeToolSource, DefaultTags: []string{"notifications"}},
	"save_contact":                {CanonicalID: "native:save_contact", Source: NativeToolSource, DefaultTags: []string{"contacts"}},
	"schedule_task":               {CanonicalID: "native:schedule_task", Source: NativeToolSource, DefaultTags: []string{"scheduler"}},
	"session_checkpoint":          {CanonicalID: "native:session_checkpoint", Source: NativeToolSource, DefaultTags: []string{"session"}},
	"session_close":               {CanonicalID: "native:session_close", Source: NativeToolSource, DefaultTags: []string{"session"}},
	"session_split":               {CanonicalID: "native:session_split", Source: NativeToolSource, DefaultTags: []string{"session"}},
	"session_working_memory":      {CanonicalID: "native:session_working_memory", Source: NativeToolSource, DefaultTags: []string{"memory"}},
	"signal_send_message":         {CanonicalID: "native:signal_send_message", Source: NativeToolSource, DefaultTags: []string{"signal"}},
	"signal_send_reaction":        {CanonicalID: "native:signal_send_reaction", Source: NativeToolSource, DefaultTags: []string{"signal"}},
	"web_fetch":                   {CanonicalID: "native:web_fetch", Source: NativeToolSource, DefaultTags: []string{"search"}},
	"web_search":                  {CanonicalID: "native:web_search", Source: NativeToolSource, DefaultTags: []string{"search"}},
	"add_context_entity":          {CanonicalID: "native:add_context_entity", Source: NativeToolSource, DefaultTags: []string{"awareness"}},
	"remove_context_entity":       {CanonicalID: "native:remove_context_entity", Source: NativeToolSource, DefaultTags: []string{"awareness"}},
}

// LookupBuiltinToolSpec returns the compiled-in tool spec for a tool name.
func LookupBuiltinToolSpec(name string) (BuiltinToolSpec, bool) {
	spec, ok := builtinToolSpecs[name]
	if !ok {
		return BuiltinToolSpec{}, false
	}
	spec.DefaultTags = append([]string(nil), spec.DefaultTags...)
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
		Kind                       string                  `json:"kind"`
		CatalogEntriesAreNotLoaded bool                    `json:"catalog_entries_are_not_loaded"`
		ActivationTools            CapabilityActionTools   `json:"activation_tools"`
		LoadedCapabilities         []LoadedCapabilityEntry `json:"loaded_capabilities"`
	}{
		Kind:                       "loaded_capabilities",
		CatalogEntriesAreNotLoaded: true,
		ActivationTools:            defaultCapabilityActionTools(false),
		LoadedCapabilities:         BuildLoadedCapabilityEntries(entries, names),
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return "{\"kind\":\"loaded_capabilities\",\"error\":\"summary marshal failed\"}"
	}
	return string(data)
}
