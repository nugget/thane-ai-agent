package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/defaults"
	"github.com/nugget/thane-ai-agent/internal/talents"
)

// runInit initializes a Thane working directory with bundled defaults.
// It creates the directory structure and writes default config, persona,
// and talent files. Existing files are never overwritten.
func runInit(w io.Writer, dir string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	fmt.Fprintf(w, "Initializing Thane workspace in %s\n", absDir)

	// Create directory structure.
	for _, sub := range []string{"db", "talents"} {
		p := filepath.Join(absDir, sub)
		if err := os.MkdirAll(p, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", sub, err)
		}
	}

	// Write config.yaml from embedded default (0600 — may contain secrets).
	if err := writeIfMissing(w, filepath.Join(absDir, "config.yaml"), defaults.ConfigYAML, 0o600); err != nil {
		return err
	}

	// Write persona.md from embedded default.
	if err := writeIfMissing(w, filepath.Join(absDir, "persona.md"), defaults.PersonaMD, 0o644); err != nil {
		return err
	}

	// Write talent files from embedded defaults.
	err = fs.WalkDir(talents.DefaultFiles, "defaults", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := filepath.Base(path)
		if !strings.HasSuffix(name, ".md") {
			return nil
		}
		data, err := talents.DefaultFiles.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", name, err)
		}
		return writeIfMissing(w, filepath.Join(absDir, "talents", name), data, 0o644)
	})
	if err != nil {
		return fmt.Errorf("deploy talents: %w", err)
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "Edit config.yaml and persona.md to customize your installation.")
	fmt.Fprintln(w, "See docs/context-layers.md for guidance on persona vs talents.")
	return nil
}

// writeIfMissing writes data to path with the given permissions if the file
// does not already exist. It prints a status line for each file indicating
// whether it was created or skipped.
func writeIfMissing(w io.Writer, path string, data []byte, mode os.FileMode) error {
	_, err := os.Stat(path)
	if err == nil {
		fmt.Fprintf(w, "  · %s (exists, skipping)\n", path)
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Fprintf(w, "  ✓ %s\n", path)
	return nil
}
