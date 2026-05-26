// Package toolcatalog provides compiled-in metadata for tools and capability
// tags, and renders capability surface descriptions for model-facing context
// and the web dashboard.
package toolcatalog

import (
	"encoding/json"
	"fmt"
	"sort"
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

// TagKind describes a tag's surface role in the capability menu.
//
//   - [TagKindLeaf] is the default: an ordinary capability tag that
//     carries tools, talents, and KB articles. When the model activates
//     a leaf tag, those resources load into scope.
//   - [TagKindMenu] is a coarse trailhead that routes the model toward
//     leaf tags. Menus typically carry few or no tools of their own;
//     their value is the per-branch teaser surfaced in the capability
//     menu prompt.
//
// Tag kind is orthogonal to [BuiltinTagSpec.Protected] — a leaf can be
// protected (e.g. `message_channel`, `owner`) without becoming a menu.
type TagKind string

const (
	// TagKindLeaf is the default kind. Empty string normalizes to this.
	TagKindLeaf TagKind = "leaf"
	// TagKindMenu marks a coarse trailhead — see [TagKind] for the
	// distinction from leaves.
	TagKindMenu TagKind = "menu"
)

// IsMenu reports whether the kind is a menu. The empty string
// (zero-value) is treated as [TagKindLeaf].
func (k TagKind) IsMenu() bool {
	return k == TagKindMenu
}

// BuiltinTagSpec captures compiled-in metadata for a tag/toolset. The
// shape grew incrementally and was formalized in PR-G as part of the
// #910 talent corpus overhaul to make tag-grouping data instead of
// prose: leaf tags now point at the menu(s) they belong under via
// [Parents], and alternate names funnel through [Aliases] at activation
// time instead of being separately-declared spec entries.
type BuiltinTagSpec struct {
	// Description is the short, model-facing summary rendered into
	// the tag menu and inspect_tag output.
	Description string

	// Core tags are pinned in every scope by operator configuration.
	// Survives capability-tag filtering; cannot be deactivated by the
	// model. Re-seeded each run from config — not a property of
	// individual built-in tags, but operators can set it via the
	// capability_tags YAML overlay.
	Core bool

	// Kind classifies the tag's surface role. Zero value is treated
	// as [TagKindLeaf]; menu trailheads explicitly set
	// [TagKindMenu]. See [TagKind] for the orthogonality with
	// Protected.
	Kind TagKind

	// Protected tags cannot be toggled via activate_tag /
	// deactivate_tag — they're reserved for runtime trust
	// assertions (e.g. owner-authenticated conversations, channel
	// affordance tags asserted by the integration). Orthogonal to
	// Kind: a tag can be a protected leaf (`message_channel`,
	// `owner`) without being a menu.
	Protected bool

	// Parents lists the menu tag(s) this leaf appears under in the
	// hierarchical capability menu. Multi-valued because some leaves
	// legitimately serve more than one menu (e.g. `files` is
	// reachable from both development and knowledge). Omitted for
	// menus themselves and for tags that don't fit any menu — those
	// surface as top-level entries in the menu rendering.
	Parents []string

	// Aliases lists alternate names that resolve to this canonical
	// tag. Most relevant for backward-compatible renames or
	// operator-friendly synonyms (e.g. `homeassistant` resolves to
	// `ha`). Resolution happens at every boundary the tag enters the
	// system: activate_tag / inspect_tag calls, channel-tag binding,
	// operator YAML. Internally only canonical names exist.
	Aliases []string
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
	Core          bool
	// Kind mirrors [BuiltinTagSpec.Kind]. Use Kind.IsMenu() to test
	// menu-ness; the zero value normalizes to [TagKindLeaf].
	Kind        TagKind
	Parents     []string
	Protected   bool
	Loaded      bool
	KBArticles  int
	LiveContext bool
	AdHoc       bool
}

var builtinToolSpecs = map[string]BuiltinToolSpec{
	"activate_tag":                {CanonicalID: "native:activate_tag", Source: NativeToolSource},
	"activate_lens":               {CanonicalID: "native:activate_lens", Source: NativeToolSource},
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
	"deactivate_tag":              {CanonicalID: "native:deactivate_tag", Source: NativeToolSource},
	"deactivate_lens":             {CanonicalID: "native:deactivate_lens", Source: NativeToolSource},
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
	"forge_repo_follow":           {CanonicalID: "native:forge_repo_follow", Source: NativeToolSource, Tags: []string{"forge"}},
	"forge_repo_subscriptions":    {CanonicalID: "native:forge_repo_subscriptions", Source: NativeToolSource, Tags: []string{"forge"}},
	"forge_repo_unfollow":         {CanonicalID: "native:forge_repo_unfollow", Source: NativeToolSource, Tags: []string{"forge"}},
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
	"list_lenses":                 {CanonicalID: "native:list_lenses", Source: NativeToolSource},
	"inspect_tag":                 {CanonicalID: "native:inspect_tag", Source: NativeToolSource},
	"reset_tags":                  {CanonicalID: "native:reset_tags", Source: NativeToolSource},
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
	"thane_assign":                {CanonicalID: "native:thane_assign", Source: NativeToolSource},
	"thane_create_container":      {CanonicalID: "native:thane_create_container", Source: NativeToolSource, Tags: []string{"loops"}},
	"thane_curate":                {CanonicalID: "native:thane_curate", Source: NativeToolSource, Tags: []string{"loops"}},
	"thane_now":                   {CanonicalID: "native:thane_now", Source: NativeToolSource},
	"update_entity_subscriptions": {CanonicalID: "native:update_entity_subscriptions", Source: NativeToolSource, Tags: []string{"loops"}},
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
	"request_core_attention":      {CanonicalID: "native:request_core_attention", Source: NativeToolSource, Tags: []string{"loops"}},
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
	"send_notification":           {CanonicalID: "native:send_notification", Source: NativeToolSource, Tags: []string{"notifications"}},
	"signal_send_message":         {CanonicalID: "native:signal_send_message", Source: NativeToolSource, Tags: []string{"signal"}},
	"signal_send_reaction":        {CanonicalID: "native:signal_send_reaction", Source: NativeToolSource, Tags: []string{"signal"}},
	"web_fetch":                   {CanonicalID: "native:web_fetch", Source: NativeToolSource, Tags: []string{"web"}},
	"web_search":                  {CanonicalID: "native:web_search", Source: NativeToolSource, Tags: []string{"web"}},
	"add_entity_subscription":     {CanonicalID: "native:add_entity_subscription", Source: NativeToolSource, Tags: []string{"awareness"}},
	"list_entity_subscriptions":   {CanonicalID: "native:list_entity_subscriptions", Source: NativeToolSource, Tags: []string{"awareness", "loops"}},
	"remove_entity_subscription":  {CanonicalID: "native:remove_entity_subscription", Source: NativeToolSource, Tags: []string{"awareness"}},
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

// BuiltinToolSpecs returns a deep copy of the compiled-in tool catalog
// keyed by tool wire name. Parallels [BuiltinTagSpecs] for the
// tag-catalog half. Used by drift tests that need to iterate every
// known tool — e.g., the talent corpus regression test that catches
// hallucinated tool references in talent prose.
func BuiltinToolSpecs() map[string]BuiltinToolSpec {
	out := make(map[string]BuiltinToolSpec, len(builtinToolSpecs))
	for name, spec := range builtinToolSpecs {
		spec.Tags = append([]string(nil), spec.Tags...)
		out[name] = spec
	}
	return out
}

// BuiltinTagSpecs returns a deep copy of the compiled-in tag catalog.
// The returned map and the slice fields on each spec (Parents,
// Aliases) are independent copies — mutating them does not affect the
// global catalog. Parallels [BuiltinToolSpecs] for the tool half.
func BuiltinTagSpecs() map[string]BuiltinTagSpec {
	out := make(map[string]BuiltinTagSpec, len(builtinTagSpecs))
	for name, spec := range builtinTagSpecs {
		spec.Parents = append([]string(nil), spec.Parents...)
		spec.Aliases = append([]string(nil), spec.Aliases...)
		out[name] = spec
	}
	return out
}

// HasBuiltinTag reports whether the name is a compiled-in tag,
// resolving aliases. So `HasBuiltinTag("homeassistant")` returns true
// because `ha` declares it as an alias.
func HasBuiltinTag(name string) bool {
	if _, ok := builtinTagSpecs[name]; ok {
		return true
	}
	_, ok := builtinTagAliases[name]
	return ok
}

// CanonicalTagName returns the canonical tag name for value, resolving
// aliases. If value is already a canonical tag (or an unknown name), it
// is returned unchanged.
func CanonicalTagName(value string) string {
	if canonical, ok := builtinTagAliases[value]; ok {
		return canonical
	}
	return value
}

// LookupBuiltinTagSpec returns the spec for name, resolving aliases.
// Returns the zero value and false for unknown names.
func LookupBuiltinTagSpec(name string) (BuiltinTagSpec, bool) {
	if spec, ok := builtinTagSpecs[name]; ok {
		return spec, true
	}
	if canonical, ok := builtinTagAliases[name]; ok {
		spec, found := builtinTagSpecs[canonical]
		return spec, found
	}
	return BuiltinTagSpec{}, false
}

// builtinTagAliases is the reverse-lookup map populated from each
// canonical spec's Aliases field at package init. Lookups are
// alias → canonical name; resolving an unknown name returns ("", false).
//
// Until alias resolution propagates to every tag ingress point (config
// validation, channel-tag binding, loop spec, persisted scope), tools
// belonging to a canonical tag with aliases should *also* carry the
// alias names in their Tags slice — see the `ha` / `homeassistant`
// pattern in builtinToolSpecs. The CanonicalTagName helper is the
// model-facing resolution boundary; the double-tagging is the
// tag-index bridge until that resolution covers every ingress.
var builtinTagAliases map[string]string

func init() {
	builtinTagAliases = make(map[string]string)
	for canonical, spec := range builtinTagSpecs {
		for _, alias := range spec.Aliases {
			if _, collides := builtinTagSpecs[alias]; collides {
				panic(fmt.Sprintf(
					"toolcatalog: alias %q on canonical tag %q collides with an existing canonical tag of the same name",
					alias, canonical))
			}
			if existing, dup := builtinTagAliases[alias]; dup {
				panic(fmt.Sprintf(
					"toolcatalog: alias %q declared by both %q and %q",
					alias, existing, canonical))
			}
			builtinTagAliases[alias] = canonical
		}
	}
}

// BuildCapabilitySurface builds a sorted capability surface from
// tag membership and descriptions.
func BuildCapabilitySurface(tags map[string][]string, descriptions map[string]string, core map[string]bool, protected map[string]bool) []CapabilitySurface {
	surface := make([]CapabilitySurface, 0, len(tags))
	for tag, toolNames := range tags {
		copiedTools := append([]string(nil), toolNames...)
		sort.Strings(copiedTools)
		spec := builtinTagSpecs[tag]
		surface = append(surface, CapabilitySurface{
			Tag:         tag,
			Description: descriptions[tag],
			Tools:       copiedTools,
			Core:        core[tag],
			Kind:        spec.Kind,
			Parents:     append([]string(nil), spec.Parents...),
			Protected:   protected[tag],
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
