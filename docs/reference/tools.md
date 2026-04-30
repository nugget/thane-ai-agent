# Tools Reference

Thane provides ~130 native tools organized by capability tag. A tool is
available to a given turn only when one of its default tags is active —
see [The Agent Loop](../understanding/agent-loop.md) for how tags flip
on and off, and [Anthropic Caching](../anthropic-caching.md) for how
tag choices interact with prompt caching.

Tools under the `always` heading below are not tag-gated and load on
every turn. Everything else loads only when a relevant capability is
activated, either by the model via `activate_capability` or via
configured always-active tags.

The authoritative source for which tool is tagged how is
[`internal/model/toolcatalog/catalog.go`](../../internal/model/toolcatalog/catalog.go);
this doc is a human-readable reflection of that catalog. If you add or
re-tag a tool there, update this file in the same PR.

## Coarse menu tags

`development`, `home`, `interactive`, `knowledge`, `media`,
`operations`, `people` are coarse navigation tags rather than tool
containers. They carry short teasers that point the model at the
fine-grained tags that actually own the tools. Activating any coarse
tag surfaces its teaser; the model then activates the fine tag it
needs.

## Always-active

These tools load on every turn regardless of active tags.

| Tool | Description |
|------|-------------|
| `activate_capability` | Activate a capability tag for the current conversation. |
| `deactivate_capability` | Deactivate a capability tag for the current conversation. |
| `activate_lens` | Activate a persistent global behavioural lens. |
| `deactivate_lens` | Deactivate a global behavioural lens. |
| `list_lenses` | List currently active behavioural lenses. |
| `thane_now` | Synchronously delegate a bounded task and return the result inline. |
| `thane_assign` | Assign a task to a sub-agent that runs in the background and reports back when complete. |
| `thane_delegate` | DEPRECATED compatibility alias routing to `thane_now` (sync) or `thane_assign` (async) based on a `mode` parameter. |
| `archive_search` | Full-text search across conversation archives. |
| `archive_sessions` | Browse session archive metadata. |
| `archive_session_transcript` | Retrieve a full session transcript. |
| `session_checkpoint` | Save current session state as a checkpoint. |
| `session_close` | Close the current session with carry-forward context. |
| `session_split` | Fork the current session. |
| `conversation_reset` | Reset the current conversation's message history. |
| `send_notification` | Provider-agnostic fire-and-forget notification. |

The delegate family (`thane_now`, `thane_assign`, and the deprecated
`thane_delegate` alias) uses capability tags as its primary tool and
context scope. Delegates inherit elective caller tags by default so
child work keeps the same task context; explicit `tags` override
profile default tags.
Use root entry-point tags such as `development`, `home`, `operations`,
`knowledge`, `media`, `interactive`, or `people` when the delegate should
read the menu guidance and choose a narrower branch. Use leaf tags such
as `ha`, `files`, `forge`, `web`, `loops`, `documents`, or `diagnostics`
when the caller already knows the needed toolset.
Runtime and channel affordance tags such as `owner` and `message_channel`
are re-asserted only from trusted runtime context; they are not inherited
as model-requested tags. Use `inherit_caller_tags: false` when a delegate
needs a strict fresh tool scope.

## `awareness` — live-context entity management

| Tool | Description |
|------|-------------|
| `add_context_entity` | Watch an HA entity so its state is injected into context each turn. |
| `list_context_entities` | List current watchlist subscriptions (scoped and always-visible). |
| `remove_context_entity` | Remove a watched entity or a scoped subscription. |

Subscription expiry is reported as `expires_delta`, not a raw timestamp,
so the model does not need to do clock arithmetic.

For `weather.*` subscriptions, `add_context_entity` accepts
`forecast: daily`, `forecast: hourly`, or `forecast: twice_daily` to
fetch Home Assistant forecast response data each turn and include a
compact forecast in the injected entity context. Use `forecast: none` to
clear forecast fetching for that subscription.

## `ha` / `homeassistant` — Home Assistant state and control

| Tool | Description |
|------|-------------|
| `control_device` | Natural-language device control with fuzzy entity matching. |
| `find_entity` | Smart entity discovery across HA domains. |
| `get_state` | Current state of any entity. |
| `list_entities` | Browse entities by domain or glob pattern. |
| `call_service` | Direct HA service invocation. |
| `ha_registry_search` | Search the entity/device/area registry. |
| `ha_automation_list` | List automations with recent activation counts. |
| `ha_automation_get` | Retrieve one automation's configuration. |
| `ha_automation_create` | Create a new automation. |
| `ha_automation_update` | Modify an existing automation. |
| `ha_automation_delete` | Delete an automation. |

