package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func defaultEvalDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "Thane/evals"
	}
	return filepath.Join(home, "Thane", "evals")
}

func openLogsReadOnly(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve logs database: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("logs database unavailable at %s: %w", abs, err)
	}
	dsnURL := url.URL{Scheme: "file", Path: abs}
	query := dsnURL.Query()
	query.Set("mode", "ro")
	query.Set("_pragma", "busy_timeout(5000)")
	dsnURL.RawQuery = query.Encode()
	dsn := dsnURL.String()
	db, err := sql.Open(database.DriverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open logs database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("read logs database: %w", err)
	}
	return db, nil
}

func writePrivateJSON(path string, value any, overwrite bool) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve output path: %w", err)
	}
	inside, root, err := insideGitWorktree(abs)
	if err != nil {
		return err
	}
	if inside {
		return fmt.Errorf("refusing to write production-derived data inside Git worktree %s; choose a private path such as %s", root, defaultEvalDir())
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return fmt.Errorf("create private output directory: %w", err)
	}
	flags := os.O_WRONLY | os.O_CREATE
	if overwrite {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(abs, flags, 0o600)
	if err != nil {
		return fmt.Errorf("create private output %s: %w", abs, err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return fmt.Errorf("secure private output %s: %w", abs, err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	encodeErr := encoder.Encode(value)
	closeErr := file.Close()
	if encodeErr != nil {
		return fmt.Errorf("encode private output: %w", encodeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close private output: %w", closeErr)
	}
	return nil
}

func insideGitWorktree(path string) (bool, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return false, "", fmt.Errorf("resolve working directory: %w", err)
	}
	root := cwd
	for {
		if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return false, "", fmt.Errorf("inspect Git worktree: %w", err)
		}
		parent := filepath.Dir(root)
		if parent == root {
			return false, "", nil
		}
		root = parent
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false, "", fmt.Errorf("resolve Git worktree root: %w", err)
	}
	rel, err := filepath.Rel(absRoot, path)
	if err != nil {
		return false, "", fmt.Errorf("compare output with Git worktree: %w", err)
	}
	inside := rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
	return inside, absRoot, nil
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("value must not be empty")
	}
	*s = append(*s, value)
	return nil
}
