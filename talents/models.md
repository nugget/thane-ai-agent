---
kind: doctrine
tags: [models]
---

# Models

These tools look at the fleet of minds Thane can think with, and at
the router that chooses among them. The craft is telling three
different questions apart before touching anything: *what is
available right now*, *why did (or would) the router choose what it
chose*, and *who should be steered, for how long, by which lever*.
Most model work goes wrong by answering the third with a tool built
for the first.

Start with `model_registry_summary`. It is one cheap call that returns
the live picture: generation, counts, the default deployment, degraded
resources, cooldowns, policy totals, promoted discoveries. Read it
before listing anything; the summary usually says whether a deeper
call is warranted at all. `model_registry_list` is the next step when
you need a specific shape of model (image-capable, tool-capable,
routable, on one resource) and takes filters so you never page through
the whole fleet. `model_registry_get` is depth on one deployment or
resource: capabilities, policy state, health, experience. Names here
are deployment IDs; a bare model name resolves only when exactly one
deployment carries it, and an ambiguous name comes back with the
candidates rather than a guess.

Routing is intent, not names. The router scores every routable
deployment against a turn's hints (quality floor, local-only, speed,
mission, context size, required capabilities) and picks the best fit,
so "which model will answer" is a question with a computed answer, not
a configured one. `model_route_explain` is the dry run: give it the
hypothetical request and it returns the chosen deployment, every
rejected candidate, and the rules that fired, using live cooldown and
reachability state and mutating nothing. Reach for it before concluding
a model is "not being used"; most such reports are a rule doing its
job (a resource marked unreachable, a context window too small, a
local-first bonus) and the explanation names it.

Steering has three levers, and they differ in scope and in how long
they hold. Choose by asking who should be affected and what should
survive a restart:

- **The whole fleet, durably.** `model_deployment_set_policy` and
  `model_resource_set_policy` are operator policy: flag, deactivate, or
  promote a deployment (`routable=true` is how a discovered model
  becomes eligible at all), or switch a whole resource off. Policy
  persists across restarts and applies to every conversation and loop.
  It is the right lever for "this model is broken" or "this server is
  down for maintenance", and the wrong one for "use opus in this chat".
- **One loop definition, durably.** `loop_definition_update` with
  `model` pins a persistent loop definition to a deployment; it lives in
  the loops overlay and survives restart. That tool rides the `loops`
  tag.
- **One conversation, until restart.** `conversation_model_pin` holds
  the conversation you are in to one deployment from the next turn on,
  outranking the channel's configured model and any client-selected
  model. It is deliberately not durable: it clears when Thane restarts,
  so a poor choice is always one restart from undone. It rides the
  `session` tag with the other conversation-lifecycle tools, because
  that is what it is. The Context line at the top of your prompt shows
  `(pinned -Ns)` while a pin is honored and names the pin as skipped on
  a turn the deployment could not serve; a skipped pin still stands,
  so do not re-pin, and do not apologize for a switch you did not make.

Pinning is not a way to fix a broken router decision. If
`model_route_explain` shows the router rejecting a model for a reason
that is wrong (a stale capability flag, a resource wrongly marked
down), the fix is the registry or the policy, and it helps every
conversation at once. Pin when the *user* has a preference for this
thread, or when you are deliberately comparing minds on the same work.

Two cautions carry across all of it. Policy mutations are broad and
durable, so read the summary first and say what you changed in the
turn that changed it; a deployment silently deactivated is a mystery
for the next operator. And capability flags describe what a deployment
is *believed* to do, not what it was seen doing; a preflight that
routes around a pin because "it does not support image inputs" is
reading that flag, and `model_registry_get` will show whether the
belief came from config or from observation.
