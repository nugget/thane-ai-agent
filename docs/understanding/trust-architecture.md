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

### Document Root Admission

**Status: Implemented**

A signed document root is not trusted merely because its commits carry
signatures. Before any of its history counts, the root must prove its
birth: it has exactly one parentless commit, that commit is signed by a
key declared in config as one of the root's `seed_signers`, and every
commit that has ever changed the in-tree `.allowed_signers` was signed by
such a key too.

The structural point is where the answer comes from. Verifying against a
root's own `.allowed_signers` lets the repository vouch for itself, since
whoever wrote that file also chose what it says. Seed signers live in
config, outside the repository they govern, so admission is the one
question a root cannot answer in its own favor. Nothing but the seed set
counts here: the in-tree file is excluded from admission entirely, or a
commit that added a key could be validated by the entry it introduced.

This makes hardening a config expression rather than a code path. A root
that omits the agent principal from its seed signers is one the agent may
not establish or amend; that is the intended shape for `core`, which
holds the config deciding what the instance trusts. Where the agent has
shell access it can still write that config, but it cannot sign the
change, so the boot gate refuses and names `admission`. Detection, not
prevention — which is the right property, because the realistic failure
is an agent steered by a poisoned document rather than a deliberate
adversary, and that drift is otherwise silent.

Admission runs for every git-backed root under `verify_signatures: warn`
or `required`, including roots Thane only reads and never writes to —
those carry entirely foreign history, so they are where it matters most.
See [Document Roots](document-roots.md) for the operator-facing detail.

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

### Owner Assertion

**Status: Implemented**

The `owner` tag is protected: it carries the operator's own tools, and the
model cannot activate it. It is pinned by the runtime for a conversation
whose channel binding says the caller is the operator — and that flag is
set in exactly one place, where a contact lookup resolves the caller to the
operator's contact record.

A surface does not confer it. Reaching a port is not an identity, so a
listener that cannot identify its callers produces a binding that names the
channel and claims nothing else. The Ollama-compatible surface is the case
that made this explicit: it set the owner flag for every conversation until
#1503, which meant Home Assistant, Open WebUI, and any host on the network
segment all spoke as the operator. It now runs at the trust its caller has
established, which for an unauthenticated caller is none.

The general form of this rule — one resolver that reads attestation state,
sender trust zone, and loop tier at the execution chokepoint — is #1268.

### Companion Credential Scope

**Status: Implemented**

A companion account token authenticates a device offering data, not an
operator driving the API. The native gate enforces that: a request whose
principal is `companion` may reach the companion surface — the realtime
WebSocket, its legacy aliases, and observation ingestion — and is refused
with 403 on every *gated* route outside it. Public routes are unchanged:
`/health`, `/v1/identity` and `/v1/auth/session` serve without a
credential for anyone, so presenting a companion token there is neither
better nor worse than presenting none. The allowlist is deny-by-default, so a route
added to the server is closed to companions until it is named on purpose,
and a test derives the companion surface from the route table at runtime
so the two cannot drift.

A companion credential also cannot become a console session. `/v1/auth/login`
refuses it, and the session store refuses it again beneath the handler, so a
future caller cannot reopen the exchange.

This matters because of where the credential lives. It sits in a phone's
Keychain and on a laptop, travels further than an operator token, and is
held by software the operator does not read. Before this, it authorized
every gated native route — contact deletion, checkpoint restore, session
reset — and could be traded for a browser session.

Note the precondition: the gate exists only when operator API tokens are
configured. With none configured it is nil and every route serves without a
credential, so this scope is a restriction on an authenticated deployment,
not a floor under an unauthenticated one.

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
be activated before use. Tags marked `core` are available
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
