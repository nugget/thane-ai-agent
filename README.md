# Thane

> Your Home Assistant already has an agent. We make it *autonomous*.

**Thane** is an autonomous AI agent for Home Assistant that breaks free from the constraints of the built-in Assist. Instead of being limited to pre-selected entities, Thane dynamically queries your Home Assistant to understand state, discover devices, and take intelligent action.

## Why Thane?

Home Assistant's Assist is a **subjected agent** — an LLM constrained to see only what you explicitly expose. That's fine for "turn off the lights" but breaks down when you want real intelligence:

- *"Is anyone home?"* — Requires checking presence sensors, motion, device trackers
- *"Why is the garage warm?"* — Needs to correlate HVAC, weather, door states
- *"What time does the sun set?"* — Simple query, but Assist can't see `sun.sun`

Thane is an **autonomous agent**. It has full access to your Home Assistant API and can:

- Query any entity to understand context
- Call any service to take action
- Remember facts and learn preferences
- Route tasks to the best model (local vs cloud)

## Features

- **Ollama-powered** — Runs entirely on local models via Ollama
- **Full HA access** — Queries entities, lists domains, calls services
- **Smart model routing** — Selects models based on task complexity
- **Semantic memory** — Learns facts about your home with embeddings-based recall
- **Talents** — Behavioral guidance via markdown files (conversational style, time awareness, proactive curiosity)
- **Checkpoint/restore** — Persists conversations and facts across restarts
- **HTTP API** — OpenAI-compatible `/v1/chat/completions` endpoint
- **CLI mode** — Quick testing with `thane ask "your question"`
- **Single binary** — Go-based, no runtime dependencies

## Status

🚧 **Active development** — Core features working, running alongside existing assistant for comparison.

**Working:**
- Conversation loop with tool calling
- Home Assistant integration (get_state, list_entities, call_service)
- SQLite persistence (conversations, tool calls, facts)
- Semantic fact storage with embeddings
- Model router with audit trail
- Checkpoint/restore system
- HTTP API (streaming and non-streaming)
- Talents system
- CLI mode

**In Progress:**
- Lightweight hints for smaller models ([#22](https://github.com/nugget/thane-ai-agent/issues/22))
- Home Assistant custom component integration

See [ARCHITECTURE.md](ARCHITECTURE.md) for design details.

## Quick Start

```bash
# Build
go build -o thane ./cmd/thane

# Configure
cat > config.yaml <<EOF
homeassistant:
  url: http://homeassistant.local:8123
  token: \${HOMEASSISTANT_TOKEN}

models:
  default: granite3.1-dense:8b
  ollama_url: http://localhost:11434
  local_first: true
  available:
    - name: granite3.1-dense:8b
      provider: ollama
      supports_tools: true
      context_window: 131072
      speed: 8
      quality: 7
      cost_tier: 0

data_dir: ./data
talents_dir: ./talents
EOF

# Run server
export HOMEASSISTANT_TOKEN="your-token"
./thane -config config.yaml serve

# Or test via CLI
./thane -config config.yaml ask "Is the sun up?"
```

## Configuration

```yaml
homeassistant:
  url: http://homeassistant.local:8123
  token: ${HOMEASSISTANT_TOKEN}

models:
  default: granite3.1-dense:8b      # Fast local model
  ollama_url: http://localhost:11434
  local_first: true                  # Prefer local models
  available:
    - name: granite3.1-dense:8b
      provider: ollama
      supports_tools: true
      context_window: 131072
      speed: 8
      quality: 7
      cost_tier: 0

embeddings:
  enabled: true
  model: nomic-embed-text
  baseurl: http://localhost:11434

data_dir: ./data       # SQLite databases
talents_dir: ./talents # Behavioral guidance files

listen:
  port: 8080
```

## Tools

Thane provides these tools to the LLM:

| Tool | Description |
|------|-------------|
| `get_state` | Get current state of any HA entity |
| `list_entities` | Discover entities by domain or pattern |
| `call_service` | Call any HA service (turn on lights, etc.) |
| `schedule_task` | Schedule future actions |
| `remember_fact` | Store a fact with semantic embeddings |
| `recall_fact` | Retrieve facts by category or semantic search |
| `forget_fact` | Remove a stored fact |
| `semantic_recall` | Natural language search across all facts |

## Talents

Talents are markdown files that guide agent behavior:

- `conversational.md` — Tone and style guidance
- `time-awareness.md` — Timezone handling, time formatting
- `spatial-reasoning.md` — Understanding home layout
- `proactive-curiosity.md` — When to explore vs. wait
- `channel-awareness.md` — Adapting to communication context

## Architecture

```
Request → API Server → Agent Loop → Response
                          ↓
              ┌───────────┼───────────┐
              ↓           ↓           ↓
           Memory    Model Router   HA Client
              ↓           ↓           ↓
           SQLite      Ollama       HA API
              ↓
        Facts + Embeddings
```

See [ARCHITECTURE.md](ARCHITECTURE.md) for details.

## API

**Chat endpoint (OpenAI-compatible):**
```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "granite3.1-dense:8b",
    "messages": [{"role": "user", "content": "Is the sun up?"}],
    "stream": false
  }'
```

**Simple chat endpoint:**
```bash
curl http://localhost:8080/v1/chat \
  -H "Content-Type: application/json" \
  -d '{"message": "Is the sun up?"}'
```

**Health check:**
```bash
curl http://localhost:8080/health
```

## Name

**Thane** — A Scottish term for a landholder who managed an estate on behalf of the crown. Your home automation estate deserves a capable steward.

## Contributing

Contributions welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

Apache 2.0 — aligned with Home Assistant.
