package companion

import "github.com/nugget/thane-ai-agent/internal/platform/database"

var observationSchema = database.Schema{
	Name: "companion/observations",
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
				PRIMARY KEY (account, client_id)
			)`,
		},
		database.TableCreate{
			Table: "companion_latest_observations",
			SQL: `CREATE TABLE IF NOT EXISTS companion_latest_observations (
				account        TEXT NOT NULL,
				client_id      TEXT NOT NULL,
				kind           TEXT NOT NULL,
				event_id       TEXT NOT NULL,
				schema_version INTEGER NOT NULL,
				status         TEXT NOT NULL,
				observed_at    TIMESTAMP NOT NULL,
				received_at    TIMESTAMP NOT NULL,
				payload        TEXT,
				PRIMARY KEY (account, client_id, kind),
				FOREIGN KEY (account, client_id)
					REFERENCES companion_devices(account, client_id)
			)`,
		},
		database.IndexCreate{
			Name: "idx_companion_latest_kind",
			SQL: `CREATE INDEX IF NOT EXISTS idx_companion_latest_kind
				ON companion_latest_observations(kind, account, client_id)`,
		},
	},
}
