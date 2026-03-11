package main

import (
	"database/sql"

	"github.com/nugget/thane-ai-agent/internal/logging"
)

// logQueryAdapter bridges the web package's [web.LogQuerier] interface
// to the [logging.Query] function, keeping the web package decoupled
// from database/sql.
type logQueryAdapter struct {
	db *sql.DB
}

// Query delegates to [logging.Query].
func (a *logQueryAdapter) Query(params logging.QueryParams) ([]logging.LogEntry, error) {
	return logging.Query(a.db, params)
}
