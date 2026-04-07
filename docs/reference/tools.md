# Tools Reference

Thane provides 80+ native tools organized by capability tag. Tools are only
available when their tag is active — see
[The Agent Loop](../understanding/agent-loop.md) for how tags work.

Tools marked `always` are available regardless of tag state.

## Home Assistant (`ha`)

| Tool | Description |
|------|-------------|
| `control_device` | Natural language device control with fuzzy entity matching |
| `find_entity` | Smart entity discovery across all HA domains |
| `get_state` | Current state of any entity |
| `list_entities` | Browse entities by domain or pattern |
| `call_service` | Direct HA service invocation |
| `add_context_entity` | Add entity to the state watchlist |
| `remove_context_entity` | Remove entity from the state watchlist |
| `ha_notify` | HA companion app push notification |

## Email (`email`)

| Tool | Description |
|------|-------------|
| `email_list` | List messages in a folder |
| `email_read` | Read message with full body |
| `email_search` | Server-side IMAP search |
| `email_folders` | List available mailboxes |
| `email_mark` | Flag/unflag messages |
| `email_send` | Compose and send (markdown to MIME) |
| `email_reply` | Reply with threading headers |
| `email_move` | Move messages between folders |

## Contacts (`contacts`)

| Tool | Description |
|------|-------------|
| `save_contact` | Create or update contacts with vCard properties |
| `lookup_contact` | Search by name, query, kind, or property |
| `forget_contact` | Delete a contact |
| `list_contacts` | List and filter contacts |
| `export_vcf` | Export contact as vCard |
| `export_vcf_qr` | Export contact as vCard QR code |
| `export_all_vcf` | Bulk vCard export |
| `import_vcf` | Import vCard data |

## Notifications (`always`)

| Tool | Description |
|------|-------------|
| `send_notification` | Provider-agnostic fire-and-forget notification |
| `request_human_decision` | Actionable notification with HITL callbacks |

## Media & Feeds (`media`)

| Tool | Description |
|------|-------------|
| `media_transcript` | Fetch video/podcast transcript via yt-dlp |
| `media_follow` | Follow an RSS/Atom feed or YouTube channel |
| `media_unfollow` | Stop following a feed |
| `media_feeds` | List followed feeds with status |
| `media_save_analysis` | Save analysis to Obsidian vault |

## Memory & Knowledge (`memory`, `always`)

| Tool | Tag | Description |
|------|-----|-------------|
| `remember_fact` | memory | Store knowledge with embeddings |
| `recall_fact` | memory | Retrieve knowledge by category or search |
| `forget_fact` | memory | Remove stored knowledge |

## Archive (`always`)

| Tool | Description |
|------|-------------|
| `archive_search` | Full-text search across conversation history |
| `archive_sessions` | Browse session archive |
| `archive_session_transcript` | Retrieve full session transcript |

## Session (`always`)

| Tool | Description |
|------|-------------|
| `session_working_memory` | Read/write scratchpad for active session |
| `session_close` | Close session with carry-forward context |
| `session_checkpoint` | Save current session state |
| `session_split` | Fork the current session |
| `conversation_reset` | Reset conversation context |

## Planning (`planning`)

| Tool | Description |
|------|-------------|
| `schedule_task` | Time-based future actions |
| `list_tasks` | List scheduled tasks |
| `cancel_task` | Cancel a scheduled task |

## Capabilities (`always`)

| Tool | Description |
|------|-------------|
| `activate_capability` | Activate capability tags for current conversation |
| `deactivate_capability` | Deactivate capability tags |
| `activate_lens` | Activate a persistent global behavioral lens |
| `deactivate_lens` | Deactivate a global behavioral lens |
| `list_lenses` | List active behavioral lenses |

## Forge / GitHub (`forge`)

| Tool | Description |
|------|-------------|
| `forge_issue_create` | Create an issue |
| `forge_issue_get` | Get issue details |
| `forge_issue_list` | List issues with filters |
| `forge_issue_update` | Update issue fields |
| `forge_issue_comment` | Comment on an issue |
| `forge_pr_list` | List pull requests |
| `forge_pr_get` | Get PR details |
| `forge_pr_diff` | Get PR diff |
| `forge_pr_files` | List changed files |
| `forge_pr_commits` | List PR commits |
| `forge_pr_reviews` | List PR reviews |
| `forge_pr_review` | Submit a review |
| `forge_pr_review_comment` | Comment on specific code |
| `forge_pr_checks` | Get CI check status |
| `forge_pr_merge` | Merge a PR |
| `forge_pr_request_review` | Request reviewers |
| `forge_react` | Add emoji reaction |
| `forge_search` | Search code and issues |

## Web (`web`)

| Tool | Description |
|------|-------------|
| `web_search` | Search via SearXNG or Brave |
| `web_fetch` | Extract readable content from URLs |

## Files (`files`)

| Tool | Description |
|------|-------------|
| `file_read` | Read file contents |
| `file_write` | Write file contents |
| `file_edit` | Edit file with diff |
| `file_list` | List directory contents |
| `file_search` | Search for files by name |
| `file_grep` | Search file contents with regex |
| `file_stat` | Get file metadata |
| `file_tree` | Directory tree view |
| `create_temp_file` | Create a temporary file |

## Shell (`shell`)

| Tool | Description |
|------|-------------|
| `exec` | Host shell command execution (configurable safety guardrails) |

## Delegation (`always`)

| Tool | Description |
|------|-------------|
| `thane_delegate` | Delegate tasks to local models |

## Utility (`always`)

| Tool | Description |
|------|-------------|
| `get_version` | Agent version and build info |
| `cost_summary` | Token usage and cost summary |

## MCP Tools

Thane hosts MCP servers as subprocesses, bridging their tools into the agent
loop. MCP tools appear as `mcp_{server}_{tool}` and are assigned to
capability tags in config.

The primary MCP server is [ha-mcp](https://github.com/karimkhaleel/ha-mcp),
which provides 90+ Home Assistant tools. With `include_tools` filtering, you
select which tools to bridge.

See [Delegation & MCP](../understanding/delegation.md) for configuration
details.
