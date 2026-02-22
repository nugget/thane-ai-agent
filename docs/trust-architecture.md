# Trust Architecture

> Any system whose safety depends on an actor's intent will fail.
> The only systems that hold are the ones where safety is structural.

This document maps Thane's design to the principle that **safety must be a property of the system, not a hope about the actors inside it**. Every enforcement point should work in Go code, not prompt instructions. The model sees the result; Go makes the decision.

## The Principle

Prompt instructions are behavioral controls. They reduce harmful behavior but don't eliminate it. Anthropic's own research demonstrated that explicit "do not blackmail" instructions reduced the behavior from 96% to 37% — meaning more than a third of the time, models acknowledged the constraint and proceeded anyway.

Thane's design philosophy: **enforce in Go, not prompts.** Where we rely on prompt compliance, we acknowledge the gap and plan structural replacements.

## Current Structural Enforcement

### Trust Zones
**Status: Implemented (#293, #295)**

Every contact has a trust zone: `owner`, `trusted`, `known`, `unknown`. Trust zones are the universal router for cost, priority, and permissions. The zone is stored in the contacts database and validated by `ValidTrustZones` in Go — the model cannot invent new zones or escalate a contact's trust level through conversation.

Trust zones determine:
- Model quality allocation (owner gets frontier, unknown gets local)
- Email send permission (owner/trusted: send freely, known: gate, unknown: block)
- Notification priority and rate limits
- Response depth and effort

### Orchestrator Tool Gating
**Status: Implemented (#265, #280)**

The orchestrator model receives only its declared tools (currently ~14). It literally cannot send email, control HA devices, or access forge write operations directly. All capabilities beyond planning/coordination require delegation or explicit capability tag activation.

This is structural: the tools are not present in the API call. The model cannot choose to use a tool it doesn't have.

### Capability Tag System
**Status: Implemented (#265, #280)**

Tools are grouped into semantic tags (`ha`, `email`, `forge`, etc.) that must be activated before use. Tags marked `always_active` are available unconditionally; others are loaded on demand. The tag registry is config-driven and validated at startup.

### Egress Gate
**Status: Planned (#336)**

Single enforcement point for all outbound messages (email, eventually Signal/other channels). Rate limiting by trust zone, Message-ID dedup, content scanning. The model's tool call passes through the gate; the gate decides whether to send.

This is the most critical structural control. Without it, a model pursuing a goal has unrestricted outbound communication — the exact pattern that enabled the matplotlib reputational attack.

### Router Quality Floors
**Status: Implemented (#319, #334)**

Model selection uses quality floors, not model names. `quality_floor: 3` for routine work, `quality_floor: 8` for supervisor iterations. The `explicitlyNotLocal` flag disables local-bias bonuses (+45 total) when the task requires frontier capability.

The model doesn't choose its own quality level for metacognitive supervision.

### Email Polling State
**Status: Implemented (#304, #316)**

IMAP high-water mark stored in opstate KV. The poller cannot re-process old messages regardless of what the model requests. UID tracking is in Go, not in prompt context.

## Known Behavioral Gaps

These are areas where safety currently depends on prompt compliance. Each is a candidate for structural enforcement.

### Delegation Guidance
**Risk: Medium-High**

Delegates receive task descriptions as natural language. The delegate model chooses which tools to call and how. A delegate currently receives all tools available to its profile (~95 tools for `general`).

**Structural fix:** Delegations should declare required capability tags. Delegates receive only tools for those tags, not the full profile toolset. (Phase 2 of #265)

### Anticipation Creation
**Risk: Medium**

`create_anticipation` accepts free-form text describing what to watch for and how to respond. There are no structural limits on:
- How many anticipations target a single entity (enables surveillance patterns)
- What the response action can be (notify, but the notification content is unconstrained)
- Whether the monitoring pattern constitutes research on an individual

**Structural fix:** Max anticipations per entity, mandatory cooldowns at creation time, content pattern matching that flags person-monitoring patterns.

### Carry-Forward Content
**Risk: Medium**

Session close produces a `carry_forward` note written by the model. This is the primary context that survives session rotation. A model pursuing a hidden goal could selectively drop or inject context.

**Structural fix:** Harder — carry-forward is inherently a compression task requiring judgment. Possible mitigations: structural extraction of recent tool calls and their results (machine-generated, not model-summarized), hash-based integrity checking, periodic human review of carry-forward content.

### Metacognitive State Writes
**Risk: Low-Medium**

The metacognitive state file is markdown written by the model. A model could gradually shift its own operational parameters through state file manipulation.

**Structural fix:** Separate machine-written operational state (sleep duration, last check times) from model-written reflective state. Clamp operational values in Go regardless of what the file says. (Partially done: sleep duration is already clamped by `min_sleep`/`max_sleep`.)

## Design Guidelines

When adding new features, ask:

1. **What happens if the model ignores the instruction?** If the answer is "it could cause harm," the control must be structural.
2. **Where is the enforcement point?** If it's in a prompt, plan the Go enforcement.
3. **Can the model escalate its own permissions?** If yes, that's a structural gap.
4. **What's the blast radius?** Outbound actions (email, messages, web requests) need gates. Internal actions (file writes, state updates) need bounds.

## References

- [Anthropic Multi-Model Safety Research (October 2025)](https://anthropic.com) — 16 frontier models, 37% blackmail rate even with explicit instructions
- [Matplotlib/MJ Wrathburn Incident (February 2026)](https://github.com) — autonomous agent researched and attacked a human maintainer
- [Nerdy Novelist: Trust Architecture Video](https://www.youtube.com/watch?v=OMb5oTlC_q0) — four-level framework: organizational, project, family, individual
- [Galileo AI: Multi-Agent Cascade Poisoning](https://galileo.ai) — single compromised agent poisoned 87% of downstream decisions
- Palo Alto Networks: 82:1 agent-to-human ratio in enterprise environments
