# Documentation

## Start Here

- **[Your First Thane](guide.md)** — Complete guide for Home Assistant users new to AI agents. Hardware requirements, model choices, deployment, and how to build a relationship with your agent.

## Setup & Configuration

- **[Getting Started](getting-started.md)** — Build, configure, and run Thane (quick reference)
- **[Thane + Home Assistant](homeassistant.md)** — Connect Thane as your HA conversation agent
- **[Routing Profiles](routing-profiles.md)** — Choose the right model for each task (`thane:latest`, `thane:premium`, `thane:ops`, etc.)
- **[OpenClaw Compatibility](openclaw.md)** — Run an OpenClaw-style workspace agent through Thane's `thane:openclaw` profile

## How It Works

- **[Architecture](../ARCHITECTURE.md)** — System design, components, and philosophy
- **[Delegation & MCP](delegation.md)** — How the primary model orchestrates local models and external tool servers
- **[Memory](memory.md)** — How Thane remembers: facts, conversations, archives, anticipations
- **[Context Layers](context-layers.md)** — How the system prompt is assembled from persona, talents, knowledge, and session state
- **[Prompt And Tool Pruning Strategy](prompt-tool-pruning-strategy.md)** — Tracking doc for shrinking the always-on prompt and moving domain doctrine behind tagged context
- **[Model-Facing Context](model-facing-context.md)** — Philosophy and conventions for writing data that will be consumed by future model loops
- **[Model-Facing Tools](model-facing-tools.md)** — Guidance for naming, shaping, and erroring internal tools so models and delegates can use them reliably

## Contributing

- **[Contributing](../CONTRIBUTING.md)** — Development workflow and guidelines
- **[Release Checklist](release-checklist.md)** — Version bump process
