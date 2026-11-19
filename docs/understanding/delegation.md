# Delegation & MCP

## The Problem

Frontier models are great at reasoning but expensive per token. Local models
are free but need precise instructions. Tool-heavy tasks — searching HA
entities, reading files, running commands — can burn through dozens of
iterations, each re-sending the full conversation context.

A single HA device control sequence (search, act, verify) might cost $1.50
in cloud API tokens if the frontier model does it directly.

## The Solution: Delegation

Thane's primary model **orchestrates** — it understands the request, plans
the approach, and writes precise instructions. Then it **delegates** the
tool-heavy execution to a smaller, faster local model.

```
User: "Set the office Hue Go to teal"

Orchestrator (Opus):
  -> "Delegate with the ha capability tag: search for 'hue go' entities in office,
     call light.turn_on with rgb_color [0,200,180], verify with ha_get_state"

Delegate (local 20B model):
  -> search_entities("hue go") -> found light.office_hue_go
  -> ha_call_service(light.turn_on, light.office_hue_go, rgb=[0,200,180])
  -> ha_get_state(light.office_hue_go) -> confirmed ON, correct color
  -> return result to orchestrator

Orchestrator: "Done - your Hue Go is now teal at full brightness."
```

The frontier model used ~62K input tokens for orchestration ($0.96). The
local model used ~32K tokens for execution ($0.00). Total: under a dollar,
versus $3+ if the frontier model ran every tool call directly.

## Tool Gating

When delegation is enabled (`delegation_required: true`), tools are
partitioned:

**Orchestrator sees:**
- `thane_now` — synchronous delegation; the orchestrator waits for the delegate's answer in this turn
- `thane_assign` — async one-shot; the delegate runs in the background and reports back through the conversation/channel when complete
- `remember_fact` / `recall_fact` — memory operations
- `session_working_memory` — session scratchpad
- `archive_search` — conversation history search
- `doc_search` — document metadata discovery
- `search` — discovery across the archive and document sources available in this run

Pick by lifecycle: reach for `thane_now` when the orchestrator needs the result inline to continue reasoning, `thane_assign` when the work is fire-and-forget and a later message is acceptable.

**Delegates see tools** through their capability tags:
- HA-tagged native and MCP tools for device control or entity queries
- Web, file, shell, document, and other tool families when those tags are active
- Core tools configured for the instance

The `thane:ops` [virtual model](../operating/routing-profiles.md) disables
orchestrator gating — the primary model sees everything directly. Use it when
you need the frontier model's judgment on tool results, not just
orchestration.

## Talents as Knowledge Bridge

The orchestrator model doesn't see tool definitions directly (they're gated).
Instead, **talent files** teach it what's available and how to write effective
delegation prompts.

The `talents/delegation.md` talent contains:
- Which capability tags and tool families exist
- Patterns to follow (search, act, verify for HA)
- Anti-patterns to avoid (multi-entity delegations, `ha_list_entities` abuse)
- Known quirks (e.g. silent `ha_call_service` failures)

The frontier model writes precise delegation instructions without ever seeing
the tool schemas — it knows the tools exist from the talent, and it knows the
delegate will have access.

## MCP: Model Context Protocol

Thane connects to **MCP servers**, local subprocesses over stdio or
servers reachable over Streamable HTTP (on this machine or remote), and
bridges their tools into the agent loop. This extends Thane's
capabilities without writing Go code.

### How It Works

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
        # ... selective inclusion keeps context manageable
      tags: [forge]   # required for the tools to be offered under tag gating
    - name: game
      transport: http
      url: http://localhost:11212/mcp
      tags: [game]
