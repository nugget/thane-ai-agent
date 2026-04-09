---
kind: examples
tags: [loops]
---

# Loops Examples

Use these as trusted launch patterns. Do not improvise the whole
loops-ng contract from scratch unless the situation truly demands it.
Start from the closest recipe and change only what matters.

## Pattern: Detached Research That Reports Back Later

Use this when the current turn would benefit from a side investigation
that can return naturally when it is done.

- Tool: `spawn_loop`
- Shape: `operation: background_task`
- Delivery: `completion: conversation` or `completion: channel`
- Persistence: none beyond whatever documents the loop writes itself

```json
{
  "launch": {
    "spec": {
      "name": "research-current-issue",
      "task": "Investigate the current issue from multiple angles, keep concise notes in a managed document if needed, and report back with the strongest answer once the uncertainty has collapsed.",
      "operation": "background_task",
      "completion": "conversation",
      "profile": {
        "mission": "background",
        "initial_tags": ["knowledge", "documents"],
        "instructions": "Prefer the smallest tool surface that can collapse uncertainty. Use document tools for durable notes."
      }
    }
  }
}
```

If this is launched from the current interactive conversation, you
usually do not need to set an explicit completion target. The runtime
can infer the current conversation or channel context.

## Pattern: Persistent Service With Supervisor Turns

Use this when the loop should keep watching something over time, sleep
between iterations, and occasionally take a more expensive supervisor
pass.

- Tool: `spawn_loop`
- Shape: `operation: service`
- Delivery: usually `completion: none`
- Persistence: the loop should maintain its own state with `doc_write`,
  `doc_edit`, or `doc_journal_update`

```json
{
  "launch": {
    "spec": {
      "name": "battery-watch",
      "task": "Maintain a current view of battery health across the property. Notice trends, anomalies, and devices that deserve attention. Keep the state document concise and trustworthy.",
      "operation": "service",
      "completion": "none",
      "sleep_min": "10m",
      "sleep_max": "30m",
      "sleep_default": "15m",
      "jitter": 0.2,
      "supervisor": true,
      "supervisor_prob": 0.15,
      "quality_floor": 4,
      "supervisor_quality_floor": 9,
      "supervisor_context": "Supervisor turn. Step back from individual readings, look for cross-device patterns or weak assumptions, and decide whether anything now deserves escalation or a sharper hypothesis.",
      "profile": {
        "mission": "background",
        "initial_tags": ["home", "knowledge", "documents"],
        "instructions": "Maintain one durable state document. Use the journal when something materially changes."
      }
    }
  }
}
```

Put the main prompt in `launch.spec.task`, not top-level `launch.task`,
when you want `supervisor_context` to apply cleanly.

## Pattern: Durable Named Service You Can Pause And Resume

Use this when the loop should survive beyond the current moment as a
stored definition that can be inspected, paused, resumed, or relaunched
later.

1. Create or replace the definition with `loop_definition_set`.
2. Use `loop_definition_set_policy(state=\"active\")` to keep it
   running, `paused` to stop without forgetting it, and `inactive` to
   disable it.
3. Use `loop_definition_get`, `loop_definition_list`, and `loop_status`
   to inspect it later.

```json
{
  "spec": {
    "name": "battery-watch",
    "enabled": true,
    "task": "Maintain a current view of battery health across the property. Notice trends, anomalies, and devices that deserve attention. Keep the state document concise and trustworthy.",
    "operation": "service",
    "completion": "none",
    "sleep_min": "10m",
    "sleep_max": "30m",
    "sleep_default": "15m",
    "jitter": 0.2,
    "supervisor": true,
    "supervisor_prob": 0.15,
    "quality_floor": 4,
    "supervisor_quality_floor": 9,
    "supervisor_context": "Supervisor turn. Step back from individual readings, look for cross-device patterns or weak assumptions, and decide whether anything now deserves escalation or a sharper hypothesis.",
    "profile": {
      "mission": "background",
      "initial_tags": ["home", "knowledge", "documents"],
      "instructions": "Maintain one durable state document. Use the journal when something materially changes."
    }
  }
}
```

Use `spawn_loop` for temporary loops. Use stored definitions when the
important part is the lifecycle:

- keep it
- pause it
- resume it
- inspect it later

## Pattern: Background Loop With Operator Turns

Use this when the loop should work independently most of the time but
return to the operator only when it has a real result or has reached a
decision boundary.

- Tool: `spawn_loop`
- Shape: `operation: background_task`
- Delivery: `completion: conversation`
- Prompting move: state clearly what should trigger a return turn

```json
{
  "launch": {
    "spec": {
      "name": "policy-research",
      "task": "Research the current policy question thoroughly. Work independently while evidence gathering is straightforward. Return to the operator only when you have a strong answer, a short list of real options, or a blocking ambiguity that requires a human choice.",
      "operation": "background_task",
      "completion": "conversation",
      "profile": {
        "mission": "background",
        "initial_tags": ["knowledge", "documents"],
        "instructions": "Leave durable notes in documents when they will make the eventual return turn easier to understand."
      }
    }
  }
}
```

This is the most reliable way to get delegate-like behavior without
flattening the parent loop into the entire investigation.
