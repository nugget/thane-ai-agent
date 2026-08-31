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

**`default`** — The model used when no virtual model specifies otherwise.

**`available`** — List of models Thane can choose from. Each model has a
quality score (1-10), speed score (1-10), and cost tier (1-5). The router
uses these scores plus routing hints to select the best model for each
request. You don't hardcode which model handles which task — you describe
the models, and Thane's router does the matching.

See [Virtual Models](routing-profiles.md) for how virtual models map to
model selection.

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
  ingest_rate_limit_per_minute: 12  # optional: cap on state-change events ingested per entity per minute
  # registry_cache_ttl and floor_alias are also optional — see homeassistant.md
```

`url` and `token` are required; the token needs access to the entities and
services you want Thane to interact with.

As of v0.10.2 the former `homeassistant.subscribe` block is retired — a stale
`subscribe:` key will fail the boot. Its `rate_limit_per_minute` moved to the
top-level `ingest_rate_limit_per_minute`, and entity globs are no longer a
config concept: they are runtime ingest-mode subscriptions, declared with
`add_entity_subscription` (or the model's `watch_entity`) after boot. See
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
carddav:
  enabled: true
  listen:
    - 127.0.0.1:8843
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
    alice:
      tokens:
        - your-shared-token
      contact: 0d1f8a6e-4c2b-4b7e-9f00-3a7d0e2c9b41
```

