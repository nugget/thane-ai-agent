package companion

import "github.com/nugget/thane-ai-agent/internal/platform/database"

var observationSchema = database.Schema{
	Name: "companion/observations",
	Steps: []database.MigrationStep{
		database.TableCreate{
			Table: "companion_devices",
			SQL: `CREATE TABLE IF NOT EXISTS companion_devices (
				account                     TEXT NOT NULL,
				device_identity             TEXT NOT NULL,
				client_id                   TEXT NOT NULL DEFAULT '',
				client_name                 TEXT NOT NULL DEFAULT '',
				platform                    TEXT NOT NULL DEFAULT '',
				app_version                 TEXT NOT NULL DEFAULT '',
				os_version                  TEXT NOT NULL DEFAULT '',
				capability_manifest_version INTEGER NOT NULL DEFAULT 0,
				capability_manifest         TEXT,
				capabilities_updated_at      TIMESTAMP,
				first_seen_at               TIMESTAMP NOT NULL,
				last_seen_at                TIMESTAMP NOT NULL,
				last_connected_at           TIMESTAMP,
				last_disconnected_at        TIMESTAMP,
				PRIMARY KEY (account, device_identity)
			)`,
		},
		database.TableCreate{
			Table: "companion_latest_observations",
			SQL: `CREATE TABLE IF NOT EXISTS companion_latest_observations (
				account         TEXT NOT NULL,
				device_identity TEXT NOT NULL,
				kind            TEXT NOT NULL,
				event_id        TEXT NOT NULL,
				schema_version  INTEGER NOT NULL,
				status          TEXT NOT NULL,
				observed_at     TIMESTAMP NOT NULL,
				received_at     TIMESTAMP NOT NULL,
				payload         TEXT,
				PRIMARY KEY (account, device_identity, kind),
				FOREIGN KEY (account, device_identity)
					REFERENCES companion_devices(account, device_identity)
			)`,
		},
		database.IndexCreate{
			Name: "idx_companion_latest_kind",
			SQL: `CREATE INDEX IF NOT EXISTS idx_companion_latest_kind
				ON companion_latest_observations(kind, account, device_identity)`,
		},
		database.IndexCreate{
			Name: "idx_companion_devices_client_id",
			SQL: `CREATE INDEX IF NOT EXISTS idx_companion_devices_client_id
				ON companion_devices(account, client_id)`,
		},
	},
}
