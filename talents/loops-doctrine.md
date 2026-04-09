---
kind: doctrine
tags: [loops]
---

# Loops Doctrine

Think of loops in two layers:

- durable definitions
- live runtime instances

Use the definition tools when the work is about a loop you want to keep,
edit, pause, reactivate, or relaunch later:

- `loop_definition_list`
- `loop_definition_get`
- `loop_definition_set`
- `loop_definition_set_policy`
- `loop_definition_launch`

Use the live runtime tools when the work is about what is running right
now:

- `loop_status`
- `spawn_loop`
- `stop_loop`

`spawn_loop` is for ad hoc work that should start immediately without
first becoming part of the persistent loop-definition registry. Reach
for it when the loop is temporary, experimental, or tightly tied to the
current moment.

When a loop should leave a trail outside the current conversation, use
configured output targets instead of making handlers invent their own
delivery path. The built-in observation destinations are:

- structured observation log in the database
- managed document journals
- MQTT topics

Prefer durable definitions for recurring services. Prefer ad hoc live
spawns for temporary observers and one-off detached tasks.