## `notifications` — delivery, escalation, and actionable responses

| Tool | Description |
|------|-------------|
| `ha_notify` | HA companion-app push notification. |
| `request_human_decision` | Actionable notification with a human-in-the-loop callback. |
| `request_human_escalation` | Escalate to a human with a synchronous response wait. |
| `request_ai_escalation` | Escalate to another agent/model with a synchronous response wait. |
| `resolve_actionable` | Mark an actionable notification as resolved. |

## `memory` — persistent facts and working memory

| Tool | Description |
|------|-------------|
| `remember_fact` | Store knowledge with optional embeddings. |
| `recall_fact` | Retrieve knowledge by category or semantic search. |
| `forget_fact` | Remove a stored fact. |
| `session_working_memory` | Read/write scratchpad for the active session. |

## `documents` — indexed document-root browsing

Managed document roots (`core:`, `kb:`, `generated:`, `scratchpad:`,
custom prefixes) are browsed through these tools rather than via raw
filesystem access. See
[Document Roots](../understanding/document-roots.md).

`doc_roots` includes each root's policy summary: indexing status,
authoring mode, git/signature expectations, and current verification
health. Mutation tools obey that policy before writing; for example,
`read_only` roots reject managed writes and git-backed roots route
writes through signed commits. Read/search surfaces also respect
`verify_signatures: required` by blocking content that is not cleanly
covered by trusted signed git history.

Document tool results use model-facing time deltas such as
`modified_delta`, `created_delta`, and `checked_delta` instead of raw
absolute timestamps. Timestamp filter inputs still accept RFC3339 values
or signed deltas like `-604800s`.

| Tool | Description |
|------|-------------|
| `doc_roots` | List configured document roots with policy, health, and counts. |
| `doc_browse` | Walk a document root by folder. |
| `doc_outline` | Emit the heading/section outline for a document. |
| `doc_read` | Read a document by prefixed path. |
| `doc_section` | Retrieve a named section from a document. |
| `doc_search` | Full-text and tagged search across roots. |
| `doc_links` | List inbound/outbound links for a document. |
| `doc_values` | List frontmatter values (tags, statuses, etc.) across a root. |
| `doc_intake` | Analyze proposed knowledge against the existing corpus before writing it. |
| `doc_commit` | Commit an approved `doc_intake` result through managed mutations. |
| `doc_write` | Write or replace a document. |
| `doc_edit` | Targeted edit within a document. |
| `doc_copy` | Copy a document to another location. |
| `doc_move` | Move or rename a document. |
| `doc_delete` | Delete a document. |
| `doc_copy_section` | Copy one named section into another document. |
| `doc_move_section` | Move one named section into another document. |
| `doc_journal_update` | Append or update a journal-style entry. |

Loop-declared document output tools are request-scoped and do not appear
in the global catalog above. When a loop declares a maintained document
or journal document output, Thane generates tools such as
`replace_output_metacognitive_state` or `append_output_daily_notes` only
for that loop run. These tools route through managed document roots, so
root policy, indexing, and provenance remain centralized instead of
being reimplemented in each loop prompt.

## `email` — inbox traffic

| Tool | Description |
|------|-------------|
| `email_list` | List messages in a folder. |
| `email_read` | Read a message with its full body. |
| `email_search` | Server-side IMAP search. |
| `email_folders` | List available mailboxes. |
| `email_mark` | Flag or unflag messages. |
| `email_send` | Compose and send (markdown → MIME). |
| `email_reply` | Reply with proper threading headers. |
| `email_move` | Move messages between folders. |

## `contacts` — directory and vCard administration

| Tool | Description |
|------|-------------|
| `save_contact` | Create or update a contact with vCard properties. |
| `lookup_contact` | Search by name, query, kind, or property. |
| `forget_contact` | Delete a contact. |
| `list_contacts` | List and filter contacts. |
| `export_vcf` | Export one contact as a vCard. |
| `export_vcf_qr` | Export one contact as a vCard QR code. |
| `export_all_vcf` | Bulk vCard export. |
| `import_vcf` | Import one or more vCards. |

