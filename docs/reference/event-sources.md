# Event Sources

Thane's agent loop processes requests from multiple sources. Everything that
can wake the agent is an *event source*. All event sources produce the same
internal request structure — the agent loop doesn't care where the request
came from.

## API Requests

The most direct path. A user types in the web dashboard, a client calls the
OpenAI-compatible API on port 8080, or Home Assistant sends a conversation
through the Ollama-compatible API on port 11434.

API requests carry a conversation ID for session continuity and can specify
a [routing profile](../operating/routing-profiles.md) via the model name.

## Home Assistant WebSocket

A persistent WebSocket connection to Home Assistant's event bus. Thane
subscribes to `state_changed` events filtered by entity glob patterns
(e.g., `person.*`, `binary_sensor.*door*`).

When a subscribed entity changes state, the event can trigger an agent wake
with the state change as context. This is the same mechanism used by the HA
frontend and mobile apps — the official, first-class event bus.

Configuration is in the `homeassistant` config section. Entity patterns
control which state changes Thane sees.

## MQTT Wake Subscriptions

Thane subscribes to configurable MQTT topics. When a message arrives on a
subscribed topic, it wakes the agent with the message payload as context.

Primary use cases:
- **Frigate NVR events** — camera-detected events trigger the agent
- **Custom automations** — any system that publishes MQTT can wake Thane
- **Cross-instance communication** — other Thane instances or agents

Configuration is in the `mqtt.wake_subscriptions` section. Each subscription
specifies a topic, optional QoS, and routing hints for the resulting agent
request.

See [MQTT](../operating/mqtt.md) for setup details.

## Email Polling

Scheduled IMAP checks with high-water mark tracking. The scheduler fires a
polling task at a configured interval. The poller checks for messages with
UIDs greater than the stored high-water mark — only new messages trigger
agent wakes.

High-water marks are stored in the operational state KV store (opstate),
not in prompt context. This means the poller cannot be manipulated into
re-processing old messages.

Each email account is polled independently. Multiple accounts with different
folders can be configured.

## Signal Messaging

Inbound Signal messages arrive via a JSON-RPC bridge to `signal-cli`. When
a message is received, it's routed to the agent loop with the sender's
contact context (including trust zone).

Signal messages automatically assert the runtime `message_channel`
capability, giving the agent normalized current-conversation tools such
as `send_reaction`. Final reply text is sent back to the sender by the
bridge automatically; the native `signal` capability is for
Signal-specific outbound or diagnostic workflows.

## Scheduled Tasks

Cron-style scheduling stored in SQLite. Each task defines:
- A cron expression (when to fire)
- A wake message (injected into agent context)
- Optional model and routing overrides
- Missed execution recovery (fires on next startup if the window was missed)

Custom tasks can be created via the `schedule_task` tool. Built-in
recurring work runs as loops-ng service definitions instead — for
example, the `ego` loop maintains `core/ego.md` with bounded voluntary
sleep and supervisor randomization, and the `email-poller` loop drives
IMAP polling.

## RSS/Atom Feed Polling

Periodic checks for new entries on followed feeds (RSS, Atom, YouTube
channels). When new entries are detected, the agent wakes to process them.
Polling intervals and feed lists are managed via the `media_follow` and
`media_unfollow` tools.
