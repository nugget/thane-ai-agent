package documents

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
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

See [[VLAN Guide]] and [vendor docs](https://example.com/switches).

## Core Switch

Details about the core switch.
`)
	writeFile(t, filepath.Join(kbDir, "network", "vpn", "firewall.md"), `# VPN Firewall

Notes about the VPN firewall appliance.
`)
	writeFile(t, filepath.Join(kbDir, "notes", "cameras.md"), `# Camera Notes

Driveway camera notes and maintenance history.

See the [trusted VLAN notes](../network/vlans.md#trusted).
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
	now := time.Now().UTC()
	setFileModTime(t, filepath.Join(kbDir, "network", "vlans.md"), now.Add(-30*time.Minute))
	setFileModTime(t, filepath.Join(kbDir, "network", "routers.md"), now.Add(-72*time.Hour))
	setFileModTime(t, filepath.Join(kbDir, "network", "unifi", "switches.md"), now.Add(-48*time.Hour))
	setFileModTime(t, filepath.Join(kbDir, "network", "vpn", "firewall.md"), now.Add(-96*time.Hour))
	setFileModTime(t, filepath.Join(kbDir, "notes", "cameras.md"), now.Add(-2*time.Hour))

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

	filtered, err := store.Search(ctx, SearchQuery{
		Root: "kb",
		Frontmatter: map[string][]string{
			"status": {"active"},
		},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search(frontmatter): %v", err)
	}
	if len(filtered) != 1 || filtered[0].Ref != "kb:network/vlans.md" {
		t.Fatalf("search(frontmatter) = %#v, want vlans doc", filtered)
	}

	keysOnly, err := store.Search(ctx, SearchQuery{
		Root:            "kb",
		FrontmatterKeys: []string{"area"},
		Limit:           10,
	})
	if err != nil {
		t.Fatalf("Search(frontmatter_keys): %v", err)
	}
	if len(keysOnly) != 1 || keysOnly[0].Ref != "kb:network/unifi/switches.md" {
		t.Fatalf("search(frontmatter_keys) = %#v, want switches doc", keysOnly)
	}

	modifiedAfter := now.Add(-3 * time.Hour)
	recent, err := store.Search(ctx, SearchQuery{
		Root:          "kb",
		ModifiedAfter: &modifiedAfter,
		Limit:         10,
	})
	if err != nil {
		t.Fatalf("Search(modified_after): %v", err)
	}
	if len(recent) != 2 || recent[0].Ref != "kb:network/vlans.md" || recent[1].Ref != "kb:notes/cameras.md" {
		t.Fatalf("search(modified_after) = %#v, want vlans then cameras", recent)
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

	links, err := store.Links(ctx, "kb:network/vlans.md", "both", 0, 0)
	if err != nil {
		t.Fatalf("Links: %v", err)
	}
	if len(links.Backlinks) != 2 {
		t.Fatalf("len(links.Backlinks) = %d, want 2", len(links.Backlinks))
	}
	if links.Backlinks[0].Ref != "kb:notes/cameras.md" || links.Backlinks[1].Ref != "kb:network/unifi/switches.md" {
		t.Fatalf("links.Backlinks = %#v, want cameras and switches backlinks", links.Backlinks)
	}

	outgoing, err := store.Links(ctx, "kb:notes/cameras.md", "outgoing", 0, 0)
	if err != nil {
		t.Fatalf("Links(outgoing): %v", err)
	}
	if len(outgoing.Outgoing) != 1 {
		t.Fatalf("len(outgoing.Outgoing) = %d, want 1", len(outgoing.Outgoing))
	}
	if outgoing.Outgoing[0].Ref != "kb:network/vlans.md" || outgoing.Outgoing[0].Kind != "section" || outgoing.Outgoing[0].Anchor != "trusted" {
		t.Fatalf("outgoing.Outgoing[0] = %#v, want trusted VLAN section link", outgoing.Outgoing[0])
	}
}

func TestDocumentStoreLinksCanonicalizeRefsAndFailFastOnCorruptIndexData(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	for _, dir := range []string{
		filepath.Join(kbDir, "network", "unifi"),
		filepath.Join(kbDir, "notes"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
	}

	writeFile(t, filepath.Join(kbDir, "network", "vlans.md"), `# VLAN Guide

Reference for the home network VLAN layout.
`)
	writeFile(t, filepath.Join(kbDir, "network", "unifi", "switches.md"), `# Switch Inventory

See [[VLAN Guide]].
`)
	writeFile(t, filepath.Join(kbDir, "notes", "cameras.md"), `# Camera Notes

See the [trusted VLAN notes](../network/vlans.md#trusted).
`)

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewStore(db, map[string]string{"kb": kbDir}, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	ctx := context.Background()
	outgoing, err := store.Links(ctx, "  kb:notes\\cameras.md  ", "outgoing", 0, 0)
	if err != nil {
		t.Fatalf("Links(canonicalize): %v", err)
	}
	if outgoing.Ref != "kb:notes/cameras.md" {
		t.Fatalf("Links.Ref = %q, want canonical kb:notes/cameras.md", outgoing.Ref)
	}

	store.refreshInterval = time.Hour
	if _, err := store.db.ExecContext(ctx,
		`UPDATE indexed_documents SET links_json = ? WHERE root = ? AND rel_path = ?`,
		"{bad-json",
		"kb",
		"network/unifi/switches.md",
	); err != nil {
		t.Fatalf("corrupt links_json: %v", err)
	}

	outgoing, err = store.Links(ctx, "kb:notes/cameras.md", "outgoing", 0, 0)
	if err != nil {
		t.Fatalf("Links(outgoing with unrelated corruption): %v", err)
	}
	if len(outgoing.Outgoing) != 1 || outgoing.Outgoing[0].Ref != "kb:network/vlans.md" {
		t.Fatalf("outgoing = %#v, want resolved VLAN link", outgoing.Outgoing)
	}

	if _, err := store.Links(ctx, "kb:network/vlans.md", "backlinks", 0, 0); err == nil || !strings.Contains(err.Error(), "unmarshal document links for kb/network/unifi/switches.md") {
		t.Fatalf("Links(backlinks) err = %v, want corrupt links_json error", err)
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

func setFileModTime(t *testing.T, path string, mod time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatalf("Chtimes(%q): %v", path, err)
	}
}

func TestParseRefRejectsPathEscape(t *testing.T) {
	t.Parallel()

	if _, _, err := parseRef("kb:../secret.md"); err == nil || !strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("parseRef(path escape) error = %v, want escape rejection", err)
	}
}

func TestParseRefNormalizesBackslashes(t *testing.T) {
	t.Parallel()

	root, relPath, err := parseRef(`kb:notes\camera.md`)
	if err != nil {
		t.Fatalf("parseRef(backslashes): %v", err)
	}
	if root != "kb" || relPath != "notes/camera.md" {
		t.Fatalf("parseRef(backslashes) = %q %q, want kb notes/camera.md", root, relPath)
	}
}

func TestSectionReturnsDocumentNotFoundForStaleIndexedFile(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(kb): %v", err)
	}

	docPath := filepath.Join(kbDir, "stale.md")
	writeFile(t, docPath, "# Stale\n\nSoon to be deleted.\n")

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewStore(db, map[string]string{"kb": kbDir}, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	store.refreshInterval = time.Hour

	if _, err := store.Outline(context.Background(), "kb:stale.md"); err != nil {
		t.Fatalf("Outline before delete: %v", err)
	}
	if err := os.Remove(docPath); err != nil {
		t.Fatalf("Remove(%q): %v", docPath, err)
	}

	if _, err := store.Section(context.Background(), "kb:stale.md", ""); err == nil || !strings.Contains(err.Error(), "document not found") {
		t.Fatalf("Section(stale doc) error = %v, want document not found", err)
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
