# Documentation

Thane is an autonomous AI agent that connects language models to Home
Assistant, giving AI grounded awareness of physical reality. These docs
cover what Thane is, how it thinks, and how to run it.

**New here?** Start with [Your First Thane](operating/guide.md) — a
complete onboarding guide for Home Assistant users.

---

## Understanding Thane

*What is this and how does it think?*

- **[Philosophy](understanding/philosophy.md)** — Why Thane exists, why
  Home Assistant is foundational, and why privacy is structural
- **[Architecture](understanding/architecture.md)** — System design,
  component map, technology choices
- **[The Agent Loop](understanding/agent-loop.md)** — The core reasoning
  cycle: context assembly, tag activation, planning, delegation, response
- **[Delegation](understanding/delegation.md)** — How the orchestrator
  model plans and local models execute at zero cost
- **[Memory](understanding/memory.md)** — Semantic facts, conversation
  history, session archives, and how the agent learns
- **[Context Layers](understanding/context-layers.md)** — How the system
  prompt is assembled from persona, talents, knowledge, and session state
- **[Trust Architecture](understanding/trust-architecture.md)** — Safety
  through structural enforcement, not prompt compliance
- **[Glossary](understanding/glossary.md)** — Canonical definitions for
  Thane-specific terminology

## Operating Thane

*How do I run this?*

- **[Getting Started](operating/getting-started.md)** — Build, configure,
  and run (quick reference)
- **[Your First Thane](operating/guide.md)** — Complete guide: hardware,
  models, deployment, and building a relationship with your agent
- **[Home Assistant](operating/homeassistant.md)** — Connect Thane as your
  HA conversation agent
- **[MQTT](operating/mqtt.md)** — Broker setup, telemetry entities, and
  wake subscriptions
- **[Configuration](operating/configuration.md)** — Config guide organized
  by concern
- **[Routing Profiles](operating/routing-profiles.md)** — Model selection
  presets (`thane:latest`, `thane:premium`, `thane:ops`, etc.)
- **[Hardware Requirements](operating/hardware.md)** — Platform support,
  production observations, minimum specs
- **[Deployment](operating/deployment.md)** — Service installation for
  macOS and Linux

## Reference

*What exactly does X do?*

- **[Tools](reference/tools.md)** — All 80+ native tools by category
- **[CLI](reference/cli.md)** — Commands, flags, and config discovery
- **[API & Endpoints](reference/api.md)** — Ports, protocols, web
  dashboard, CardDAV
- **[Event Sources](reference/event-sources.md)** — Everything that can
  wake the agent loop

## Extending Thane

- **[OpenClaw Compatibility](openclaw.md)** — Run an OpenClaw-style
  workspace agent through Thane's `thane:openclaw` profile

## Developer Internals

- **[Prompt & Tool Pruning Strategy](prompt-tool-pruning-strategy.md)** — Tracking doc for shrinking the always-on prompt and moving domain doctrine behind tagged context
- **[Model-Facing Context](model-facing-context.md)** — Philosophy and conventions for writing data consumed by model loops
- **[Model-Facing Tools](model-facing-tools.md)** — Guidance for naming, shaping, and erroring internal tools
- **[Dashboard Graph Visual Grammar](dashboard-graph-visual-grammar.md)** — Visual language for the dashboard's force-directed graph

## Project

- **[History](history.md)** — Timeline from first commit to v0.9.0
- **[Contributing](../CONTRIBUTING.md)** — Development workflow and
  guidelines
- **[Release Checklist](release-checklist.md)** — Version bump process
