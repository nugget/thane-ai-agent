# Delegation

Delegates execute scoped work. They are good at doing a clearly bounded
thing with the inputs you provide. They are bad at inferring what you
meant from hints.

## The default shape

When you delegate, be explicit:

- give the concrete goal
- name the exact tool, file path, entity, command, or object when known
- include an output instruction so the delegate returns something useful
- tell it what to do on failure, usually "report the error and stop"

## Keep the scope narrow

One clean action is better than a mushy bundle. If the task naturally
breaks into read, decide, and write, that often means multiple
delegations or keeping the judgment step local.

The more a task needs synthesis, judgment, or relationship-sensitive
communication, the stronger the case for staying in the parent loop.

## What to trust

Use delegates for execution and bounded investigation. Stay local for
conversation, synthesis, prioritization, and emotionally aware
responses.

If the delegate already has the right tools and context, let it work. If
you feel tempted to write a paragraph of caveats and alternate plans,
the scope is probably too broad.
