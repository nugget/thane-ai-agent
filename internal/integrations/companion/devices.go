package companion

import (
	"encoding/json"
	"strings"
	"time"
)

// DeviceMetadata is the client-reported device description carried from
// the auth handshake into the durable inventory. Every field is
// optional; the inventory treats absent fields as "not reported", not
// as an erasure of what the device said before.
type DeviceMetadata struct {
	ClientName string
	Platform   string
	AppVersion string
	OSVersion  string
}

// DeviceRecorder receives durable device-inventory events from the
// connection lifecycle. Connections are ephemeral and records are not:
// a recorder must treat disconnects as timestamp updates, never as
// deletions (#1437). The handler calls it best-effort — a recording
// failure is logged and must not affect the live connection.
type DeviceRecorder interface {
	// RecordConnected upserts the device row for a successful
	// authentication, stamping first-seen on new devices.
	RecordConnected(account, clientID string, meta DeviceMetadata, at time.Time) error
	// RecordCapabilities stores the most recently advertised capability
	// manifest (opaque JSON) for the device.
	RecordCapabilities(account, clientID string, manifest []byte, at time.Time) error
	// RecordDisconnected stamps the disconnect time for the device.
	RecordDisconnected(account, clientID string, at time.Time) error
}

// SetDeviceRecorder wires durable device-inventory recording into the
// connection lifecycle. Optional: a nil recorder (the default) disables
// persistence and the handler behaves as a pure live-connection
// endpoint.
func (h *Handler) SetDeviceRecorder(recorder DeviceRecorder) {
	h.devices = recorder
}

// recordConnected persists the auth-time device upsert. Connections
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
	err := h.devices.RecordConnected(p.Account, p.ClientID, DeviceMetadata{
		ClientName: p.ClientName,
		Platform:   p.Platform,
		AppVersion: p.AppVersion,
		OSVersion:  p.OSVersion,
	}, time.Now().UTC())
	if err != nil {
		h.logger.Warn("companion device inventory connect record failed",
			"provider_id", p.ID,
			"account", p.Account,
			"client_id", p.ClientID,
			"error", err,
		)
	}
}

// recordCapabilities persists the normalized capability manifest the
// provider just registered.
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
	if err := h.devices.RecordCapabilities(p.Account, p.ClientID, manifest, time.Now().UTC()); err != nil {
		h.logger.Warn("companion device inventory capability record failed",
			"provider_id", p.ID,
			"account", p.Account,
			"client_id", p.ClientID,
			"error", err,
		)
	}
}

// recordDisconnected stamps the disconnect on the durable record.
func (h *Handler) recordDisconnected(p *Provider) {
	if h.devices == nil || strings.TrimSpace(p.ClientID) == "" {
		return
	}
	if err := h.devices.RecordDisconnected(p.Account, p.ClientID, time.Now().UTC()); err != nil {
		h.logger.Warn("companion device inventory disconnect record failed",
			"provider_id", p.ID,
			"account", p.Account,
			"client_id", p.ClientID,
			"error", err,
		)
	}
}
