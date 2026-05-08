package memory

import "github.com/nugget/thane-ai-agent/internal/platform/database"

// schema declares the working-memory tables (conversations, messages,
// tool calls, entity facts, preferences) plus the additive lifecycle
// columns introduced by the storage unification (#434).
//
// SQLiteStore is the documented exception to the standardized
// NewStore(db, logger) shape: it owns thane.db itself, since every
// other store on the main connection consumes the handle that
// SQLiteStore opens.
var schema = database.Schema{
	Name: "memory",
	Steps: []database.MigrationStep{
		database.TableCreate{
			Table: "conversations",
			SQL: `CREATE TABLE IF NOT EXISTS conversations (
				id TEXT PRIMARY KEY,
				created_at TIMESTAMP NOT NULL,
				updated_at TIMESTAMP NOT NULL,
				metadata TEXT
			)`,
		},
		database.TableCreate{
			Table: "messages",
			SQL: `CREATE TABLE IF NOT EXISTS messages (
				id TEXT PRIMARY KEY,
				conversation_id TEXT NOT NULL,
				session_id TEXT,
				role TEXT NOT NULL,
				content TEXT NOT NULL,
				timestamp TIMESTAMP NOT NULL,
				token_count INTEGER DEFAULT 0,
				tool_calls TEXT,
				tool_call_id TEXT,
				status TEXT DEFAULT 'active' CHECK (status IN ('active', 'compacted', 'archived')),
				archived_at TIMESTAMP,
				archive_reason TEXT,
				iteration_index INTEGER,
				FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
			)`,
		},
		database.IndexCreate{Name: "idx_messages_conversation", SQL: `CREATE INDEX IF NOT EXISTS idx_messages_conversation ON messages(conversation_id, timestamp)`},
		database.IndexCreate{Name: "idx_messages_session", SQL: `CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, timestamp)`},
		database.IndexCreate{Name: "idx_messages_status", SQL: `CREATE INDEX IF NOT EXISTS idx_messages_status ON messages(conversation_id, status)`},
		database.TableCreate{
			Table: "tool_calls",
			SQL: `CREATE TABLE IF NOT EXISTS tool_calls (
				id TEXT PRIMARY KEY,
				message_id TEXT,
				conversation_id TEXT NOT NULL,
				session_id TEXT,
				tool_name TEXT NOT NULL,
				arguments TEXT NOT NULL,
				result TEXT,
				error TEXT,
				started_at TIMESTAMP NOT NULL,
				completed_at TIMESTAMP,
				duration_ms INTEGER,
				status TEXT DEFAULT 'active' CHECK (status IN ('active', 'archived')),
				archived_at TIMESTAMP,
				iteration_index INTEGER,
				FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE SET NULL,
				FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
			)`,
		},
		database.IndexCreate{Name: "idx_tool_calls_conversation", SQL: `CREATE INDEX IF NOT EXISTS idx_tool_calls_conversation ON tool_calls(conversation_id, started_at)`},
		database.IndexCreate{Name: "idx_tool_calls_tool", SQL: `CREATE INDEX IF NOT EXISTS idx_tool_calls_tool ON tool_calls(tool_name)`},
		database.IndexCreate{Name: "idx_tool_calls_message", SQL: `CREATE INDEX IF NOT EXISTS idx_tool_calls_message ON tool_calls(message_id)`},
		database.IndexCreate{Name: "idx_tool_calls_session", SQL: `CREATE INDEX IF NOT EXISTS idx_tool_calls_session ON tool_calls(session_id, started_at)`},
		database.IndexCreate{Name: "idx_tool_calls_status", SQL: `CREATE INDEX IF NOT EXISTS idx_tool_calls_status ON tool_calls(conversation_id, status)`},
		database.TableCreate{
			Table: "entity_facts",
			SQL: `CREATE TABLE IF NOT EXISTS entity_facts (
				id TEXT PRIMARY KEY,
				entity_id TEXT NOT NULL,
				fact_type TEXT NOT NULL,
				content TEXT NOT NULL,
				source TEXT NOT NULL,
				confidence REAL DEFAULT 1.0,
				created_at TIMESTAMP NOT NULL,
				updated_at TIMESTAMP NOT NULL,
				valid_until TIMESTAMP
			)`,
		},
		database.IndexCreate{Name: "idx_entity_facts_entity", SQL: `CREATE INDEX IF NOT EXISTS idx_entity_facts_entity ON entity_facts(entity_id)`},
		database.TableCreate{
			Table: "preferences",
			SQL: `CREATE TABLE IF NOT EXISTS preferences (
				id TEXT PRIMARY KEY,
				category TEXT NOT NULL,
				key TEXT NOT NULL,
				value TEXT NOT NULL,
				context TEXT,
				confidence REAL DEFAULT 1.0,
				learned_from TEXT,
				created_at TIMESTAMP NOT NULL,
				updated_at TIMESTAMP NOT NULL
			)`,
		},
		// Lifecycle columns added by storage unification (#434). New
		// databases get these from the CREATE TABLE above; existing
		// pre-unification databases get them via these idempotent adds.
		database.ColumnAdd{Table: "messages", Column: "session_id", Typedef: "TEXT"},
		database.ColumnAdd{Table: "messages", Column: "status", Typedef: "TEXT DEFAULT 'active' CHECK (status IN ('active', 'compacted', 'archived'))"},
		database.ColumnAdd{Table: "messages", Column: "archived_at", Typedef: "TIMESTAMP"},
		database.ColumnAdd{Table: "messages", Column: "archive_reason", Typedef: "TEXT"},
		database.ColumnAdd{Table: "messages", Column: "iteration_index", Typedef: "INTEGER"},
		database.ColumnAdd{Table: "tool_calls", Column: "session_id", Typedef: "TEXT"},
		database.ColumnAdd{Table: "tool_calls", Column: "status", Typedef: "TEXT DEFAULT 'active' CHECK (status IN ('active', 'archived'))"},
		database.ColumnAdd{Table: "tool_calls", Column: "archived_at", Typedef: "TIMESTAMP"},
		database.ColumnAdd{Table: "tool_calls", Column: "iteration_index", Typedef: "INTEGER"},
	},
}
