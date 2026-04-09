package documents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/database"
)

func TestDocumentStoreBrowseSearchAndSections(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	scratchDir := filepath.Join(rootDir, "scratchpad")
	for _, dir := range []string{
		filepath.Join(kbDir, "network", "unifi"),
		filepath.Join(kbDir, "notes"),
		scratchDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
	}

	writeFile(t, filepath.Join(kbDir, "network", "vlans.md"), `---
title: VLAN Guide
tags: [network, vlans]
status: active
---

# VLAN Guide

Reference for the home network VLAN layout.

## Trusted

Primary trusted LAN notes.

## IoT

The isolated IoT section.
`)
	writeFile(t, filepath.Join(kbDir, "network", "unifi", "switches.md"), `---
tags: [network, unifi]
area: rack
---

# Switch Inventory

Rack switch notes.

## Core Switch

Details about the core switch.
`)
	writeFile(t, filepath.Join(kbDir, "notes", "cameras.md"), `# Camera Notes

Driveway camera notes and maintenance history.
`)
	writeFile(t, filepath.Join(scratchDir, "ideas.md"), `---
tags: [draft]
---

# Loop Ideas

Scratch thoughts about future loops.
`)

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewStore(db, map[string]string{
		"kb":         kbDir,
		"scratchpad": scratchDir,
	}, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	ctx := context.Background()

	roots, err := store.Roots(ctx)
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	if len(roots) != 2 {
		t.Fatalf("len(roots) = %d, want 2", len(roots))
	}
	if roots[0].Root != "kb" || roots[0].DocumentCount != 3 {
		t.Fatalf("roots[0] = %#v, want kb with 3 docs", roots[0])
	}

	browse, err := store.Browse(ctx, "kb", "network", 20)
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	if len(browse.Directories) != 1 || browse.Directories[0].PathPrefix != "network/unifi" {
		t.Fatalf("browse.Directories = %#v, want network/unifi", browse.Directories)
	}
	if len(browse.Documents) != 1 || browse.Documents[0].Ref != "kb:network/vlans.md" {
		t.Fatalf("browse.Documents = %#v, want kb:network/vlans.md", browse.Documents)
	}

	results, err := store.Search(ctx, SearchQuery{
		Root:  "kb",
		Query: "switch",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search(query): %v", err)
	}
	if len(results) != 1 || results[0].Ref != "kb:network/unifi/switches.md" {
		t.Fatalf("search(query) = %#v, want switches doc", results)
	}

	tagged, err := store.Search(ctx, SearchQuery{
		Root:  "kb",
		Tags:  []string{"network"},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search(tags): %v", err)
	}
	if len(tagged) != 2 {
		t.Fatalf("len(search(tags)) = %d, want 2", len(tagged))
	}

	outline, err := store.Outline(ctx, "kb:network/vlans.md")
	if err != nil {
		t.Fatalf("Outline: %v", err)
	}
	if len(outline) != 3 {
		t.Fatalf("len(outline) = %d, want 3", len(outline))
	}
	if outline[1].Slug != "trusted" || outline[2].Slug != "iot" {
		t.Fatalf("outline = %#v, want trusted/iot slugs", outline)
	}

	section, err := store.Section(ctx, "kb:network/vlans.md", "iot")
	if err != nil {
		t.Fatalf("Section: %v", err)
	}
	if section.Heading != "IoT" || section.Level != 2 {
		t.Fatalf("section = %#v, want IoT level 2", section)
	}
	if want := "The isolated IoT section."; !strings.Contains(section.Content, want) {
		t.Fatalf("section.Content = %q, want substring %q", section.Content, want)
	}

	values, err := store.Values(ctx, "kb", "tags", 10)
	if err != nil {
		t.Fatalf("Values(tags): %v", err)
	}
	if len(values) == 0 || values[0].Value != "network" || values[0].Count != 2 {
		t.Fatalf("values = %#v, want network count 2 first", values)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}
