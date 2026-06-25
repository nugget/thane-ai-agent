package database

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// bareSQLiteOpenPattern matches a call that opens the bare modernc driver
// name ("sqlite" or the removed "sqlite3") in either string-literal form —
// double-quoted or raw backtick — with optional whitespace after the
// paren, so the guard cannot be evaded by switching literal style. The
// DriverName constant and "sqlite-thane" are intentionally not matched
// (the "-thane" suffix follows the name).
var bareSQLiteOpenPattern = regexp.MustCompile("sql\\.Open\\(\\s*[\"`]sqlite3?[\"`]")

// TestNoBareSQLiteOpenOutsideDatabasePackage enforces that every SQLite
// connection in the codebase opens through the [DriverName] wrapper, which
// forces modernc's _time_format=sqlite so time.Time serializes to
// [SQLiteTimestampLayout]. A raw sql.Open("sqlite", ...) (or the removed
// "sqlite3") would instead emit modernc's default time.Time.String()
// shape, silently stranding rows in a format that strftime cannot parse —
// breaking ORDER BY normalization and keyset pagination — and that does
// not sort lexically against existing rows. Open via Open/OpenMemory or
// the DriverName constant; never name the bare driver directly.
func TestNoBareSQLiteOpenOutsideDatabasePackage(t *testing.T) {
	root := moduleRoot(t)
	selfDir := filepath.Join(root, "internal", "platform", "database")
	skipDirs := map[string]bool{"vendor": true, "dist": true, "node_modules": true}

	var offenders []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip hidden directories (.git, .claude/worktrees, etc.) and
			// non-source trees — nested worktrees carry their own checkout.
			if name := d.Name(); name != "." && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// The database package is the one legitimate place that names the
		// underlying driver (to register and delegate to the wrapper).
		if filepath.Dir(path) == selfDir {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bareSQLiteOpenPattern.Match(data) {
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("bare sql.Open(\"sqlite\"|\"sqlite3\") found outside internal/platform/database — "+
			"open via database.Open/OpenMemory or the DriverName constant instead:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// TestBareSQLiteOpenPattern pins the guard regex against evasion forms:
// both quote styles and whitespace must be caught, while the wrapped
// driver name and the DriverName constant must pass.
func TestBareSQLiteOpenPattern(t *testing.T) {
	shouldMatch := []string{
		`sql.Open("sqlite", dsn)`,
		`sql.Open("sqlite3", dsn)`,
		"sql.Open(`sqlite`, dsn)",       // raw string literal
		"sql.Open(`sqlite3`, dsn)",      // raw string literal
		`sql.Open( "sqlite" , dsn)`,     // whitespace after paren
		"sql.Open(\n\t\t\"sqlite\", x)", // newline after paren
	}
	shouldNotMatch := []string{
		`sql.Open("sqlite-thane", dsn)`, // the wrapper name
		"sql.Open(`sqlite-thane`, dsn)",
		`sql.Open(DriverName, dsn)`, // the constant
		`sql.Open("postgres", dsn)`,
	}
	for _, s := range shouldMatch {
		if !bareSQLiteOpenPattern.MatchString(s) {
			t.Errorf("pattern should match %q but did not", s)
		}
	}
	for _, s := range shouldNotMatch {
		if bareSQLiteOpenPattern.MatchString(s) {
			t.Errorf("pattern should NOT match %q but did", s)
		}
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above test working directory")
		}
		dir = parent
	}
}
