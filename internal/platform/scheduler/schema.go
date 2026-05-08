package scheduler

import "github.com/nugget/thane-ai-agent/internal/platform/database"

// schema declares the scheduler's tasks and executions tables plus
// supporting indexes.
var schema = database.Schema{
	Name: "scheduler",
	Steps: []database.MigrationStep{
		database.TableCreate{
			Table: "tasks",
			SQL: `CREATE TABLE IF NOT EXISTS tasks (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				schedule_json TEXT NOT NULL,
				payload_json TEXT NOT NULL,
				enabled INTEGER NOT NULL DEFAULT 1,
				created_at TEXT NOT NULL,
				created_by TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
		},
		database.TableCreate{
			Table: "executions",
			SQL: `CREATE TABLE IF NOT EXISTS executions (
				id TEXT PRIMARY KEY,
				task_id TEXT NOT NULL,
				scheduled_at TEXT NOT NULL,
				started_at TEXT,
				completed_at TEXT,
				status TEXT NOT NULL,
				result TEXT,
				FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
			)`,
		},
		database.IndexCreate{
			Name: "idx_tasks_name",
			SQL:  `CREATE INDEX IF NOT EXISTS idx_tasks_name ON tasks(name)`,
		},
		database.IndexCreate{
			Name: "idx_executions_task_id",
			SQL:  `CREATE INDEX IF NOT EXISTS idx_executions_task_id ON executions(task_id)`,
		},
		database.IndexCreate{
			Name: "idx_executions_status",
			SQL:  `CREATE INDEX IF NOT EXISTS idx_executions_status ON executions(status)`,
		},
		database.IndexCreate{
			Name: "idx_executions_scheduled_at",
			SQL:  `CREATE INDEX IF NOT EXISTS idx_executions_scheduled_at ON executions(scheduled_at)`,
		},
	},
}
