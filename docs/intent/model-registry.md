# Model Registry And Provider Topology

Issue: [#93](https://github.com/nugget/thane-ai-agent/issues/93)

This note revisits the dynamic model registry problem from the current
codebase, not from the February version of Thane. The routing system
works today, but the surrounding provider and model plumbing still
assumes a much smaller world than the one we are now building toward.

## What Exists Today

The current stack has three useful pieces we should keep:

- static YAML model metadata is simple and operator-friendly
- hint-based routing is expressive and already used throughout the app
- the router already has in-memory audit and stats surfaces

The current implementation also has four structural limits:

1. `internal/config/config.go` still models one default Ollama URL plus a
   static `models.available` list. There is no first-class concept of a
   provider resource, server, deployment, overlay state, or runtime
   enable/disable.
2. `cmd/thane/main.go` builds the LLM client graph, while
   `internal/app/new_stores.go` separately rebuilds model metadata for
   the router. The same model catalog is effectively derived twice.
3. `internal/llm/multi.go` routes by bare model name to a provider name.
   It has no concept of multiple Ollama servers, per-deployment health,
   model discovery, or provider capabilities beyond chat.
4. `internal/router/router.go` scores static models and records outcomes
   by selected model name. It cannot distinguish the same model running
   on different resources, and it has no health-aware penalties.

There is also a second adjacent problem: routing policy is scattered.
Delegate profiles, wake paths, summarization, metacognition, and other
callers all inject their own hint maps. `router.LoopSeed` is helping,
but policy is still distributed across many packages.

## Direction

The current provider abstraction should grow into a three-layer model.

### Layer 1: provider resources

These are the actual runtime resources that can answer model requests:

- an Ollama server on a specific host
- the Anthropic API
- the OpenAI API
- any future runner with model discovery or install support

This layer owns:

- connectivity and health
- discovery of advertised models when the provider supports it
- provider-specific capabilities such as pull/install or multimodal
  support
- provider-scoped runtime stats

### Layer 2: normalized Thane deployments

The router should not score raw upstream advertisements. It should score
uniform Thane deployment records derived from provider exports plus
Thane-owned metadata.

A deployment is the effective routing unit:

- provider resource identity
- upstream model name
- optional deployment or server name
- tool support, context window, cost tier, speed, quality
- enabled or disabled state
- provider and deployment health
- observed runtime stats

This is where the tuple idea lives. `qwen3:32b` on a local Mac mini and
`qwen3:32b` on a DGX are different deployments, even if the upstream
model string is the same.

### Layer 3: routing policy

Routing policy should be able to evolve independently of the provider
inventory.

That layer owns:

- default model selection
- quality floors and local-only preferences
- named policies for delegates, metacognition, summarization, wake
  handlers, and escalation
- penalties or boosts based on runtime observations

The router should choose deployments. Policies should shape how it
chooses them.

## Config Base Plus Runtime Overlay

The right long-term pattern here is an immutable config-backed baseline
plus runtime overlay. This is not an already-landed repository pattern.
It is a design direction we have been discussing in other areas, and
issue #93 is a strong candidate to become the first full reference
implementation of it.

Config should remain the source of truth for:

- minimum viable provider resources
- baseline model metadata
- fallback or safety-rail models
- defaults that must exist after restart

Runtime overlay should add:

- discovered deployments
- runtime enable or disable state
- dynamic provider health
- observed latency and failure statistics
- future pull or install results for providers that support them

If the overlay becomes inconsistent or empty, config still provides a
known-good floor.

## Package Shape

This issue is a good opportunity to reduce the current sprawl, but a
big-bang rename would create more churn than value.

Recommended direction:

- keep `internal/router` as the routing-policy package
- introduce `internal/models` for registry, provider resources,
  normalized deployments, discovery, and health
- move provider-specific implementations under subpackages such as:
  - `internal/models/ollama`
  - `internal/models/anthropic`
  - `internal/models/openai`
- keep `internal/llm` temporarily as the provider-neutral request and
  response vocabulary used widely by the loop, iterate engine, logging,
  and tests

That split gives us a cleaner top-level story without forcing every
consumer of `llm.Message` and `llm.ChatResponse` to move immediately.
If `internal/llm` later shrinks to a thin types package, we can decide
whether it still deserves its own top-level name.

## Proposed Phases

### Phase 1: normalize the catalog and centralize construction

Goal: remove duplication and establish first-class provider resources
without requiring dynamic mutation yet.

Deliverables:

- add `models.servers` and `models.available[].server` to config
- preserve `models.ollama_url` as shorthand for a default Ollama server
- build one normalized catalog from config instead of deriving it in
  both `cmd/thane/main.go` and `internal/app/new_stores.go`
- make router config and runtime clients consume that single catalog
- introduce explicit deployment identity in the normalized model record

This phase should be mostly structural cleanup plus multi-server
correctness.

### Phase 2: provider resource abstraction and discovery

Goal: let providers advertise what they can do, instead of treating them
as opaque chat clients.

Deliverables:

- define provider resource interfaces for chat, health, and optional
  discovery
- teach Ollama resources to list installed models
- allow cloud providers to advertise static configured models even if
  they do not support inventory discovery
- represent provider capabilities in normalized metadata

This is the phase where model-provider vocabulary gets standardized.

### Phase 3: deployment-aware router outcomes

Goal: make runtime observations meaningful.

Deliverables:

- record outcomes by deployment identity, not just model name
- attach provider or server identity to router audit and stats
- track health and latency at both provider-resource and deployment
  levels
- feed those observations back into routing penalties

This is where the router starts learning from reality instead of static
scores alone.

### Phase 4: runtime overlay and hot updates

Goal: allow dynamic add, drop, enable, disable, and flagging without
restart.

Deliverables:

- add a SQLite-backed registry overlay for dynamic provider and model
  state
- merge config base plus overlay into one live catalog
- support hot refresh when provider discovery changes
- preserve config-defined fallback models even if overlay state is bad

This is the first phase that truly makes the registry dynamic.

### Phase 5: routing policy consolidation

Goal: move from scattered hint maps to named, inspectable policy.

Deliverables:

- central named policies for delegates, metacognition, summarization,
  wake handlers, and escalation
- fewer hand-assembled hint maps in leaf packages
- better visibility into which callers are using which policy

This phase is closely related to issue #93, but it does not need to
block Phase 1 cleanup.

## Recommended First PR

The first implementation PR should not try to solve the whole issue.

The best first slice is:

1. new normalized catalog builder
2. multi-server config schema with backward-compatible `ollama_url`
3. a provider-resource aware client builder that replaces the duplicated
   construction logic
4. router model records that can distinguish deployments, even if the
   scoring logic stays mostly the same at first

That gives us a better foundation immediately and makes later dynamic
discovery a contained extension instead of another rewrite.

## Non-Goals For The First Slice

Not in the first implementation pass:

- automatic Ollama pull or install support
- a full agent-facing model registry tool surface
- a mass move of every hint callsite into named policies
- a package rename purely for aesthetics

Those all make more sense once the normalized catalog and provider
resource layer exist.
