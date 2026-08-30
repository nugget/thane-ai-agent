package companions

import "github.com/nugget/thane-ai-agent/internal/platform/database"

// devicesSchema declares the companion_devices table: the durable
// companion-device inventory, keyed by (account, client_id) — the
// authenticated account plus the stable identity a companion claims
// about itself, never the per-connection provider ID (#1437).
//
// Rows outlive connections by design. A WebSocket disconnect updates
// timestamps; nothing in the connection lifecycle deletes a row.
// client_id is opaque: today it is the UUID companion apps persist
// locally, and the column deliberately carries no format so a future
// enrollment arc (#1444) can key devices by an attested key
// fingerprint without a schema break.
var devicesSchema = database.Schema{
	Name: "companions/devices",
	Steps: []database.MigrationStep{
		database.TableCreate{
			Table: "companion_devices",
			SQL: `CREATE TABLE IF NOT EXISTS companion_devices (
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
				state                TEXT NOT NULL DEFAULT 'active',
				PRIMARY KEY (account, client_id)
			)`,
		},
	},
}
