// Package toolcatalog provides compiled-in metadata for tools and capability
// tags, and renders capability surface descriptions for model-facing context
// and the web dashboard.
//
// Tag names are first-class — there is no alias mechanism. Every
// external surface (operator config, channel bindings, loop specs,
// persisted scope, talents, knowledge frontmatter) spells each
// built-in tag with its compiled-in name; the v0.9.3 retirement of
// the alias machinery (#925) removed the `CanonicalTagName` resolver
// and the `Aliases` field on [BuiltinTagSpec], so any pre-v0.9.3
// reference to `homeassistant` (the one alias that ever existed)
// needs to be rewritten as `ha`. Operators may still define ad-hoc
// tags via the `capability_tags:` YAML overlay; those names pass
// through verbatim.
package toolcatalog

import (
	"encoding/json"
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
// prose: leaf tags point at the menu(s) they belong under via
// [Parents]. Built-in tag names have no aliases — every external
// surface (operator config, channel bindings, loop specs, persisted
// scope, talents, knowledge frontmatter) must spell each built-in
// tag with its compiled-in name. Operators may still define their
// own ad-hoc tags via the capability_tags YAML overlay; those names
// pass through verbatim.
type BuiltinTagSpec struct {
	// Description is the short, model-facing summary rendered into
	// the tag menu and tag_inspect output.
	Description string

	// Core marks a tag as pinned in every scope — it survives
	// capability-tag filtering and cannot be deactivated by the model.
	// Re-seeded each run from operator configuration; operators set it
	// via the capability_tags YAML overlay.
	Core bool

	// Kind classifies the tag's surface role. Zero value is treated
	// as [TagKindLeaf]; menu trailheads explicitly set
	// [TagKindMenu]. See [TagKind] for the orthogonality with
	// Protected.
	Kind TagKind

	// Protected tags cannot be toggled via tag_activate /
	// tag_deactivate — they're reserved for runtime trust
	// assertions (e.g. owner-authenticated conversations, channel
	// affordance tags asserted by the integration). Orthogonal to
	// Kind: a tag can be a protected leaf (`message_channel`,
	// `owner`) without being a menu.
	Protected bool

	// Parents lists the menu tag(s) a leaf appears under in the intended
	// hierarchical capability menu. Multi-valued because some leaves
	// legitimately serve more than one menu (e.g. `files` is reachable
	// from both development and knowledge). Omitted for menus themselves
	// and for tags that don't fit any menu.
	//
	// Reserved: Parents is authored on every leaf and validated by the
	// catalog invariant test, but no renderer consumes it yet — the
	// hierarchical menu that would group leaves beneath their parents has
	// not shipped. Until it does, menu navigation flows through NextTags.
	Parents []string
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
	"tag_activate":                {CanonicalID: "native:tag_activate", Source: NativeToolSource},
	"lens_activate":               {CanonicalID: "native:lens_activate", Source: NativeToolSource},
	"archive_range":               {CanonicalID: "native:archive_range", Source: NativeToolSource, Tags: []string{"archive"}},
	"archive_search":              {CanonicalID: "native:archive_search", Source: NativeToolSource, Tags: []string{"archive"}},
	"archive_session_transcript":  {CanonicalID: "native:archive_session_transcript", Source: NativeToolSource, Tags: []string{"archive"}},
	"archive_sessions":            {CanonicalID: "native:archive_sessions", Source: NativeToolSource, Tags: []string{"archive"}},
	"attachment_describe":         {CanonicalID: "native:attachment_describe", Source: NativeToolSource, Tags: []string{"attachments"}},
	"attachment_list":             {CanonicalID: "native:attachment_list", Source: NativeToolSource, Tags: []string{"attachments"}},
	"attachment_search":           {CanonicalID: "native:attachment_search", Source: NativeToolSource, Tags: []string{"attachments"}},
	"ha_call_service":             {CanonicalID: "native:ha_call_service", Source: NativeToolSource, Tags: []string{"ha"}},
	"task_cancel":                 {CanonicalID: "native:task_cancel", Source: NativeToolSource, Tags: []string{"scheduler"}},
	"ha_control_device":           {CanonicalID: "native:ha_control_device", Source: NativeToolSource, Tags: []string{"ha"}},
	"conversation_reset":          {CanonicalID: "native:conversation_reset", Source: NativeToolSource, Tags: []string{"session"}},
	"cost_summary":                {CanonicalID: "native:cost_summary", Source: NativeToolSource, Tags: []string{"diagnostics"}},
	"create_temp_file":            {CanonicalID: "native:create_temp_file", Source: NativeToolSource, Tags: []string{"files"}},
	"tag_deactivate":              {CanonicalID: "native:tag_deactivate", Source: NativeToolSource},
	"lens_deactivate":             {CanonicalID: "native:lens_deactivate", Source: NativeToolSource},
	"doc_at":                      {CanonicalID: "native:doc_at", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_browse":                  {CanonicalID: "native:doc_browse", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_commit":                  {CanonicalID: "native:doc_commit", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_copy":                    {CanonicalID: "native:doc_copy", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_copy_section":            {CanonicalID: "native:doc_copy_section", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_delete":                  {CanonicalID: "native:doc_delete", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_diff":                    {CanonicalID: "native:doc_diff", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_edit":                    {CanonicalID: "native:doc_edit", Source: NativeToolSource, Tags: []string{"documents"}},
	"doc_history":                 {CanonicalID: "native:doc_history", Source: NativeToolSource, Tags: []string{"documents"}},
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
	"contact_export_all_vcf":      {CanonicalID: "native:contact_export_all_vcf", Source: NativeToolSource, Tags: []string{"contacts"}},
	"contact_export_vcf":          {CanonicalID: "native:contact_export_vcf", Source: NativeToolSource, Tags: []string{"contacts"}},
	"contact_export_vcf_qr":       {CanonicalID: "native:contact_export_vcf_qr", Source: NativeToolSource, Tags: []string{"contacts"}},
	"file_edit":                   {CanonicalID: "native:file_edit", Source: NativeToolSource, Tags: []string{"files"}},
	"file_grep":                   {CanonicalID: "native:file_grep", Source: NativeToolSource, Tags: []string{"files"}},
	"file_list":                   {CanonicalID: "native:file_list", Source: NativeToolSource, Tags: []string{"files"}},
	"file_read":                   {CanonicalID: "native:file_read", Source: NativeToolSource, Tags: []string{"files"}},
	"file_search":                 {CanonicalID: "native:file_search", Source: NativeToolSource, Tags: []string{"files"}},
	"file_stat":                   {CanonicalID: "native:file_stat", Source: NativeToolSource, Tags: []string{"files"}},
	"file_tree":                   {CanonicalID: "native:file_tree", Source: NativeToolSource, Tags: []string{"files"}},
	"file_write":                  {CanonicalID: "native:file_write", Source: NativeToolSource, Tags: []string{"files"}},
	"ha_find_entity":              {CanonicalID: "native:ha_find_entity", Source: NativeToolSource, Tags: []string{"ha"}},
	"contact_forget":              {CanonicalID: "native:contact_forget", Source: NativeToolSource, Tags: []string{"contacts"}},
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
	"ha_get_state":                {CanonicalID: "native:ha_get_state", Source: NativeToolSource, Tags: []string{"ha"}},
	"get_version":                 {CanonicalID: "native:get_version", Source: NativeToolSource, Tags: []string{"diagnostics"}},
	"ha_automation_create":        {CanonicalID: "native:ha_automation_create", Source: NativeToolSource, Tags: []string{"ha"}},
	"ha_automation_delete":        {CanonicalID: "native:ha_automation_delete", Source: NativeToolSource, Tags: []string{"ha"}},
	"ha_automation_get":           {CanonicalID: "native:ha_automation_get", Source: NativeToolSource, Tags: []string{"ha"}},
	"ha_automation_list":          {CanonicalID: "native:ha_automation_list", Source: NativeToolSource, Tags: []string{"ha"}},
	"ha_automation_update":        {CanonicalID: "native:ha_automation_update", Source: NativeToolSource, Tags: []string{"ha"}},
	"ha_notify":                   {CanonicalID: "native:ha_notify", Source: NativeToolSource, Tags: []string{"notifications"}},
	"ha_registry_search":          {CanonicalID: "native:ha_registry_search", Source: NativeToolSource, Tags: []string{"ha"}},
	"contact_import_vcf":          {CanonicalID: "native:contact_import_vcf", Source: NativeToolSource, Tags: []string{"contacts"}},
	"contact_list":                {CanonicalID: "native:contact_list", Source: NativeToolSource, Tags: []string{"contacts"}},
	"ha_list_entities":            {CanonicalID: "native:ha_list_entities", Source: NativeToolSource, Tags: []string{"ha"}},
	"ha_automation_traces":        {CanonicalID: "native:ha_automation_traces", Source: NativeToolSource, Tags: []string{"ha"}},
	"ha_automation_vocabulary":    {CanonicalID: "native:ha_automation_vocabulary", Source: NativeToolSource, Tags: []string{"ha"}},
	"ha_list_services":            {CanonicalID: "native:ha_list_services", Source: NativeToolSource, Tags: []string{"ha"}},
	"ha_search_states":            {CanonicalID: "native:ha_search_states", Source: NativeToolSource, Tags: []string{"ha"}},
	"get_area_activity":           {CanonicalID: "native:get_area_activity", Source: NativeToolSource, Tags: []string{"ha"}},
	"ha_device":                   {CanonicalID: "native:ha_device", Source: NativeToolSource, Tags: []string{"ha"}},
	"ha_history":                  {CanonicalID: "native:ha_history", Source: NativeToolSource, Tags: []string{"ha"}},
	"ha_home_snapshot":            {CanonicalID: "native:ha_home_snapshot", Source: NativeToolSource, Tags: []string{"ha"}},
	"lens_list":                   {CanonicalID: "native:lens_list", Source: NativeToolSource},
	"tag_inspect":                 {CanonicalID: "native:tag_inspect", Source: NativeToolSource},
	"tag_reset":                   {CanonicalID: "native:tag_reset", Source: NativeToolSource},
	"task_list":                   {CanonicalID: "native:task_list", Source: NativeToolSource, Tags: []string{"scheduler"}},
	"logs_query":                  {CanonicalID: "native:logs_query", Source: NativeToolSource, Tags: []string{"diagnostics"}},
	"contact_lookup":              {CanonicalID: "native:contact_lookup", Source: NativeToolSource, Tags: []string{"contacts"}},
	"contact_owner":               {CanonicalID: "native:contact_owner", Source: NativeToolSource, Tags: []string{"owner"}},
	"set_next_sleep":              {CanonicalID: "native:set_next_sleep", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_status":                 {CanonicalID: "native:loop_status", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_containers":             {CanonicalID: "native:loop_containers", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_wake":                   {CanonicalID: "native:loop_wake", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_definition_delete":      {CanonicalID: "native:loop_definition_delete", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_definition_get":         {CanonicalID: "native:loop_definition_get", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_definition_lint":        {CanonicalID: "native:loop_definition_lint", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_definition_launch":      {CanonicalID: "native:loop_definition_launch", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_definition_list":        {CanonicalID: "native:loop_definition_list", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_definition_update":      {CanonicalID: "native:loop_definition_update", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_definition_set":         {CanonicalID: "native:loop_definition_set", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_definition_set_policy":  {CanonicalID: "native:loop_definition_set_policy", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_definition_summary":     {CanonicalID: "native:loop_definition_summary", Source: NativeToolSource, Tags: []string{"loops"}},
	"loop_reparent":               {CanonicalID: "native:loop_reparent", Source: NativeToolSource, Tags: []string{"loops"}},
	"spawn_loop":                  {CanonicalID: "native:spawn_loop", Source: NativeToolSource, Tags: []string{"loops"}},
	"stop_loop":                   {CanonicalID: "native:stop_loop", Source: NativeToolSource, Tags: []string{"loops"}},
	"thane_assign":                {CanonicalID: "native:thane_assign", Source: NativeToolSource},
	"thane_loop_create":           {CanonicalID: "native:thane_loop_create", Source: NativeToolSource},
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
	"mqtt_wake_add":               {CanonicalID: "native:mqtt_wake_add", Source: NativeToolSource, Tags: []string{"loops"}},
	"mqtt_wake_list":              {CanonicalID: "native:mqtt_wake_list", Source: NativeToolSource, Tags: []string{"loops"}},
	"mqtt_wake_remove":            {CanonicalID: "native:mqtt_wake_remove", Source: NativeToolSource, Tags: []string{"loops"}},
	"recall_fact":                 {CanonicalID: "native:recall_fact", Source: NativeToolSource, Tags: []string{"memory"}},
	"remember_fact":               {CanonicalID: "native:remember_fact", Source: NativeToolSource, Tags: []string{"memory"}},
	"request_ai_escalation":       {CanonicalID: "native:request_ai_escalation", Source: NativeToolSource, Tags: []string{"notifications"}},
	"request_core_attention":      {CanonicalID: "native:request_core_attention", Source: NativeToolSource, Tags: []string{"loops"}},
	"request_human_decision":      {CanonicalID: "native:request_human_decision", Source: NativeToolSource, Tags: []string{"notifications"}},
	"request_human_escalation":    {CanonicalID: "native:request_human_escalation", Source: NativeToolSource, Tags: []string{"notifications"}},
	"resolve_actionable":          {CanonicalID: "native:resolve_actionable", Source: NativeToolSource, Tags: []string{"notifications"}},
	"contact_save":                {CanonicalID: "native:contact_save", Source: NativeToolSource, Tags: []string{"contacts"}},
	"task_schedule":               {CanonicalID: "native:task_schedule", Source: NativeToolSource, Tags: []string{"scheduler"}},
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
// The returned map and the Parents slice on each spec are independent
// copies — mutating them does not affect the global catalog. Parallels
// [BuiltinToolSpecs] for the tool half.
func BuiltinTagSpecs() map[string]BuiltinTagSpec {
	out := make(map[string]BuiltinTagSpec, len(builtinTagSpecs))
	for name, spec := range builtinTagSpecs {
		spec.Parents = append([]string(nil), spec.Parents...)
		out[name] = spec
	}
	return out
}

// HasBuiltinTag reports whether the name is a compiled-in tag. Tag
// names are first-class; there are no aliases.
func HasBuiltinTag(name string) bool {
	_, ok := builtinTagSpecs[name]
	return ok
}

// LookupBuiltinTagSpec returns the spec for name. Returns the zero
// value and false for unknown names.
func LookupBuiltinTagSpec(name string) (BuiltinTagSpec, bool) {
	spec, ok := builtinTagSpecs[name]
	return spec, ok
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
