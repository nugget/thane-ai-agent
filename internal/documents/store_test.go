package documents

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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
	writeFile(t, filepath.Join(kbDir, "network", "routers.md"), `# Router Notes

Operational notes about the edge router.
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
	writeFile(t, filepath.Join(kbDir, "network", "vpn", "firewall.md"), `# VPN Firewall

Notes about the VPN firewall appliance.
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
	if roots[0].Root != "kb" || roots[0].DocumentCount != 5 {
		t.Fatalf("roots[0] = %#v, want kb with 5 docs", roots[0])
	}
	if roots[0].TotalWordCount <= 0 || roots[0].TotalSizeBytes <= 0 {
		t.Fatalf("roots[0] aggregate stats = %#v, want non-zero totals", roots[0])
	}
	if roots[0].LastModifiedAt == "" {
		t.Fatalf("roots[0].LastModifiedAt = empty, want timestamp")
	}
	if len(roots[0].TopDirectories) != 2 || roots[0].TopDirectories[0].PathPrefix != "network" {
		t.Fatalf("roots[0].TopDirectories = %#v, want network/notes summary", roots[0].TopDirectories)
	}
	if len(roots[0].RecentDocuments) == 0 || !strings.HasPrefix(roots[0].RecentDocuments[0].Ref, "kb:") {
		t.Fatalf("roots[0].RecentDocuments = %#v, want canonical root refs", roots[0].RecentDocuments)
	}

	browse, err := store.Browse(ctx, "kb", "network", 20)
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	if len(browse.Directories) != 2 || browse.Directories[0].PathPrefix != "network/unifi" || browse.Directories[1].PathPrefix != "network/vpn" {
		t.Fatalf("browse.Directories = %#v, want network/unifi and network/vpn", browse.Directories)
	}
	if len(browse.Documents) != 2 || browse.Documents[0].Ref != "kb:network/routers.md" || browse.Documents[1].Ref != "kb:network/vlans.md" {
		t.Fatalf("browse.Documents = %#v, want network/routers.md and network/vlans.md", browse.Documents)
	}

	browseLimited, err := store.Browse(ctx, "kb", "network", 2)
	if err != nil {
		t.Fatalf("Browse(limit): %v", err)
	}
	if got := len(browseLimited.Directories) + len(browseLimited.Documents); got != 2 {
		t.Fatalf("combined browse limit = %d, want 2 (dirs=%d docs=%d)", got, len(browseLimited.Directories), len(browseLimited.Documents))
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

func TestDocumentStoreSkipsSymlinkedMarkdownOutsideRoot(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior varies on Windows")
	}

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	outsideDir := filepath.Join(rootDir, "outside")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(kb): %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(outside): %v", err)
	}

	writeFile(t, filepath.Join(kbDir, "inside.md"), "# Inside\n\nSafe document.\n")
	outsidePath := filepath.Join(outsideDir, "secret.md")
	writeFile(t, outsidePath, "# Secret\n\nDo not index me.\n")
	if err := os.Symlink(outsidePath, filepath.Join(kbDir, "escaped.md")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewStore(db, map[string]string{"kb": kbDir}, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	results, err := store.Search(context.Background(), SearchQuery{
		Root:  "kb",
		Query: "secret",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("Search() indexed symlinked outside document: %#v", results)
	}

	roots, err := store.Roots(context.Background())
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	if len(roots) != 1 || roots[0].DocumentCount != 1 {
		t.Fatalf("Roots() = %#v, want exactly 1 in-root document", roots)
	}
}

func TestResolveDocumentPathRejectsEscapes(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior varies on Windows")
	}

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	outsideDir := filepath.Join(rootDir, "outside")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(kb): %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(outside): %v", err)
	}

	outsidePath := filepath.Join(outsideDir, "secret.md")
	writeFile(t, outsidePath, "# Secret\n")
	if err := os.Symlink(outsidePath, filepath.Join(kbDir, "escaped.md")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewStore(db, map[string]string{"kb": kbDir}, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if _, err := store.resolveDocumentPath("kb", "../secret.md"); err == nil || !strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("resolveDocumentPath(../secret.md) error = %v, want escape rejection", err)
	}
	if _, err := store.resolveDocumentPath("kb", "escaped.md"); err == nil || !strings.Contains(err.Error(), "outside root") {
		t.Fatalf("resolveDocumentPath(escaped.md) error = %v, want outside-root rejection", err)
	}
}

func TestParseRefRejectsPathEscape(t *testing.T) {
	t.Parallel()

	if _, _, err := parseRef("kb:../secret.md"); err == nil || !strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("parseRef(path escape) error = %v, want escape rejection", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}
