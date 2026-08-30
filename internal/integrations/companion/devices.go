package companion

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// deviceOpTimeout bounds each inventory write so a wedged database can
// never back the recording queue up indefinitely.
const deviceOpTimeout = 5 * time.Second

// deviceOpQueueSize bounds the recording queue. Lifecycle events are
// rare (one per connect, capability registration, and disconnect), so
// a full queue means the database has been unhealthy for a while — at
// that point dropping records is the honest best-effort behavior.
const deviceOpQueueSize = 256

// DeviceMetadata is the client-reported device description carried from
// the auth handshake into the durable inventory. Every field is
// optional; the inventory treats absent fields as "not reported", not
// as an erasure of what the device said before. Values are normalized
// (trimmed) once, at authentication, and stored verbatim after that.
type DeviceMetadata struct {
	ClientName string
	Platform   string
	AppVersion string
	OSVersion  string
}

// DeviceRecorder receives durable device-inventory events from the
// connection lifecycle. Connections are ephemeral and records are not:
// a recorder must treat disconnects as timestamp updates, never as
// deletions (#1437). The handler calls it best-effort from a
// dedicated goroutine — a recording failure is logged and must not
// affect the live connection. The timestamps passed are event times,
// captured when the lifecycle event happened, not when the write runs.
type DeviceRecorder interface {
	// RecordConnected upserts the device row for a successful
	// authentication, stamping first-seen on new devices.
	RecordConnected(ctx context.Context, account, clientID string, meta DeviceMetadata, at time.Time) error
	// RecordCapabilities stores the most recently advertised capability
	// manifest (opaque JSON) for the device.
	RecordCapabilities(ctx context.Context, account, clientID string, manifest []byte, at time.Time) error
	// RecordDisconnected stamps the disconnect time for the device.
	RecordDisconnected(ctx context.Context, account, clientID string, at time.Time) error
}

// SetDeviceRecorder wires durable device-inventory recording into the
// connection lifecycle and starts the recording goroutine. Optional: a
// nil recorder (the default) disables persistence and the handler
// behaves as a pure live-connection endpoint. Call before the handler
// starts serving; the queue goroutine lives for the process lifetime.
func (h *Handler) SetDeviceRecorder(recorder DeviceRecorder) {
	h.devices = recorder
	if recorder == nil || h.deviceOps != nil {
		return
	}
	h.deviceOps = make(chan func(context.Context), deviceOpQueueSize)
	go h.runDeviceOps()
}

// runDeviceOps applies queued inventory writes in order. A single
// consumer keeps each connection's connect → capabilities → disconnect
// sequence ordered without ever putting a database write on the
// WebSocket handshake or teardown path.
func (h *Handler) runDeviceOps() {
	for op := range h.deviceOps {
		ctx, cancel := context.WithTimeout(context.Background(), deviceOpTimeout)
		op(ctx)
		cancel()
	}
}

// enqueueDeviceOp hands a write to the recording goroutine without
// blocking the connection path. Dropping on overflow is deliberate:
// inventory is best-effort relative to live traffic.
func (h *Handler) enqueueDeviceOp(op func(context.Context)) {
	select {
	case h.deviceOps <- op:
	default:
		h.logger.Warn("companion device inventory queue full; dropping record")
	}
}

// recordConnected enqueues the auth-time device upsert. Connections
// without a client_id have no durable identity to key a row by; they
// stay fully functional live but are deliberately not inventoried —
// the contract published on #1437.
func (h *Handler) recordConnected(p *Provider) {
	if h.devices == nil {
		return
	}
	if strings.TrimSpace(p.ClientID) == "" {
		h.logger.Debug("companion connection has no client_id; not recorded in device inventory",
			"provider_id", p.ID,
			"account", p.Account,
			"client_name", p.ClientName,
		)
		return
	}
	at := time.Now().UTC()
	h.enqueueDeviceOp(func(ctx context.Context) {
		err := h.devices.RecordConnected(ctx, p.Account, p.ClientID, DeviceMetadata{
			ClientName: p.ClientName,
			Platform:   p.Platform,
			AppVersion: p.AppVersion,
			OSVersion:  p.OSVersion,
		}, at)
		if err != nil {
			h.logger.Warn("companion device inventory connect record failed",
				"provider_id", p.ID,
				"account", p.Account,
				"client_id", p.ClientID,
				"error", err,
			)
		}
	})
}

// recordCapabilities enqueues the normalized capability manifest the
// provider just registered. The manifest is snapshotted now so a later
// re-registration cannot mutate what this event advertises.
func (h *Handler) recordCapabilities(p *Provider) {
	if h.devices == nil || strings.TrimSpace(p.ClientID) == "" {
		return
	}
	manifest, err := json.Marshal(p.capabilitiesSnapshot())
	if err != nil {
		h.logger.Warn("companion device inventory manifest marshal failed",
			"provider_id", p.ID,
			"account", p.Account,
			"client_id", p.ClientID,
			"error", err,
		)
		return
	}
	at := time.Now().UTC()
	h.enqueueDeviceOp(func(ctx context.Context) {
		if err := h.devices.RecordCapabilities(ctx, p.Account, p.ClientID, manifest, at); err != nil {
			h.logger.Warn("companion device inventory capability record failed",
				"provider_id", p.ID,
				"account", p.Account,
				"client_id", p.ClientID,
				"error", err,
			)
		}
	})
}

// recordDisconnected enqueues the disconnect stamp for the durable
// record.
func (h *Handler) recordDisconnected(p *Provider) {
	if h.devices == nil || strings.TrimSpace(p.ClientID) == "" {
		return
	}
	at := time.Now().UTC()
	h.enqueueDeviceOp(func(ctx context.Context) {
		if err := h.devices.RecordDisconnected(ctx, p.Account, p.ClientID, at); err != nil {
			h.logger.Warn("companion device inventory disconnect record failed",
				"provider_id", p.ID,
				"account", p.Account,
				"client_id", p.ClientID,
				"error", err,
			)
		}
	})
}
