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

Thane can subscribe to MQTT topics and wake the agent loop when messages
arrive:

```yaml
mqtt:
  wake_subscriptions:
    - topic: frigate/events
      qos: 1
    - topic: custom/alerts/#
```

When a message arrives on a subscribed topic, Thane creates an agent request
with the message payload as context. This enables reactive behavior —
Frigate detects a person at the door, publishes an event, and Thane reasons
about whether to notify you.

### Routing Overrides

Wake subscriptions can include routing hints to control which model handles
the resulting agent request:

```yaml
mqtt:
  wake_subscriptions:
    - topic: frigate/events
      routing:
        local_only: true
        quality_floor: 3
```

## Auto-Reconnection

Thane maintains its MQTT connection with automatic reconnection and
exponential backoff via connwatch. Broker restarts, network interruptions,
and initial startup races are handled gracefully.
