# MQTT

Thane uses MQTT for two purposes: publishing its own telemetry as
HA-discoverable entities, and subscribing to topics that can wake the
agent loop.

## Why MQTT Is Required

MQTT is how Thane announces itself to Home Assistant. Through HA's MQTT
discovery protocol, Thane publishes sensor entities that appear
automatically in the HA UI — no manual configuration needed. Without MQTT,
Thane can still *talk to* HA via the REST and WebSocket APIs, but HA can't
*see* Thane's operational state.

MQTT also enables event-driven agent wakes. External systems (Frigate NVR,
custom automations, other Thane instances) can publish messages that trigger
the agent loop without polling.

## Broker Setup

Most Home Assistant installations include the
[Mosquitto broker add-on](https://github.com/home-assistant/addons/tree/master/mosquitto).
If you're already running HA, you likely have an MQTT broker available.

```yaml
mqtt:
  broker: tcp://homeassistant.local:1883
  # username: thane          # if your broker requires auth
  # password: your_password
```

If your broker uses TLS:

```yaml
mqtt:
  broker: tls://homeassistant.local:8883
```

## Telemetry Entities

Thane publishes these sensors via HA MQTT discovery:

| Entity | Description |
|--------|-------------|
| `sensor.thane_uptime` | Service uptime |
| `sensor.thane_tokens_today` | Daily token consumption |
| `sensor.thane_default_model` | Current default routing model |
| `sensor.thane_last_request` | Timestamp of last interaction |
| `sensor.thane_version` | Running version |

These appear automatically in Home Assistant under the Thane device. You can
use them in automations, dashboards, and alerts — for example, alerting if
Thane hasn't processed a request in 24 hours.

Entity names are prefixed with the agent's configured name (typically the
persona name).

## Wake Subscriptions

Thane can subscribe to MQTT topics and deliver matching messages as
event-source wakes to an existing loop. Each subscription names a
`wake_loop` target; matching messages become loop notifications that
the target loop sees on its next iteration. No fresh loop spawns per
message — the target loop owns routing via its own
`Spec.Profile`/`SupervisorProfile`.

```yaml
mqtt:
  subscriptions:
    - topic: frigate/events
      wake_loop:
        name: home_security
    - topic: custom/alerts/#
      wake_loop:
        name: mqtt-default-handler
        tags:
          - security
```

When no custom handler is desired, point `wake_loop` at the built-in
`mqtt-default-handler` event-driven loop, which is registered
automatically when MQTT is configured.

`wake_loop` accepts the following fields:

| Field | Purpose |
|---|---|
| `loop_id` | Exact live loop ID. Preferred when known. |
| `name` | Loop name when `loop_id` isn't known (mqtt-default-handler if you don't have a bespoke handler). |
| `force_supervisor` | Force the target's next iteration into supervisor mode. |
| `priority` | `low` / `normal` / `urgent`; drives prompt-rendering order on the target loop. |
| `instructions` | Compact text appended to the rendered wake event. |
| `tags` | Iteration-scoped capability tags activated on the next turn (e.g. `security`, `device_control`). Fade unless the model explicitly activates them. |

Subscriptions without `wake_loop` are ambient-awareness only and are
not delivered to any loop. Legacy inline-Profile entries (the
pre-PR-T2b `wake:` field with `local_only` / `quality_floor` / etc.)
are auto-migrated onto `mqtt-default-handler` at config load with a
WARN log; remove them from the YAML at your convenience.

Config-defined targets are verified against the live loop registry at
startup — a typo in `wake_loop.name` fails the app launch loud rather
than silently dropping the first matching message.

## Auto-Reconnection

Thane maintains its MQTT connection with automatic reconnection and
exponential backoff via connwatch. Broker restarts, network interruptions,
and initial startup races are handled gracefully.
