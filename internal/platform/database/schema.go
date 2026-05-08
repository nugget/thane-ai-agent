package database

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// Schema declares a store's full SQL schema as an ordered sequence of
// migration steps. Steps are applied in order; each step is idempotent
// against an empty database AND against any prior version of the schema.
//
// Use TableCreate for initial CREATE TABLE IF NOT EXISTS statements.
// Use ColumnAdd for additive schema changes that need to apply to
// existing deployments. Use IndexCreate for indexes that aren't
// declared inline. Use Raw for SQL that doesn't fit any of the above.
//
// Stores should declare a Schema in a package-private schema.go and
// apply it from NewStore via Migrate. Schema versioning and
// down-migrations are deliberately out of scope; the IF-NOT-EXISTS +
// additive ColumnAdd model is sufficient for forward-only evolution.
type Schema struct {
	// Name identifies the store in error messages and migration logs.
	Name string

	// Steps are applied in order. Each step must be idempotent.
	Steps []MigrationStep
}

// MigrationStep is a single idempotent unit of schema evolution.
type MigrationStep interface {
	// Apply runs the step against db. Implementations must be
	// idempotent: a second Apply on the same db must be a no-op.
	Apply(db *sql.DB) error

	// Describe returns a short human-readable label for migration
	// logs. The label should identify what changed (table or column
	// name), not the SQL itself.
	Describe() string
}

// TableCreate creates a table with CREATE TABLE IF NOT EXISTS. SQL
// must be the full CREATE statement including any inline indexes or
// constraints. Embedded CREATE INDEX IF NOT EXISTS statements are
// supported via db.Exec semantics.
type TableCreate struct {
	Table string
	SQL   string
}

// Apply executes the CREATE TABLE statement.
func (s TableCreate) Apply(db *sql.DB) error {
	if _, err := db.Exec(s.SQL); err != nil {
		return fmt.Errorf("create table %s: %w", s.Table, err)
	}
	return nil
}

// Describe returns "table <name>".
func (s TableCreate) Describe() string {
	return "table " + s.Table
}

// ColumnAdd adds a column to an existing table if it does not already
// exist. The Typedef parameter carries the SQL type and any inline
// constraints (for example "TEXT NOT NULL DEFAULT ”").
type ColumnAdd struct {
	Table   string
	Column  string
	Typedef string
}

// Apply adds the column when missing and is a no-op otherwise.
func (s ColumnAdd) Apply(db *sql.DB) error {
	return AddColumn(db, s.Table, s.Column, s.Typedef)
}

// Describe returns "column <table>.<column>".
func (s ColumnAdd) Describe() string {
	return "column " + s.Table + "." + s.Column
}

// IndexCreate creates an index. SQL must be the full CREATE INDEX
// statement (including IF NOT EXISTS for idempotency). Use this for
// indexes that aren't already inlined in a TableCreate.
type IndexCreate struct {
	Name string
	SQL  string
}

// Apply executes the CREATE INDEX statement.
func (s IndexCreate) Apply(db *sql.DB) error {
	if _, err := db.Exec(s.SQL); err != nil {
		return fmt.Errorf("create index %s: %w", s.Name, err)
	}
	return nil
}

// Describe returns "index <name>".
func (s IndexCreate) Describe() string {
	return "index " + s.Name
}

// Raw is an escape hatch for SQL that doesn't fit the typed steps
// above (for example virtual-table CREATE or one-off cleanup).
// Description is used in migration logs.
type Raw struct {
	Description string
	SQL         string
}

// Apply executes the raw SQL.
func (s Raw) Apply(db *sql.DB) error {
	if _, err := db.Exec(s.SQL); err != nil {
		return fmt.Errorf("%s: %w", s.Description, err)
	}
	return nil
}

// Describe returns the step's Description.
func (s Raw) Describe() string {
	return s.Description
}

// Migrate applies every step in schema.Steps to db in order. The first
// failing step halts migration and its error is returned wrapped with
// the schema name. On success a single info-level log line records the
// store name and the steps that ran.
func Migrate(db *sql.DB, schema Schema, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	applied := make([]string, 0, len(schema.Steps))
	for _, step := range schema.Steps {
		if err := step.Apply(db); err != nil {
			return fmt.Errorf("%s migrate: %w", schema.Name, err)
		}
		applied = append(applied, step.Describe())
	}

	logger.Info("schema migrated",
		"store", schema.Name,
		"steps", len(applied),
		"applied", strings.Join(applied, ", "),
	)
	return nil
}