## `owner` — trusted operator context

| Tool | Description |
|------|-------------|
| `owner_contact` | Return the runtime owner identity. Protected tag. |

Owner channel activity recency is reported with delta fields such as
`last_active_delta`.

## `files` — workspace filesystem access

| Tool | Description |
|------|-------------|
| `file_read` | Read file contents. |
| `file_write` | Write file contents. |
| `file_edit` | Targeted edit with a diff preview. |
| `file_list` | List directory contents. |
| `file_search` | Search for files by name. |
| `file_grep` | Search file contents with regex. |
| `file_stat` | Get file metadata. |
| `file_tree` | Render a directory tree. |
| `create_temp_file` | Create a temp file with a labelled path. |

`file_stat` reports modification recency as `modified_delta`.

## `shell` — host command execution

| Tool | Description |
|------|-------------|
| `exec` | Run a host shell command with configurable allow/deny guardrails. |

## `web` — web search and fetch

| Tool | Description |
|------|-------------|
| `web_search` | Search via the configured backend (SearXNG/Brave). |
| `web_fetch` | Extract readable content from a URL. |

## `media` — transcript and analysis

| Tool | Description |
|------|-------------|
| `media_transcript` | Fetch a video/podcast transcript via yt-dlp. Also tagged `web`. |
| `media_save_analysis` | Save a media analysis to the configured vault with generated-document provenance. |

## `feeds` — RSS/Atom and channel subscriptions

| Tool | Description |
|------|-------------|
| `media_follow` | Follow an RSS/Atom feed or YouTube channel. |
| `media_unfollow` | Stop following a feed. |
| `media_feeds` | List followed feeds and their status. |

## `attachments` — vision pipeline

| Tool | Description |
|------|-------------|
| `attachment_list` | List known attachments with metadata. |
| `attachment_search` | Semantic or tag search over attachment descriptions. |
| `attachment_describe` | Produce/refresh a vision description for an attachment. |

Attachment list and search results report arrival recency as
`received_delta`.

## `forge` — GitHub/code collaboration

| Tool | Description |
|------|-------------|
| `forge_issue_list` | List issues with filters. |
| `forge_issue_get` | Get an issue's details. |
| `forge_issue_create` | Create an issue. |
| `forge_issue_update` | Update issue fields. |
| `forge_issue_comment` | Comment on an issue. |
| `forge_pr_list` | List pull requests. |
| `forge_pr_get` | Get a PR's details. |
| `forge_pr_diff` | Retrieve a PR's diff. |
| `forge_pr_files` | List changed files in a PR. |
| `forge_pr_commits` | List commits in a PR. |
| `forge_pr_reviews` | List reviews on a PR. |
| `forge_pr_review` | Submit a review. |
| `forge_pr_review_comment` | Comment on a specific line in a PR. |
| `forge_pr_merge` | Merge a PR. |
| `forge_pr_request_review` | Request reviewers on a PR. |
| `forge_react` | Add an emoji reaction to an issue/PR/comment. |
| `forge_search` | Search code and issues across the forge. |

## `scheduler` — time-based tasks

| Tool | Description |
|------|-------------|
| `schedule_task` | Schedule a future task. |
| `list_tasks` | List scheduled tasks. |
| `cancel_task` | Cancel a scheduled task. |

Task next-run values include a model-facing delta.

## `thane_*` family — intent-shaped front door for "do work"

Always-available (`thane_now`, `thane_assign`) plus loops-tagged
(`thane_curate`, `thane_wake`). Pick by lifecycle. `thane_delegate`
and `notify_loop` remain as deprecated aliases that route into the
family.

| Tool | Lifecycle | Description |
|------|-----------|-------------|
| `thane_now` | sync | Synchronously delegate a bounded task and return the result inline. |
| `thane_assign` | async one-shot | Assign a task to a sub-agent that runs in the background and reports back through the current conversation/channel when complete. |
| `thane_curate` | recurring | Scaffold a managed document and launch a recurring service loop that curates it (`journal` mode appends entries; `maintain` mode rewrites idempotently). |
| `thane_wake` | poke existing | Send a one-shot message envelope to a live timer loop, waking it or queueing for the next iteration. |

