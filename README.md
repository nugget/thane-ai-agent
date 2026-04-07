# Thane

[![CI](https://github.com/nugget/thane-ai-agent/actions/workflows/ci.yml/badge.svg)](https://github.com/nugget/thane-ai-agent/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/nugget/thane-ai-agent.svg)](https://pkg.go.dev/github.com/nugget/thane-ai-agent)
[![Go Report Card](https://goreportcard.com/badge/github.com/nugget/thane-ai-agent)](https://goreportcard.com/report/github.com/nugget/thane-ai-agent)
[![Go Version](https://img.shields.io/github/go-mod/go-version/nugget/thane-ai-agent)](go.mod)
[![License](https://img.shields.io/github/license/nugget/thane-ai-agent)](LICENSE)
[![Release](https://img.shields.io/github/v/release/nugget/thane-ai-agent)](https://github.com/nugget/thane-ai-agent/releases/latest)

> An autonomous AI agent that learns, remembers, and acts.

Your home already generates thousands of data points every hour — temperature sensors, motion detectors, door states, power draws. **Thane** is an AI agent that actually understands them. It knows an open garage door for two hours is unusual, that someone in the kitchen at 2am doesn't want bright lights, and that the dryer finishing three hours ago but still drawing power is worth mentioning. It learns your home, your preferences, and your routines through conversation — and it remembers.

Everything runs on your hardware, behind your firewall. Thane works with any model runner that speaks a standard inference API — local or cloud, your choice. Written in Go. Single binary. No Python, no containers, no accounts to create.

## What Makes Thane Different

**Autonomous, not subjected.** Home Assistant's Assist can only see entities you pre-select. Thane has full API access — it discovers devices, understands context, and acts on its own.

**Memory that persists.** Tell it once that you like the lights dim after 10pm and it just knows. Facts, preferences, routines — all learned through natural conversation and remembered across restarts.

**Private by architecture.** Every conversation, every fact, every contact lives in SQLite on your machine. Cloud models are available but nothing requires them. Privacy is structural, not a policy promise.

**Smart about cost.** The frontier model thinks; a small free local model does the work. Delegation keeps 90%+ of tool calls at zero API cost.

**Single binary.** `go build` and you're done. One command: `thane`.

## Quick Start

```bash
git clone https://github.com/nugget/thane-ai-agent.git
cd thane-ai-agent
just build && just init
# Edit ~/Thane/config.yaml with your HA token, Ollama URL, and MQTT broker
just serve
```

Point Home Assistant's Ollama integration at `http://thane-host:11434`, select model `thane:latest`, and start talking.

**[Your First Thane](docs/operating/guide.md)** — Complete onboarding guide with hardware, models, and deployment.

## Documentation

### Understand It

- [Philosophy](docs/understanding/philosophy.md) — Why Thane exists and why Home Assistant is foundational
- [Architecture](docs/understanding/architecture.md) — System design and component overview
- [Trust Architecture](docs/understanding/trust-architecture.md) — Safety through structure, not prompts

### Run It

- [Getting Started](docs/operating/getting-started.md) — Build, configure, run
- [Home Assistant](docs/operating/homeassistant.md) — HA integration setup
- [Configuration](docs/operating/configuration.md) — Config guide by concern
- [Deployment](docs/operating/deployment.md) — macOS and Linux service installation

### Extend It

- [Delegation & MCP](docs/understanding/delegation.md) — Orchestrator/delegate pattern and MCP tool servers
- [Tools Reference](docs/reference/tools.md) — All 80+ native tools
- [Full Documentation](docs/) — Guided tour of all docs

## Name

**Thane** — A Scottish term for a landholder who managed an estate on behalf of the crown. Your home automation estate deserves a capable steward. ([Project History](docs/history.md))

## Contributing

Contributions welcome. See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache 2.0 — aligned with Home Assistant.
