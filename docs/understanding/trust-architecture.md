# Trust Architecture

> Any system whose safety depends on an actor's intent will fail.
> The only systems that hold are the ones where safety is structural.

This document maps Thane's design to the principle that **safety must be a
property of the system, not a hope about the actors inside it**. Every
enforcement point should work in Go code, not prompt instructions. The model
sees the result; Go makes the decision.

## The Principle

Prompt instructions are behavioral controls. They reduce harmful behavior
but don't eliminate it. Anthropic's own research demonstrated that explicit
"do not blackmail" instructions reduced the behavior from 96% to 37% —
meaning more than a third of the time, models acknowledged the constraint
and proceeded anyway.

Thane's design philosophy: **enforce in Go, not prompts.** Where we rely on
prompt compliance, we acknowledge the gap and plan structural replacements.

## Current Structural Enforcement

### Core Identity Bootstrap

**Status: Implemented**

`thane init` creates `core/` as the instance trust root. It generates an
Ed25519 SSH signing key, an internal Ed25519 X.509 channel CA, and
`core/config.yaml` with the initial identity declaration and default trust
policy. Private keys remain local under `core/` with `0600` permissions and
are ignored by git. Public identity material and policy are committed
together as one SSH-signed birth commit, giving the instance a verifiable
cryptographic birthday.

This is a foundation, not the full peer-trust system. Peer CA exchange,
delegation certificates, inherited-trust policy enforcement, and transport
mTLS still need dedicated runtime paths.

### Trust Zones

**Status: Implemented**

Every contact has a trust zone: `admin`, `household`, `trusted`, `known`,
`unknown`. Trust zones are the universal router for cost, priority, and
permissions. The zone is stored in the contacts database and validated by
`ValidTrustZones` in Go — the model cannot invent new zones or escalate a
contact's trust level through conversation.

Trust zones determine:
- Model quality allocation (admin/household gets frontier, unknown gets local)
- Email send permission (admin/household: send freely, known: gate, unknown: block)
- Notification priority and rate limits
- Response depth and effort

### Orchestrator Tool Gating

**Status: Implemented**

The orchestrator model receives only its declared tools (currently ~14). It
literally cannot send email, control HA devices, or access forge write
operations directly. All capabilities beyond planning/coordination require
delegation or explicit capability tag activation.

This is structural: the tools are not present in the API call. The model
cannot choose to use a tool it doesn't have.

### Capability Tag System

**Status: Implemented**

Tools are grouped into semantic tags (`ha`, `email`, `forge`, etc.) that must
be activated before use. Tags marked `always_active` are available
unconditionally; others are loaded on demand. The tag registry is
config-driven and validated at startup.

### Egress Gate

**Status: Planned**

Single enforcement point for all outbound messages (email, eventually
Signal and other channels). Rate limiting by trust zone, Message-ID dedup,
content scanning. The model's tool call passes through the gate; the gate
decides whether to send.

This is the most critical structural control not yet implemented. Without
it, a model pursuing a goal has unrestricted outbound communication — the
exact pattern that enabled the matplotlib reputational attack.

### Router Quality Floors

**Status: Implemented**

Model selection uses quality floors, not model names. The model doesn't
choose its own quality level for metacognitive supervision.

### Email Polling State

**Status: Implemented**

IMAP high-water mark stored in opstate KV. The poller cannot re-process old
messages regardless of what the model requests. UID tracking is in Go, not
in prompt context.

## Known Behavioral Gaps

These are areas where safety currently depends on prompt compliance. Each is
a candidate for structural enforcement.

### Delegation Guidance

**Risk: Medium-High**

Delegates receive task descriptions as natural language. The delegate model
chooses which tools to call and how. A delegate currently receives all tools
available to its profile.

**Structural fix:** Delegations should declare required capability tags.
Delegates receive only tools for those tags, not the full profile toolset.

### Carry-Forward Content

**Risk: Medium**

Session close produces a carry-forward note written by the model. This is
the primary context that survives session rotation. A model pursuing a hidden
goal could selectively drop or inject context.

**Structural fix:** Harder — carry-forward is inherently a compression task
requiring judgment. Possible mitigations: structural extraction of recent
tool calls and their results, hash-based integrity checking, periodic human
review.

### Metacognitive State Writes

**Risk: Low-Medium**

The metacognitive state file is markdown written by the model. A model could
gradually shift its own operational parameters through state file
manipulation.

**Structural fix:** Separate machine-written operational state from
model-written reflective state. Clamp operational values in Go regardless of
what the file says.

## Design Guidelines

When adding new features, ask:

1. **What happens if the model ignores the instruction?** If the answer is
   "it could cause harm," the control must be structural.
2. **Where is the enforcement point?** If it's in a prompt, plan the Go
   enforcement.
3. **Can the model escalate its own permissions?** If yes, that's a
   structural gap.
4. **What's the blast radius?** Outbound actions (email, messages, web
   requests) need gates. Internal actions (file writes, state updates) need
   bounds.
