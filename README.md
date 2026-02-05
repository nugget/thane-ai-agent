# Thane

> Your Home Assistant already has an agent. We make it *autonomous*.

**Thane** is an autonomous AI agent for Home Assistant that breaks free from the constraints of the built-in Assist. Instead of being limited to pre-selected entities, Thane dynamically queries your Home Assistant to understand state, discover devices, and take intelligent action.

## Why Thane?

Home Assistant's Assist is a **subjected agent** — an LLM constrained to see only what you explicitly expose. That's fine for "turn off the lights" but breaks down when you want real intelligence:

- *"Is anyone home?"* — Requires checking presence sensors, motion, device trackers
- *"Why is the garage warm?"* — Needs to correlate HVAC, weather, door states
- *"Remind me when the laundry is done"* — Monitoring power sensors over time

Thane is an **autonomous agent**. It has full access to your Home Assistant API and can:

- Query any entity to understand context
- Call any service to take action
- Monitor events and act proactively
- Remember facts and learn preferences

## Features

- **Ollama-compatible API** — Drop-in replacement for any OpenAI-compatible LLM endpoint
- **Full HA access** — Queries entities, calls services, subscribes to events
- **Smart model routing** — Uses local models for simple tasks, cloud for complex reasoning
- **Structured memory** — Learns about your home, not just chat history
- **Event-driven** — Responds to state changes, not just prompts
- **Single binary** — Go-based, no runtime dependencies

## Status

🚧 **Early development** — Architecture defined, implementation starting.

See [ARCHITECTURE.md](ARCHITECTURE.md) for design details.

## Quick Start

*(Coming soon)*

```bash
# Install
go install github.com/nugget/thane-ai-agent@latest

# Configure
cat > thane.yaml <<EOF
homeassistant:
  url: http://homeassistant.local:8123
  token: ${HA_TOKEN}

models:
  default: ollama/llama3:8b
EOF

# Run
thane serve
```

## Configuration

Thane uses a simple YAML config:

```yaml
homeassistant:
  url: http://homeassistant.local:8123
  token: ${HA_TOKEN}  # or use HA_TOKEN env var

models:
  default: ollama/llama3:8b           # Local model for quick tasks
  reasoning: anthropic/claude-sonnet-4  # Cloud model for complex reasoning

memory:
  path: ./memory    # Where to store memory database
  
listen:
  port: 8080        # API server port
```

## Home Assistant Integration

*(Coming in Phase 4)*

Thane will include a custom component that registers it as a conversation agent in Home Assistant, enabling:

- Voice assistant integration
- Automation triggers
- Dashboard cards

## Architecture

```
Request → API Server → Agent Loop → Response
                          ↓
              ┌───────────┼───────────┐
              ↓           ↓           ↓
           Memory    Model Router   HA Client
              ↓           ↓           ↓
           SQLite    Ollama/Claude   HA API
```

See [ARCHITECTURE.md](ARCHITECTURE.md) for the full design.

## Roadmap

- [x] Architecture design
- [ ] OpenAI-compatible API server
- [ ] Home Assistant REST client
- [ ] Basic agent loop
- [ ] Conversation memory
- [ ] WebSocket event subscription
- [ ] Model routing
- [ ] Parallel tool execution
- [ ] Proactive automation

## Contributing

Contributions welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## Name

**Thane** — A Scottish term for a landholder who managed an estate on behalf of the crown. Your home automation "estate" deserves a capable steward.

## License

Apache 2.0 — aligned with Home Assistant.