Optional. Companion apps connect inward to Thane and expose local host
capabilities, such as macOS Calendar access from
[thane-agent-macos](https://github.com/nugget/thane-agent-macos).

`contact` (optional) binds every device that authenticates through the
account to one contact record, by contact UUID — the counterparty
layer's person attribution. Devices then present and report as that
contact's devices, and inherit authority from the contact's trust zone
at read time, so a zone change reaches every bound device immediately.
Configuration is deliberately the only place this binding can be made:
bindings confer inherited trust, and neither they nor trust zones are
writable through model-facing contact tools. A malformed (non-UUID)
value is rejected at config load; a UUID that matches no contact fails
closed at render time — the devices degrade to account-only
attribution and a warning names the unresolved binding.

The second counterparty edge binds a contact to its Home Assistant
person entity, through which zone state and separately resolved,
provider-attributed room presence flow into that contact's channel
context. Signed config is the preferred source of truth:

```yaml
identity:
  operator_contact_id: 019c76e4-2ff1-7918-8d6f-6c2488f5098d

person:
  track:
    - person.nugget
  contact_bindings:
    019c76e4-2ff1-7918-8d6f-6c2488f5098d: person.nugget
```

`identity.operator_contact_id` says which stable contact is the human
operator; it does not infer that authority from presence. The legacy
`identity.owner_contact_name` selector remains accepted for existing
installations, but it is mutually exclusive with the UUID selector.
When neither is configured, Thane falls back to the sole `admin`
contact only when exactly one exists. New `thane init` workspaces create
an `admin`-zone `Operator` contact stub, write its UUID into the signed
config, and start with an explicit empty `person.contact_bindings` map.

`person.contact_bindings` is an exact contact-UUID-to-person-entity map.
Every referenced contact must exist at startup, every person entity must
also appear under `person.track`, and a person may belong to only one
active contact. When the key is present — even as `{}` — startup
atomically reconciles the stored set to config. CardDAV still emits
`X-THANE-HA-PERSON`, but treats it as a read-only projection and preserves
it when clients omit unknown vCard fields. Removing a map entry clears
that binding on the next restart.

For backward compatibility, omitting `person.contact_bindings` entirely
keeps the earlier CardDAV-managed behavior: an authenticated PUT may set
or clear `X-THANE-HA-PERSON`. Model-facing vCard import never honors that
field in either mode.

The person tracker follows each person's Home Assistant `device_trackers`
attribute and adds those linked entities to the system ingestion floor. A
linked tracker contributes room presence only when its entity-registry
platform is `bermuda`, its state is `home`, and its `area` attribute is a
non-empty string. The tracker entity ID remains the stable observation identity;
a non-empty human-readable `scanner` attribute is surfaced as source evidence,
while missing scanner evidence stays empty rather than exposing the tracker ID.
The configured UniFi poller remains a second room provider and uses the AP name
as its evidence. Observations that agree resolve to one room, while disagreement
reports `room_conflict` and withholds a guessed room.
Use `companion:`. A top-level `platform:` section is rejected at config
load with an actionable error and must be renamed to `companion:` (the
field shape is unchanged).

## Capability Tags

```yaml
capability_tags:
  project_ops:
    description: "Project-specific research and archive tools"
    include: [archive_search, web_search]
```

Thane ships with a compiled-in capability-tag catalog for native and
provider-discovered tools. Most operators leave built-in tags out of
config. Use `capability_tags` only for deliberate overrides or custom
tags. Tags with `core: true` are always loaded. Others activate
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
roots:
  kb:
    path: ~/Thane/knowledge
    authoring: managed
    git:
      enabled: true
      sign_commits: true
      verify_signatures: warn
      signing_key: ~/.ssh/id_ed25519
  dossiers:
    path: ~/Vaults/private-dossiers
    authoring: read_only
  scratchpad:
    path: ~/Thane/scratchpad
    indexing: false
    authoring: managed
```

Each entry under `roots:` names one local collection Thane keeps track
of, combining its `path` with optional per-root policy: whether Thane
indexes it, whether managed document tools may write to it, and whether
writes should go through a signed git history. An entry may be a bare
string when it needs no policy beyond its path:

```yaml
roots:
  kb: ~/Thane/knowledge
```

> **Deprecated:** the older `paths:` / `doc_roots:` split — one block to
> name the path, a second to attach policy — is still parsed but emits a
> deprecation warning and cannot appear in the same config as `roots:`.
> Migrate each `paths:` entry and its matching `doc_roots:` policy into a
> single `roots:` entry.

`authoring` accepts `managed`, `read_only`, or `restricted`. `managed`
is the default and allows document tools and loop-declared output tools
to write the root. `read_only` blocks managed writes. `restricted`
reserves the root for narrower future flows.

Forge-maintained source checkouts use the same named-root resolver but do
not need a `roots:` entry. Pass a handle such as `repo_root: thanecode` to
`forge_repo_follow`; Thane derives a checkout location beneath
`workspace.path`, clones it before returning, and registers `thanecode:` as
a read-only repository root. The host path remains internal. File tools can
then read `thanecode:internal/app/new.go`, while the scoped `repo_git_*`
tools expose commit history without shell access. Repository roots are not
document corpora: they are neither indexed nor subjected to document
signature policy.

A repository root only stays current while repository polling runs. Set
`forge.subscription_check_interval` to a positive number of seconds. When
polling is disabled the initial clone still succeeds, but the root remains
at that snapshot and the subscription wakes no loop; the tool response
calls this out.

`git.sign_commits` turns each managed document write/delete into a
signed git commit. By default the root itself is the repository; set
`git.repo_path` when several roots live under one larger repo. Thane
uses the repository-local `.allowed_signers` file for SSH signature
verification. Thane creates that file once, when the root is first
established, from the agent key plus the root's declared `seed_signers`;
after that the file is the root's own trust surface and config never
rewrites it.

A root that signs commits must declare `seed_signers` — the keys
entitled to establish it. At boot, a git-backed root under
`verify_signatures: warn` or `required` must prove its birth is
attributable to one of them, and that every change to `.allowed_signers`
was signed by one of them. A root founded by the agent must therefore
declare `thane@provenance.local` with the public half of its
`git.signing_key`; omitting that principal is how a root declares that
the agent may not establish or amend it. Failures report through the
root's own `verify_signatures` policy and name the `admission` check.

`git.verify_signatures` controls read-side enforcement. `none` disables
checks, `warn` logs and reports verification failures without blocking,
and `required` blocks managed document reads, indexed browse/search
results, and tagged context injection unless the content is cleanly
covered by trusted signed git history.

Good uses for custom roots:

- knowledge bases
- scratch work you want to revisit
- generated reports
- dossiers
- imported research notes

See [Document Roots](../understanding/document-roots.md) for the fuller
operator guide.

## Persona & Talents

Talents have no config key. They load from `{workspace}/core/talents/`,
a location derived from the workspace rather than authored, so the
prose that steers model behavior every turn is covered by the same
signed history and cleanliness rule as the config beside it. Editing a
talent means committing it: an uncommitted talent file fails the boot
gate exactly as an uncommitted `config.yaml` does, and every talent
must declare `tags:` (`always`, `persona`, or the capability tags that
select it).

A config that still declares the retired `talents_dir` key is rejected
at load with the migration recipe: move the directory into core
(`mv <talents_dir> {workspace}/core/talents`), commit it, and remove
the `talents_dir:` line. An instance that skips the move boots with an
empty talent set and a startup warning naming the missing directory.

See [Context Layers](../understanding/context-layers.md) for how these
fit into the system prompt. The workspace derives the protected
`core` and `self` roots; `core/axioms.md`, `core/persona.md`, and
`core/mission.md` are picked up by the runtime without a separate
inject-file list, as is `self/ego.md` — what Thane has made of them,
read beside them but written under the agent's own signer policy.

## Scheduler

```yaml
scheduler:
  tasks:
    email_poll:
      cron: "*/5 * * * *"
      message: "Check for new email"
```

Cron-style task scheduling. Each task can override the model and routing
hints. See [Event Sources](../reference/event-sources.md) for how scheduled
tasks integrate with the agent loop.

Self-reflection (`ego.md` maintenance) runs as the `ego` service loop,
not as a scheduled task. See the `ego:` block in the example config for
sleep bounds, supervisor randomization, and routing.

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
  root: ~/Thane/archive
  level: info
  stdout:
    enabled: true
    level: info
  datasets:
    events:
      enabled: true
    requests:
      enabled: true
    http_access:
      enabled: false
    loops:
      enabled: true
    delegates:
      enabled: true
    envelopes:
      enabled: true
    conversations:
      enabled: false
```

Thane writes append-only JSONL datasets under `logging.root`, partitioned by
dataset/date/hour. `stdout` is a separate operator-facing surface, so high-volume
request and access chatter can be retained on disk without polluting live logs.
`logs.db` remains the query/index layer, but the dataset files are the primary
filesystem record.

For live production monitoring, `just alerts ~/Thane WARN 2` follows new
warnings and errors from `logs.db` across all datasets. Use `ERROR` instead of
`WARN` to suppress warnings. The output is newline-delimited compact JSON and
can be piped through `grep` or `jq`.


## MCP Servers

```yaml
mcp:
  servers:
    - name: github
      transport: stdio
      command: docker
      args: ["run", "-i", "--rm", "-e", "GITHUB_PERSONAL_ACCESS_TOKEN", "ghcr.io/github/github-mcp-server"]
      env:
        - "GITHUB_PERSONAL_ACCESS_TOKEN=your_token"
      include_tools:
        - search_code
        - issue_read
```

Optional. Extends Thane's capabilities via the Model Context Protocol.
See [Delegation & MCP](../understanding/delegation.md).

## Delegation

```yaml
delegate:
  profiles:
    general:
      tool_timeout: 8m
      max_duration: 15m
    ha:
      tool_timeout: 3m
      max_duration: 5m
```

Controls how delegated tasks are routed.
See [Delegation](../understanding/delegation.md).

## Listen Addresses

```yaml
listen:
  address: "0.0.0.0"
  port: 8080

ollama_api:
  enabled: true
  address: ""
  port: 11434

openai_api:
  enabled: true
  address: ""
  port: 8081
```

Network binding for the API servers. `listen:` binds the native Thane
/v1 API and web dashboard (default port 8080). The optional
Ollama-compatible (port 11434) and OpenAI-compatible (port 8081) shims
bind separately under their own blocks. Default is localhost-only; set
`address` to `0.0.0.0` to accept connections from other hosts.
