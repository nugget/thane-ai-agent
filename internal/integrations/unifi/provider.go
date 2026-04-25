// Package unifi provides a client for the UniFi Network controller API
// and a provider interface for room-level presence detection via wireless
// AP client associations.
package unifi

import "context"

// DeviceLocation represents a wireless device's current network location
// as reported by a network controller. The MAC identifies the device,
// APName indicates which access point it is associated with, and Signal
// provides the RSSI in dBm.
type DeviceLocation struct {
	MAC      string // device MAC address (lowercase, colon-separated)
	APName   string // name of the AP the device is connected to
	Signal   int    // RSSI in dBm
	LastSeen int64  // Unix timestamp of last activity
}

// DeviceLocator provides wireless device location data from a network
// controller. The UniFi Client implements this interface; future
// implementations could support Meraki, Aruba, or other controllers.
type DeviceLocator interface {
	// LocateDevices returns the current wireless location of all known
	// devices. The returned slice may be large (100+ entries); callers
	// should filter by MAC addresses of interest.
	LocateDevices(ctx context.Context) ([]DeviceLocation, error)

	// Ping checks if the network controller is reachable.
	Ping(ctx context.Context) error
}