`thane_now`, `thane_assign`, and the deprecated `thane_delegate` alias
accept `context_mode`. The default, `task`, gives the child run a compact
task-worker prompt with active capabilities, tagged context, and current
conditions, but without full Thane identity files, inject files,
always-on talents, or conversation-history dressing. Use
`context_mode=full` only when the delegated work genuinely needs that
continuity.

## `loops` — lower-level loop control and inspection

Below the `thane_*` family. Use these for inspection, control, or
unusual launch shapes (event-driven, mqtt-wake-only,
supervisor-randomized metacog) where the canonical family doesn't fit.

| Tool | Description |
|------|-------------|
| `loop_status` | Snapshot of currently running loops. |
| `set_next_sleep` | From inside a service loop, request the next sleep duration. |
| `spawn_loop` | Launch an ad-hoc loop from a definition and input. |
| `stop_loop` | Stop a running loop. |
| `loop_definition_list` | List registered loop definitions. |
| `loop_definition_get` | Retrieve a loop definition's spec. |
| `loop_definition_set` | Create or update a loop definition. |
| `loop_definition_delete` | Remove a loop definition. |
| `loop_definition_lint` | Validate a proposed loop-definition spec. |
| `loop_definition_launch` | Launch a persistent loop from a definition. |
| `loop_definition_set_policy` | Update a loop definition's lifecycle policy. |
| `loop_definition_summary` | Summary view across definitions. |
| `notify_loop` | DEPRECATED alias for `thane_wake`. |
| `thane_delegate` | DEPRECATED alias routing to `thane_now` (sync) or `thane_assign` (async). |

## `mqtt` — wake subscriptions

See [MQTT](../operating/mqtt.md) for the broker-side conventions.

| Tool | Description |
|------|-------------|
| `mqtt_wake_list` | List runtime and config-defined wake subscriptions. |
| `mqtt_wake_add` | Add a runtime wake subscription with routing config. |
| `mqtt_wake_remove` | Remove a runtime wake subscription by ID. |

## `message_channel` — current message-app conversation

Normalized tools for the active message-app channel. Providers such as
Signal adapt these calls to their native APIs. Inbound message-app
bridges assert this capability as a runtime fact; it does not need to be
listed in `channel_tags` or contact `origin_tags`.

| Tool | Description |
|------|-------------|
| `send_reaction` | React inside the current message-app conversation. |

## `signal` — Signal messaging

Declared via a Provider with async binding; handlers return
[`tools.ErrUnavailable`](../../internal/tools/provider.go) when
signal-cli isn't connected. In inbound Signal conversations, final
response text is sent automatically by the bridge; prefer
`message_channel` for in-channel reactions and reserve these native
tools for Signal-specific workflows.

| Tool | Description |
|------|-------------|
| `signal_send_message` | Send a Signal message to a phone number. |
| `signal_send_reaction` | React to an inbound Signal message. |

## `companion` — native companion app integration

| Tool | Description |
|------|-------------|
| `macos_calendar_events` | Query the local macOS Calendar (companion app required). |

## `models` — model registry and routing

| Tool | Description |
|------|-------------|
| `model_registry_list` | List available models with capability metadata. |
| `model_registry_get` | Retrieve one model deployment's metadata. |
| `model_registry_summary` | Summary of routing policy and cost tiers. |
| `model_route_explain` | Dry-run a routing decision with the router's rationale. |
| `model_deployment_set_policy` | Update deployment-level routing policy. |
| `model_resource_set_policy` | Update resource-level routing policy. |

## `diagnostics` — operational visibility

| Tool | Description |
|------|-------------|
| `get_version` | Agent version, build info, and commit SHA. |
| `cost_summary` | Aggregated token usage and cost (uses `usage.Summary`, including `cache_hit_rate`). |
| `logs_query` | Query the structured log index with attribute filters. |

## MCP tools

Thane hosts MCP servers as subprocesses and bridges their tools into
the registry as `mcp_{server}_{tool}`. MCP tools inherit their default
tags from the MCP config's `default_tags` or configuration-side tag
overrides; they do not have a compiled-in entry in
`internal/model/toolcatalog/catalog.go`.

The primary MCP server in typical deployments is
[`ha-mcp`](https://github.com/karimkhaleel/ha-mcp), which exposes
90+ Home Assistant tools beyond Thane's native set. `include_tools`
filtering in the config narrows the bridged surface.

See [Delegation & MCP](../understanding/delegation.md) for
configuration details.
