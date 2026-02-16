# Delegation & MCP

## The Problem

Frontier models are great at reasoning but expensive per token. Local models are free but need precise instructions. Tool-heavy tasks — searching HA entities, reading files, running commands — can burn through dozens of iterations, each re-sending the full conversation context.

A single HA device control sequence (search → act → verify) might cost $1.50 in cloud API tokens if the frontier model does it directly.

## The Solution: Delegation

Thane's primary model **orchestrates** — it understands the request, plans the approach, and writes precise instructions. Then it **delegates** the tool-heavy execution to a smaller, faster local model.

```
User: "Set the office Hue Go to teal"

Primary (Opus):
  → "Delegate to ha profile: search for 'hue go' entities in office,
     call light.turn_on with rgb_color [0,200,180], verify with get_state"

Delegate (gpt-oss:20b, local):
  → search_entities("hue go") → found light.office_hue_go
  → call_service(light.turn_on, light.office_hue_go, rgb=[0,200,180])
  → get_state(light.office_hue_go) → confirmed ON, correct color
  → return result to primary

Primary: "Done — your Hue Go is now teal at full brightness."
```

The frontier model used ~62K input tokens for orchestration ($0.96). The local model used ~32K tokens for execution ($0.00). Total: under a dollar, versus $3+ if the frontier model ran every tool call directly.

## Tool Gating

When delegation is enabled (`delegation_required: true`), tools are partitioned:

**Primary model (iteration 0) sees:**
- `thane_delegate` — delegate tasks to local models
- `remember_fact` / `recall_fact` — memory operations
- `session_working_memory` — session scratchpad
- `archive_search` — conversation history search

**Delegates see all tools:**
- Native HA tools (`get_state`, `find_entity`, `call_service`, etc.)
- MCP tools (`mcp_home_assistant_ha_*`)
- File operations, shell exec, web search/fetch

The `thane:ops` profile disables gating — the primary model sees everything directly. Use it when you need the frontier model's judgment on tool results, not just orchestration.

## Talents as Knowledge Bridge

The primary model doesn't see tool definitions directly (they're gated). Instead, **talent files** teach it what's available and how to write effective delegation prompts.

`delegate-hints.md` contains:
- Which tools exist for each delegation profile
- Patterns to follow (search → act → verify for HA)
- Anti-patterns to avoid (multi-entity delegations, `list_entities` abuse)
- Known quirks (ha-mcp `return_response` errors, silent `call_service` failures)

This means the frontier model can write precise delegation instructions without ever seeing the tool schemas — it knows the tools exist from the talent, and it knows the delegate will have access.

## MCP: Model Context Protocol

Thane hosts **MCP servers** as stdio subprocesses, bridging their tools into the agent loop. This extends Thane's capabilities without writing Go code.

### How It Works

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
        # ... selective inclusion keeps context manageable
```

On startup, Thane:
1. Launches each MCP server as a subprocess
2. Discovers available tools via the MCP protocol
3. Filters to `include_tools` (if specified)
4. Bridges filtered tools into the agent loop as `mcp_{server}_{tool}` functions

### ha-mcp

[ha-mcp](https://github.com/karimkhaleel/ha-mcp) provides 90+ Home Assistant tools — far more than Thane's native HA tools. With `include_tools` filtering, we select ~13 high-value tools to keep delegate context manageable:

- `ha_search_entities` — find entities by name, area, or domain
- `ha_get_state` — current state and attributes
- `ha_get_entity` — entity configuration details
- `ha_call_service` — invoke HA services
- `ha_get_history` — historical state data
- `ha_get_logbook` — event timeline
- `ha_config_list_areas` — area registry
- `ha_get_camera_image` — camera snapshots
- And more

### Adding MCP Servers

Any MCP-compatible server can be added. Future candidates:
- GitHub MCP (issue management, PR review)
- Search MCP (web search providers)
- Calendar MCP (scheduling)

The MCP protocol is standardized — if a server exists, Thane can host it.

## Delegation Profiles

When delegating, the primary model specifies a **profile** that determines which model and tools the delegate gets:

| Profile | Model | Tools | Use For |
|---------|-------|-------|---------|
| `general` | Default local | All (native + MCP) | Most tasks |
| `ha` | Default local | Native HA tools only | Device control, entity queries |

## Writing Good Delegation Prompts

Delegates are literal executors, not creative problem-solvers. The more specific the prompt, the fewer iterations wasted:

**Good:** "Use `find_entity('office hue go')` to get the entity_id, then `call_service('light.turn_on', entity_id, rgb_color=[0,200,180])`, then `get_state(entity_id)` to verify."

**Bad:** "Find the Hue Go light in the office and make it teal."

The delegate-hints talent teaches the primary model these patterns. Over time, as we observe delegation successes and failures, the talent evolves.
