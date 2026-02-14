package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nugget/thane-ai-agent/internal/defaults"
	"github.com/nugget/thane-ai-agent/internal/talents"
)

// runInit initializes a Thane working directory with default files.
// It creates the directory structure and copies bundled defaults for
// config, persona, and talents. Existing files are never overwritten.
func runInit(w io.Writer, dir string) error {
	fmt.Fprintf(w, "Initializing Thane workspace in %s\n", dir)

	// Create the base directory and subdirectories.
	for _, sub := range []string{"db", "talents"} {
		path := filepath.Join(dir, sub)
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
	}

	// Write config example if no config exists.
	configPath := filepath.Join(dir, "config.yaml")
	if err := writeIfMissing(configPath, defaults.ConfigYAML); err != nil {
		return err
	}
	fmt.Fprintf(w, "  ✓ %s\n", configPath)

	// Write persona example if no persona exists.
	personaPath := filepath.Join(dir, "persona.md")
	if err := writeIfMissing(personaPath, defaults.PersonaMD); err != nil {
		return err
	}
	fmt.Fprintf(w, "  ✓ %s\n", personaPath)

	// Copy bundled talent files.
	err := fs.WalkDir(talents.DefaultFiles, "defaults", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}

		destPath := filepath.Join(dir, "talents", d.Name())

		content, err := talents.DefaultFiles.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", path, err)
		}

		if err := writeIfMissing(destPath, content); err != nil {
			return err
		}
		fmt.Fprintf(w, "  ✓ %s\n", destPath)
		return nil
	})
	if err != nil {
		return fmt.Errorf("install talents: %w", err)
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "Edit config.yaml and persona.md to customize your installation.")
	fmt.Fprintln(w, "See docs/context-layers.md for guidance on persona vs talents.")
	return nil
}

// writeIfMissing writes content to path only if the file does not already
// exist. This ensures init never overwrites user customizations.
func writeIfMissing(path string, content []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil // already exists, skip
	}
	return os.WriteFile(path, content, 0o644)
}
