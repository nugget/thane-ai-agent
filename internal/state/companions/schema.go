package companions

import "github.com/nugget/thane-ai-agent/internal/platform/database"

// devicesSchema declares the companion_devices table: the durable
// companion-device inventory (#1437).
//
// device_id is the immutable server-assigned primary key, minted once
// when a device is first seen. (account, client_id) is how devices are
// looked up today — the authenticated account plus the stable identity
// a companion claims about itself, never the per-connection provider
// ID — but it is a credential mapping, not the identity itself: the
// enrollment arc (#1444) makes key replacement and re-enrollment
// normal, so anything that must survive credential rotation (future
// observation rows included) references device_id, and rotating a
// claim re-points the mapping instead of minting a second device.
//
// Rows outlive connections by design. A WebSocket disconnect updates
// timestamps; nothing in the connection lifecycle deletes a row.
var devicesSchema = database.Schema{
	Name: "companions/devices",
	Steps: []database.MigrationStep{
		database.TableCreate{
			Table: "companion_devices",
			SQL: `CREATE TABLE IF NOT EXISTS companion_devices (
				device_id            TEXT NOT NULL PRIMARY KEY,
				account              TEXT NOT NULL,
				client_id            TEXT NOT NULL,
				client_name          TEXT NOT NULL DEFAULT '',
				platform             TEXT NOT NULL DEFAULT '',
				app_version          TEXT NOT NULL DEFAULT '',
				os_version           TEXT NOT NULL DEFAULT '',
				first_seen_at        TIMESTAMP NOT NULL,
				last_seen_at         TIMESTAMP NOT NULL,
				last_connected_at    TIMESTAMP,
				last_disconnected_at TIMESTAMP,
				capabilities         TEXT NOT NULL DEFAULT '[]',
				capabilities_recorded_at TIMESTAMP,
				state                TEXT NOT NULL DEFAULT 'active',
				UNIQUE (account, client_id)
			)`,
		},
		// companion_observations holds the latest observation per
		// (device, kind) — one current record, not a history (#1437's
		// deliberate retention contract). Rows reference the immutable
		// device_id so they survive credential rotation (#1444).
		// observed_at is the device's claim, received_at the server's
		// receipt time; status 'withdrawn' keeps provenance while the
		// payload is cleared.
		database.TableCreate{
			Table: "companion_observations",
			SQL: `CREATE TABLE IF NOT EXISTS companion_observations (
				device_id      TEXT NOT NULL,
				kind           TEXT NOT NULL,
				event_id       TEXT NOT NULL,
				schema_version INTEGER NOT NULL,
				observed_at    TIMESTAMP NOT NULL,
				received_at    TIMESTAMP NOT NULL,
				payload        TEXT NOT NULL DEFAULT '{}',
				status         TEXT NOT NULL DEFAULT 'present',
				PRIMARY KEY (device_id, kind)
			)`,
		},
	},
}
