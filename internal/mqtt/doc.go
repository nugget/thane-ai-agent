// Package mqtt publishes Home Assistant MQTT discovery messages,
// periodic sensor state updates, and subscribes to configured topics
// for ambient awareness. Thane appears as a native HA device with
// availability tracking.
//
// Phase 1 (publish): Sensor entities and availability.
// Phase 2 (subscribe): Topic subscriptions with structured logging.
// Future phases will route inbound messages to the anticipation engine
// for autonomous action.
//
// The publisher uses Eclipse Paho v2's [autopaho] package for
// connection management with automatic reconnection. On every
// (re-)connect it publishes retained discovery config payloads for
// each sensor entity, a birth message ("online") to the availability
// topic, and re-subscribes to all configured topic filters. A will
// message ensures the availability topic transitions to "offline" on
// unexpected disconnects.
package mqtt