```

On startup, Thane connects to every server in parallel, negotiates the
protocol version (older handshake servers and current stateless servers
both work), discovers the available tools, filters them by
`include_tools`/`exclude_tools` and `tools:` overrides, and bridges them
into the registry as `mcp_{server}_{tool}`. Each server keeps a
supervisor running: a server that is down at startup, crashes or
restarts is reconnected in the background, and its tools answer
"not reachable right now" in the meantime instead of disappearing.

### What delegates see

Delegates run on the registry as it stood at startup. Tools of servers
that were ready then (Thane waits up to 30 seconds) reach delegates like
native tools. Tools that first appear later, because a server came up
late or changed its tool list, reach the main loop through the dynamic
tool overlay but not delegates until the next restart. When a server
changes a tool's description or schema after startup, the main loop sees
the new definition and delegates keep the startup one.

Like every other tool, an MCP tool is only offered when one of its
capability tags is active, so give each server `tags:`.

### A note on Home Assistant

HA does not go through MCP. Earlier deployments bridged
[ha-mcp](https://github.com/karimkhaleel/ha-mcp) for capabilities the
native tools lacked; the native `ha_*` set reached full parity in
v0.10.2 (search, targeting, traces, service discovery, vocabulary) and
the bridge was retired — telemetry showed the model had already stopped
reaching for it. MCP is for capabilities Thane doesn't implement
natively.

### Adding MCP Servers

Most MCP servers work: Thane speaks the stdio and Streamable HTTP
transports and both protocol eras, authenticating with static headers.
Servers that need OAuth sign-in, the deprecated 2024-11-05 HTTP+SSE
transport, or client features such as sampling, elicitation or roots are
not supported yet. See
[MCP Servers](../operating/configuration.md#mcp-servers) for every
option.

## Delegation Profiles

Delegation profiles are compatibility hints for budget and routing defaults.
By default they use a task-focused prompt mode: compact worker identity,
tool-use contract, active capability summaries, tagged context, and
current conditions. The full Thane prompt can still be requested for
continuity-sensitive work, but ordinary delegates should stay in task mode
so small local models are not flooded with persona, ego, injected core
files, always-on talents, or conversation-history dressing. Capability tags
determine the delegate's tool and tagged context scope.

| Profile | Default Tags | Routing Bias | Use For |
|---------|--------------|--------------|---------|
| `general` | none | local, general purpose | Most tasks |
| `ha` | `ha` when no explicit tags are supplied | local, device-control mission | HA-domain delegations |

The model-facing tools do not expose a `profile` knob. When a delegate's
scope includes the `ha` tag, the executor selects the `ha` profile's
budget and routing hints automatically; otherwise it uses `general`.

Delegates inherit elective caller capability tags by default. This keeps
task context such as activated domain tags or KB articles attached to
the child work without making the orchestrator restate it on every
handoff. If explicit tags are provided, they take precedence over profile
default tags.

Use root trailhead tags when the delegate should read the menu guidance
and choose the next branch itself: `development`, `home`, `operations`,
`knowledge`, `media`, `interactive`, or `people`. Use leaf tags when the
caller already knows the needed toolset: `ha`, `files`, `forge`, `web`,
`loops`, `documents`, `diagnostics`, and similar focused tags.

Runtime/channel affordance tags such as `message_channel` and trust tags
such as `owner` are not inherited as model-requested tags; they must be
asserted again by trusted runtime context. Use `inherit_caller_tags=false`
for a strict fresh scope.

Use `context_mode=full` sparingly when a delegate truly needs the same rich
identity and continuity context as the caller. The default `task` mode is
the normal execution path.

## Writing Good Delegation Prompts

Delegates are literal executors, not creative problem-solvers. The more
specific the prompt, the fewer iterations wasted:

**Good:** "Use `ha_find_entity('office hue go')` to get the entity_id, then
`ha_call_service('light.turn_on', entity_id, rgb_color=[0,200,180])`, then
`ha_get_state(entity_id)` to verify."

**Bad:** "Find the Hue Go light in the office and make it teal."

The `talents/delegation.md` talent teaches the orchestrator these patterns. Over
time, as delegation successes and failures are observed, the talent evolves.
