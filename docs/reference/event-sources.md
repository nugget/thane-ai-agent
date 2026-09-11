# Event Sources

Thane's agent loop processes requests from multiple sources. Everything that
can wake the agent is an *event source*. All event sources produce the same
internal request structure — the agent loop doesn't care where the request
came from.

## API Requests

The most direct path. A user types in the web dashboard, a client calls the
OpenAI-compatible API on port 8081, or Home Assistant sends a conversation
through the Ollama-compatible API on port 11434.

API requests carry a conversation ID for session continuity and can specify
a [virtual model](../operating/routing-profiles.md) via the model name.

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

The `email-poller` service loop checks every configured account's INBOX at
`email.poll_interval` and dispatches each new message as one event to the
`email-default-handler` loop (an event-driven built-in). Each event's
metadata names the `account`, `folder`, and `uid` of the message, its
`message_id`, the sender (`from`, `from_address`, `from_name`), and the
contact directory's answer about the sender: `contact_status`
(`matched`, `unmatched`, `ambiguous`, or `lookup_failed`), the effective
`trust_zone` (`unknown` for a stranger, the least privileged candidate's
zone when several records share the address), and, for a match,
`contact_id`, `contact_name`, and `is_owner`. These are the same keys
the Signal bridge stamps, so a handler reads identity the same way on
both channels. `is_owner` says the matched record is the operator's; it
does not say the operator wrote the message, because a From header is a
claim until a signature verifies it. The poller stamps no per-wake tags;
identity rides in the event. After each delivered batch the poller
records an inbound interaction on every matched contact, once per
contact at the newest message's date.

High-water marks are stored in the operational state KV store (opstate)
as `{uidvalidity, uid}` per account, not in prompt context. The poller
cannot be manipulated into re-processing old messages, and a mailbox
whose UIDVALIDITY changes reseeds silently instead of replaying.
Messages the account itself sent (matching `default_from`) are skipped.

Each account is polled independently; only INBOX is watched. The poller
also refreshes the folder cache the Email Accounts context block renders,
so a handler woken by the poller sees real folder names.

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

Custom tasks can be created via the `task_schedule` tool. Built-in
recurring work runs as service loop definitions instead — for
example, the `ego` loop maintains `self/ego.md` with bounded voluntary
sleep and supervisor randomization, and the `email-poller` loop drives
IMAP polling.

## RSS/Atom Feed Polling

Periodic checks for new entries on followed feeds (RSS, Atom, YouTube
channels). When new entries are detected, the agent wakes to process them.
Polling intervals and feed lists are managed via the `media_follow` and
`media_unfollow` tools.

Feeds may also target an existing loop with `wake_loop`. In that mode the
poller sends structured `feed_entry` events through the loop notification
path, allowing a `thane_loop_create` (`operation: service`) loop to own the output document, path, and
tagging strategy.

## Code Forge Repository Events

Repository event subscriptions are managed with `forge_repo_follow`,
`forge_repo_unfollow`, and `forge_repo_subscriptions`. Each subscription
tracks new releases, commits, or both with high-water marks stored in
opstate. A subscription may also expose a named repository root by setting
`repo_root` to a stable handle such as `thanecode`. Thane derives the physical
checkout path beneath `workspace.path`, registers the root as read-only, and
maintains the mirror before event delivery. Host filesystem paths are not part
of the model-facing contract. Omitting `repo_root` creates an event-only
subscription, even when the caller itself carries a `repo_root` binding; a
binding constrains an explicitly named new root but does not synthesize one.

`forge_repo_follow` performs the initial clone itself, so a checkout
exists on disk when the call returns; a clone that fails fails the
call rather than storing a path nothing will ever create. The poller
keeps it current thereafter. With `forge.subscription_check_interval`
unset or zero the checkout is still created and accurate as of that
moment, but nothing refreshes it and the subscription wakes no loop —
the tool says so in its response.

Unlike legacy pollers that start a fresh generic conversation, forge
subscriptions require `wake_loop`. New `release` and `commit` events are
delivered to the named loop as structured event-source notifications, so the
receiving `thane_loop_create` or other `thane_` loop remains the owner of durable
documents and corpus conventions. When a repository root is configured,
event metadata includes `repo_root` and `last_synced_sha`, and
unfollowing the subscription leaves the checkout on disk.

The receiving loop can use `file_read`, `file_tree`, `file_search`, and
`file_grep` against the root prefix, and `repo_git_log`, `repo_git_diff`,
`repo_git_show`, and `repo_git_blame` for scoped history. Binding the loop
with `repo_root: thanecode` makes unqualified relative file paths and omitted
git-tool roots resolve to `thanecode` and refuses every other root.
