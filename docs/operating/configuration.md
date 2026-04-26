# Configuration

Thane is configured via a YAML file. See
[Getting Started](getting-started.md) for where the config file lives and
`examples/config.example.yaml` for the full field reference with inline
documentation.

This guide covers the major config sections organized by concern.

## Models & Routing

```yaml
models:
  ollama_url: http://localhost:11434
  default: qwen2.5:20b
  available:
    - name: qwen2.5:20b
      provider: ollama
      quality: 6
      speed: 7
      cost_tier: 1
    - name: claude-sonnet-4-20250514
      provider: anthropic
      quality: 9
      speed: 5
      cost_tier: 4
```

**`ollama_url`** — Where your Ollama instance lives. Required.

**`default`** — The model used when no routing profile specifies otherwise.

**`available`** — List of models Thane can choose from. Each model has a
quality score (1-10), speed score (1-10), and cost tier (1-5). The router
uses these scores plus routing hints to select the best model for each
request. You don't hardcode which model handles which task — you describe
the models, and Thane's router does the matching.

See [Routing Profiles](routing-profiles.md) for how profiles map to model
selection.

## Anthropic (Cloud Models)

```yaml
anthropic:
  api_key: sk-ant-...
```

Optional. Enables cloud models for complex reasoning. Without this, Thane
runs entirely on local models.

## Home Assistant

```yaml
homeassistant:
  url: http://homeassistant.local:8123
  token: your_long_lived_access_token
```

Both fields are required. The token needs access to the entities and
services you want Thane to interact with. See
[Home Assistant](homeassistant.md) for setup details.

## MQTT

```yaml
mqtt:
  broker: tcp://homeassistant.local:1883
  username: thane
  password: your_password
  wake_subscriptions:
    - topic: frigate/events
```

Required. See [MQTT](mqtt.md) for broker setup, telemetry entities, and
wake subscriptions.

## Email

```yaml
email:
  accounts:
    - name: primary
      imap:
        host: imap.example.com
        port: 993
        username: thane@example.com
        password: app_password
      smtp:
        host: smtp.example.com
        port: 587
        username: thane@example.com
        password: app_password
      owner_email: you@example.com    # Bcc for audit trail
```

Optional. Multiple accounts supported. Each account has independent IMAP
and SMTP settings. The `owner_email` receives Bcc copies of all outbound
email for governance.

Email polling is configured in the scheduler section (see below).

## Signal Messaging

```yaml
signal:
  enabled: true
  socket: /var/run/signal-cli/socket
```

