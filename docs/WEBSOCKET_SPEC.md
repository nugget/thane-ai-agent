# Home Assistant WebSocket Integration

## Overview

Thane currently uses the HA REST API for state queries and service calls. This works for reactive conversations but doesn't support:
- Real-time event subscriptions (zone enter/exit, state changes)
- Full registry access (areas with entity relationships)
- Anticipation triggers based on HA events

The WebSocket API is HA's primary integration channel and enables all of the above.

## Connection

```
wss://homeassistant.hollowoak.net/api/websocket
```

### Authentication Flow

```json
// Server sends auth_required
{"type": "auth_required", "ha_version": "2024.1.0"}

// Client responds with token
{"type": "auth", "access_token": "eyJ..."}

// Server confirms
{"type": "auth_ok", "ha_version": "2024.1.0"}
```

## Key Message Types

### Subscribe to Events

```json
// Subscribe to all state changes
{
  "id": 1,
  "type": "subscribe_events",
  "event_type": "state_changed"
}

// Subscribe to specific event type
{
  "id": 2,
  "type": "subscribe_events",
  "event_type": "zone.enter"
}
```

### Event Delivery

```json
{
  "id": 1,
  "type": "event",
  "event": {
    "event_type": "state_changed",
    "data": {
      "entity_id": "person.nugget",
      "old_state": {"state": "home"},
      "new_state": {"state": "not_home"}
    }
  }
}
```

### Registry Access

```json
// Get area registry (includes entity assignments!)
{"id": 3, "type": "config/area_registry/list"}

// Get device registry
{"id": 4, "type": "config/device_registry/list"}

// Get entity registry
{"id": 5, "type": "config/entity_registry/list"}
```

### Call Service (alternative to REST)

```json
{
  "id": 6,
  "type": "call_service",
  "domain": "light",
  "service": "turn_on",
  "target": {"entity_id": "light.office"},
  "service_data": {"brightness": 255}
}
```

## Implementation Plan

### Phase 1: Connection Manager

```go
type WSClient struct {
    conn     *websocket.Conn
    token    string
    baseURL  string
    msgID    atomic.Int64
    handlers map[int64]chan json.RawMessage
    events   chan Event
}

func (c *WSClient) Connect(ctx context.Context) error
func (c *WSClient) Subscribe(eventType string) error
func (c *WSClient) Events() <-chan Event
```

### Phase 2: Event Router

Route incoming events to anticipation matcher:

```go
type EventRouter struct {
    ws           *WSClient
    anticipations *anticipation.Store
}

func (r *EventRouter) Run(ctx context.Context) {
    for event := range r.ws.Events() {
        matches := r.anticipations.MatchTrigger(event)
        for _, ant := range matches {
            r.fireAnticipation(ant, event)
        }
    }
}
```

### Phase 3: Registry Cache

Cache registries for `find_entity` area filtering:

```go
type RegistryCache struct {
    areas    []Area
    devices  []Device
    entities []Entity
    mu       sync.RWMutex
}

func (c *RegistryCache) Refresh(ctx context.Context) error
func (c *RegistryCache) EntityArea(entityID string) string
```

## Anticipation Trigger Matching

Current trigger types map to WebSocket events:

| Trigger Type | WebSocket Event |
|-------------|-----------------|
| `after_time` | Internal timer (no WS needed) |
| `entity_state` | `state_changed` where entity_id matches |
| `zone_enter` | `state_changed` on `person.*` entering zone |
| `zone_exit` | `state_changed` on `person.*` leaving zone |
| `event_type` | Direct event subscription |

## Error Handling

- Automatic reconnection with exponential backoff
- Subscription restoration after reconnect
- Graceful degradation to REST API if WS unavailable

## Security Considerations

- Same token as REST API (long-lived access token)
- TLS required (wss://)
- No additional permissions needed beyond existing token scope

## References

- [HA WebSocket API docs](https://developers.home-assistant.io/docs/api/websocket)
- [gorilla/websocket](https://github.com/gorilla/websocket) — Go WebSocket library
