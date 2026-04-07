# Philosophy

> An autonomous AI agent that learns, remembers, and acts.

A vibration sensor on a washer tells you it's done. But understanding that
you're home, it's been 30 minutes, and you haven't moved the laundry — and
gently reminding you before it gets musty — that's the difference between
automation and comprehension.

This is what Thane is building toward: a system that notices, understands,
and helps. Not through rigid automation rules, but through genuine contextual
awareness.

## Principles

**Understanding over Rules.** Traditional home automation fires events.
Thane comprehends situations. The difference between "washer stopped" and
"laundry needs attention before it mildews."

**Boring Tech, Creative Application.** Go, SQLite, MQTT, Home Assistant.
Mature, stable, documented. The innovation isn't in the stack — it's in
how these pieces compose to create something that can actually care about
your environment.

**Open Source as Philosophy.** This isn't a product seeking users. It's an
idea seeking evolution. Every component is accessible, every decision
documented. Someone will fork this and do something we never imagined.
That's the point.

### What We're Not Building

- Not another voice assistant (though voice is one interface)
- Not another automation platform (though we integrate with automation)
- Not a business model (this is gift culture)

### What We Are Building

A nervous system for living spaces. Sensors that notice. Memory that learns
what matters. Context engines that understand. All working together to create
an environment that's genuinely helpful without being intrusive.

*Wake frequently, speak rarely. The difference is the craft.*

## Why Home Assistant

Home automation systems generate thousands of real-time data points —
temperature sensors, motion detectors, door states, device power draws —
creating a rich, structured representation of physical reality. When Thane
connects an LLM to Home Assistant, it transforms these discrete signals into
coherent awareness.

The AI doesn't just read sensor values. It understands that a 6-degree
temperature differential between two office sensors suggests one is near a
heat source, that an open garage door for two hours is unusual, or that
someone moving from kitchen to office at 2am might not want bright lights.
Structured data becomes the foundation for genuine environmental intelligence.

The temporal dimension is where this combination truly shines. Home
Assistant's continuous state tracking gives Thane's AI a sense of time that
most LLMs lack — not just clock time, but *lived time*. It knows that the
dryer finishing its cycle three hours ago but still drawing power is worth
mentioning, that presence patterns on weekdays differ from weekends, or that
temperature trends matter more than absolute values.

By grounding language models in real sensor data with temporal context, Thane
creates AI that doesn't just respond to commands but actively participates in
the rhythm of daily life. The result is an AI presence that feels less like
a smart speaker and more like someone who genuinely lives in your home.

This is why Home Assistant isn't optional — it's foundational. HA is the
sensory layer that gives the AI something real to reason about. Without it,
you have a chatbot. With it, you have awareness.

## Privacy by Architecture

Your data stays on your hardware, behind your firewall. This isn't a
compromise — it's the design intent.

Thane works with any model runner that speaks a standard inference API.
Every conversation, every fact it learns, every contact it manages lives
in SQLite databases on your machine. Cloud models are available when you
want them, but nothing requires them. There is no telemetry, no
phone-home, no account to create.

Interoperability is the philosophy. Rather than coupling to a single
provider, Thane speaks the protocols the ecosystem already uses —
OpenAI-compatible chat completions, Ollama's API, and LM Studio's
extensions. As new runners and providers emerge, they slot in without
architectural changes.

The local-first architecture means privacy is structural, not a policy
promise. There's no trust decision about what a vendor does with your
data because your data never leaves your network. The code is open. The
databases are SQLite files you can inspect with any tool. The
configuration is a YAML file on your filesystem.

For operators who want the intelligence of frontier models without
compromising privacy, Thane's hybrid routing sends only the conversation
context needed for a specific request — not your full home state, not
your credentials, not your history. And even that is opt-in.

## The Core Insight

Thane is an **autonomous agent** — an LLM with persistent memory, tool use,
and the ability to act on your behalf. It's not constrained to a pre-defined
set of capabilities; it discovers what's available and reasons about how to
help.

The key is what Thane connects to. Home Assistant gives it eyes and hands in
the physical world. MQTT gives it a heartbeat visible to the rest of your
infrastructure. Email and Signal give it a voice beyond the dashboard. The
agent loop ties these together into something that can notice, reason, and
act — not just respond.

Written in Go. Single binary. No Python environments, no dependency hell,
no runtime to manage. One command: `thane`.
