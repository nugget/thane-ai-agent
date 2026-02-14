// Package mqtt publishes Home Assistant MQTT discovery messages and
// periodic sensor state updates so Thane appears as a native HA
// device with availability tracking. Phase 1 is publish-only (sensors
// and availability); control entities (buttons, selects) are deferred
// to a later phase that adds MQTT subscriptions.
//
// The publisher uses Eclipse Paho v2's [autopaho] package for
// connection management with automatic reconnection. On every
// (re-)connect it publishes retained discovery config payloads for
// each sensor entity and a birth message ("online") to the
// availability topic. A will message ensures the availability topic
// transitions to "offline" on unexpected disconnects.
package mqtt
