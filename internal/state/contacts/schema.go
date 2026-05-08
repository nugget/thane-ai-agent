package contacts

import "github.com/nugget/thane-ai-agent/internal/platform/database"

// schema declares the contact tables and their non-conditional indexes.
// The case-insensitive unique active-name index is set up separately in
// NewStore — it's allowed to fail (the store warns and falls back) and
// so does not belong in a hard-failing migration. FTS5 is similarly
// optional and handled in tryEnableFTS.
var schema = database.Schema{
	Name: "contacts",
	Steps: []database.MigrationStep{
		database.TableCreate{
			Table: "contacts",
			SQL: `CREATE TABLE IF NOT EXISTS contacts (
				id TEXT PRIMARY KEY,
				kind TEXT NOT NULL DEFAULT 'individual',
				formatted_name TEXT NOT NULL,
				family_name TEXT,
				given_name TEXT,
				additional_names TEXT,
				name_prefix TEXT,
				name_suffix TEXT,
				nickname TEXT,
				birthday TEXT,
				anniversary TEXT,
				gender TEXT,
				org TEXT,
				title TEXT,
				role TEXT,
				note TEXT,
				photo_uri TEXT,
				trust_zone TEXT NOT NULL DEFAULT 'known',
				ai_summary TEXT,
				rev TEXT NOT NULL,
				etag TEXT,
				embedding BLOB,
				last_interaction TEXT,
				last_interaction_meta TEXT,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL,
				deleted_at TEXT
			)`,
		},
		database.IndexCreate{Name: "idx_contacts_kind", SQL: `CREATE INDEX IF NOT EXISTS idx_contacts_kind ON contacts(kind)`},
		database.IndexCreate{Name: "idx_contacts_fn", SQL: `CREATE INDEX IF NOT EXISTS idx_contacts_fn ON contacts(formatted_name)`},
		database.IndexCreate{Name: "idx_contacts_deleted", SQL: `CREATE INDEX IF NOT EXISTS idx_contacts_deleted ON contacts(deleted_at)`},
		database.IndexCreate{Name: "idx_contacts_trust_zone", SQL: `CREATE INDEX IF NOT EXISTS idx_contacts_trust_zone ON contacts(trust_zone)`},
		database.TableCreate{
			Table: "contact_properties",
			SQL: `CREATE TABLE IF NOT EXISTS contact_properties (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				contact_id TEXT NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
				property TEXT NOT NULL,
				value TEXT NOT NULL,
				type TEXT,
				pref INTEGER,
				label TEXT,
				mediatype TEXT,
				verified INTEGER DEFAULT 0,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
		},
		database.IndexCreate{Name: "idx_cp_contact", SQL: `CREATE INDEX IF NOT EXISTS idx_cp_contact ON contact_properties(contact_id)`},
		database.IndexCreate{Name: "idx_cp_property", SQL: `CREATE INDEX IF NOT EXISTS idx_cp_property ON contact_properties(property)`},
		database.IndexCreate{Name: "idx_cp_property_value", SQL: `CREATE INDEX IF NOT EXISTS idx_cp_property_value ON contact_properties(property, value)`},
		database.IndexCreate{Name: "idx_cp_value", SQL: `CREATE INDEX IF NOT EXISTS idx_cp_value ON contact_properties(value)`},
	},
}
