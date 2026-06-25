package database

import (
	"database/sql"
	"database/sql/driver"
	"strings"

	sqlite "modernc.org/sqlite"
)

// DriverName is the SQLite driver every thane store opens against. It is a
// thin wrapper around modernc.org/sqlite that forces a deterministic
// time.Time serialization on every connection.
//
// Why the wrapper exists: modernc's default time.Time binding uses Go's
// time.Time.String() layout ("2006-01-02 15:04:05.999999999 -0700 MST"),
// which SQLite's strftime() cannot parse (collapsing normalized sort keys
// to NULL and breaking keyset pagination), which ParseTimestamp rejects,
// and which does not sort lexically against the historical mattn-written
// rows already in production databases. modernc registers itself under the
// name "sqlite" in its own init() and database/sql panics on a duplicate
// registration, so the "sqlite" name cannot be shadowed and modernc
// exposes no hook to set a default _time_format globally. The wrapper
// therefore registers under a distinct name and injects
// _time_format=sqlite into every DSN, which makes modernc emit
// "2006-01-02 15:04:05.999999999-07:00" — byte-identical to
// SQLiteTimestampLayout / FormatTimestamp and to mattn's prior output, so
// old and new rows remain mutually readable and orderable.
const DriverName = "sqlite-thane"

func init() {
	sql.Register(DriverName, tsDriver{delegate: &sqlite.Driver{}})
}

// tsDriver wraps modernc's driver and forces _time_format=sqlite on open.
type tsDriver struct {
	delegate driver.Driver
}

func (d tsDriver) Open(dsn string) (driver.Conn, error) {
	return d.delegate.Open(injectTimeFormat(dsn))
}

// injectTimeFormat appends _time_format=sqlite to a DSN unless the caller
// already pinned the format. It selects the correct query separator so the
// parameter is honored even on a bare DSN like ":memory:" (which has no
// "?" yet) — modernc only parses query parameters that follow a "?".
func injectTimeFormat(dsn string) string {
	if strings.Contains(dsn, "_time_format=") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "_time_format=sqlite"
}