Optional. Requires [signal-cli](https://github.com/AsamK/signal-cli)
running as a daemon with JSON-RPC over Unix socket.

## Contacts & CardDAV

```yaml
contacts:
  carddav:
    enabled: true
    port: 8843
    username: thane
    password: your_password
```

The contact directory is always active. The CardDAV server is optional —
enable it to sync contacts with macOS/iOS/Thunderbird.

## Companion Apps

```yaml
companion:
  enabled: true
  providers:
    nugget:
      tokens:
        - your-shared-token
```

Optional. Companion apps connect inward to Thane and expose local host
capabilities, such as macOS Calendar access from
[thane-agent-macos](https://github.com/nugget/thane-agent-macos).
Use `companion:` for new configs. Older `platform:` configs still load as
a compatibility alias.

## Capability Tags

```yaml
capability_tags:
  project_ops:
    description: "Project-specific research and archive tools"
    tools: [archive_search, web_search]
```

Thane ships with a compiled-in capability-tag catalog for native and
provider-discovered tools. Most operators leave built-in tags out of
config. Use `capability_tags` only for deliberate overrides or custom
tags. Tags with `always_active: true` are always loaded. Others activate
on demand. See [The Agent Loop](../understanding/agent-loop.md).

## Channel Tags

```yaml
channel_tags: {}
```

`channel_tags` pins broad optional capability families for requests from
a source. Use it for coarse source defaults only. Runtime facts such as
current message-channel affordances and owner identity are asserted by
the integrations that know them, not configured here. Do not put
`message_channel` or `owner` in `channel_tags`; Thane skips those
runtime-only tags from source defaults.

## Memory & Storage

```yaml
data_dir: ~/Thane/data
```

Where SQLite databases live (`thane.db`, `facts.db`). Defaults to
`~/Thane/data`.

## Document Roots

```yaml
workspace:
  path: ~/Thane

paths:
  kb: ~/Thane/knowledge
  scratchpad: ~/Thane/scratchpad
  dossiers: ~/Vaults/private-dossiers

doc_roots:
  kb:
    authoring: managed
    git:
      enabled: true
      sign_commits: true
      verify_signatures: warn
      signing_key: ~/.ssh/id_ed25519
  dossiers:
    authoring: read_only
  scratchpad:
    indexing: false
    authoring: managed
```

Any directory listed in `paths:` becomes a named local collection that
Thane can keep track of.

Use `paths:` to name the collection. Use `doc_roots:` only when that
collection needs policy: whether Thane indexes it, whether managed
document tools may write to it, and whether writes should go through a
signed git history.

`authoring` accepts `managed`, `read_only`, or `restricted`. `managed`
is the default and allows document tools and loop-declared output tools
to write the root. `read_only` blocks managed writes. `restricted`
reserves the root for narrower future flows.

`git.sign_commits` turns each managed document write/delete into a
signed git commit. By default the root itself is the repository; set
`git.repo_path` when several roots live under one larger repo. Set
`git.allowed_signers` to use an existing OpenSSH allowed signers file;
otherwise Thane writes a repository-local `.allowed_signers` file from
the signing key.

Good uses for custom roots:

- knowledge bases
- scratch work you want to revisit
- generated reports
- dossiers
- imported research notes

See [Document Roots](../understanding/document-roots.md) for the fuller
operator guide.

## Persona & Talents

```yaml
persona_file: ~/Thane/persona.md
talents_dir: ~/Thane/talents
inject_files:
  - ~/Thane/IDENTITY.md
  - ~/Thane/USER.md
```

See [Context Layers](../understanding/context-layers.md) for how these
fit into the system prompt.

## Scheduler

```yaml
scheduler:
  tasks:
    email_poll:
      cron: "*/5 * * * *"
      message: "Check for new email"
    periodic_reflection:
      cron: "0 3 * * *"
      message: "Daily reflection"
      model: claude-sonnet-4-20250514
      routing:
        quality_floor: 7
```

Cron-style task scheduling. Each task can override the model and routing
hints. See [Event Sources](../reference/event-sources.md) for how scheduled
tasks integrate with the agent loop.

## Shell Execution

```yaml
shell_exec:
  enabled: true
  denied_patterns:
    - "rm -rf"
    - "sudo"
  allowed_prefixes:
    - "ls"
    - "cat"
    - "grep"
```

Optional. Controls the `exec` tool. Denied patterns are checked first
(block), then allowed prefixes (permit). If neither matches, the command
is blocked by default.

## Logging

```yaml
logging:
  root: ~/Thane/logs
  level: info
  stdout:
    enabled: true
    level: info
  datasets:
    events:
      enabled: true
    requests:
      enabled: true
    access:
      enabled: false
    loops:
      enabled: true
    delegates:
      enabled: true
    envelopes:
      enabled: true
```

Thane writes append-only JSONL datasets under `logging.root`, partitioned by
dataset/date/hour. `stdout` is a separate operator-facing surface, so high-volume
request and access chatter can be retained on disk without polluting live logs.
`logs.db` remains the query/index layer, but the dataset files are the primary
filesystem record.

## MCP Servers

```yaml
mcp:
  servers:
    - name: home-assistant
      transport: stdio
      command: /opt/homebrew/bin/uvx
      args: ["ha-mcp@latest"]
      env:
        - "HOMEASSISTANT_URL=https://homeassistant.local"
        - "HOMEASSISTANT_TOKEN=your_token"
      include_tools:
        - ha_search_entities
        - ha_get_state
        - ha_call_service
```

Optional. Extends Thane's capabilities via the Model Context Protocol.
See [Delegation & MCP](../understanding/delegation.md).

## Delegation

```yaml
delegation:
  profiles:
    general:
      quality_floor: 5
      prefer_speed: true
    ha:
      quality_floor: 4
      prefer_speed: true
```

Controls how delegated tasks are routed.
See [Delegation](../understanding/delegation.md).

## Listen Addresses

```yaml
listen:
  address: "0.0.0.0"
  port: 8080
  ollama_port: 11434
```

Network binding for the API servers. Default is localhost-only; set to
`0.0.0.0` to accept connections from other hosts.
