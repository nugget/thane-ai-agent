package documents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func TestToolsRootsOmitAbsolutePath(t *testing.T) {
	t.Parallel()

	tools := newDocumentToolsTestFixture(t)
	got, err := tools.Roots(context.Background())
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	var payload struct {
		Roots []map[string]any `json:"roots"`
	}
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("json.Unmarshal(Roots()) error: %v", err)
	}
	if len(payload.Roots) == 0 {
		t.Fatalf("Roots() returned no roots: %s", got)
	}
	if _, ok := payload.Roots[0]["path"]; ok {
		t.Fatalf("Roots() leaked root filesystem path: %s", got)
	}
	for _, want := range []string{`"total_size_bytes"`, `"last_modified_delta"`, `"top_directories"`, `"recent_documents"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("Roots() = %s, want field %s", got, want)
		}
	}
	for _, rawTimeField := range []string{`"last_modified_at"`, `"modified_at"`} {
		if strings.Contains(got, rawTimeField) {
			t.Fatalf("Roots() = %s, should not expose raw timestamp field %s", got, rawTimeField)
		}
	}
}

func TestDocumentToolsUseDeltaTimeFields(t *testing.T) {
	t.Parallel()

	tools := newDocumentToolsTestFixture(t)
	ctx := context.Background()

	outputs := map[string]string{}
	var err error
	outputs["roots"], err = tools.Roots(ctx)
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	outputs["browse"], err = tools.Browse(ctx, BrowseArgs{Root: "kb"})
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	outputs["search"], err = tools.Search(ctx, SearchArgs{
		Root:            "kb",
		Query:           "note",
		FrontmatterKeys: []string{"created"},
		ModifiedAfter:   "-3600s",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	outputs["read"], err = tools.Read(ctx, RefArgs{Ref: "kb:note.md"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	outputs["write"], err = tools.Write(ctx, WriteArgs{
		Ref:   "kb:written.md",
		Title: "Written",
		Body:  stringPtr("# Written\n\nBody."),
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	outputs["read_written"], err = tools.Read(ctx, RefArgs{Ref: "kb:written.md"})
	if err != nil {
		t.Fatalf("Read written: %v", err)
	}
	outputs["values_created"], err = tools.Values(ctx, ValuesArgs{Root: "kb", Key: "created"})
	if err != nil {
		t.Fatalf("Values created: %v", err)
	}

	for name, got := range outputs {
		assertModelOutputDeltaContract(t, name, got)
	}
}

func TestModelDeltaValueCountsSkipsUnparseableTimestamps(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	got := modelDeltaValueCounts([]ValueCount{
		{Value: now.Add(-time.Hour).Format(time.RFC3339), Count: 2},
		{Value: "not-a-timestamp", Count: 3},
	}, now)

	if len(got) != 1 {
		t.Fatalf("modelDeltaValueCounts() = %#v, want one parsed delta", got)
	}
	if got[0].Value != "-3600s" || got[0].Count != 2 {
		t.Fatalf("modelDeltaValueCounts()[0] = %#v, want -3600s count 2", got[0])
	}
}

func TestModelFrontmatterSkipsUnparseableTimestampValues(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	got := modelFrontmatter(map[string][]string{
		"created": {"not-a-timestamp"},
		"updated": {now.Add(-2 * time.Hour).Format(time.RFC3339)},
		"status":  {"open"},
	}, now)

	if _, ok := got["created_delta"]; ok {
		t.Fatalf("modelFrontmatter() = %#v, should omit unparseable created_delta", got)
	}
	if values := got["updated_delta"]; len(values) != 1 || values[0] != "-7200s" {
		t.Fatalf("updated_delta = %#v, want [-7200s]", values)
	}
	if values := got["status"]; len(values) != 1 || values[0] != "open" {
		t.Fatalf("status = %#v, want [open]", values)
	}
}

func TestModelFrontmatterNormalizesTimestampAliases(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	got := modelFrontmatter(map[string][]string{
		"created_at":     {now.Add(-1 * time.Hour).Format(time.RFC3339)},
		"updated_at":     {now.Add(-2 * time.Hour).Format(time.RFC3339)},
		GeneratedFieldAt: {now.Add(-3 * time.Hour).Format(time.RFC3339)},
	}, now)

	tests := map[string]string{
		"created_delta":   "-3600s",
		"updated_delta":   "-7200s",
		"generated_delta": "-10800s",
	}
	for key, want := range tests {
		if values := got[key]; len(values) != 1 || values[0] != want {
			t.Fatalf("%s = %#v, want [%s]", key, values, want)
		}
	}
}

func TestModelFrontmatterCanonicalTimestampWinsOverAlias(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	got := modelFrontmatter(map[string][]string{
		"created":    {now.Add(-1 * time.Hour).Format(time.RFC3339)},
		"created_at": {now.Add(-2 * time.Hour).Format(time.RFC3339)},
	}, now)

	if values := got["created_delta"]; len(values) != 1 || values[0] != "-3600s" {
		t.Fatalf("created_delta = %#v, want canonical created value [-3600s]", values)
	}
}

func TestFrontmatterDeltaFieldName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key  string
		want string
		ok   bool
	}{
		{key: "created", want: "created_delta", ok: true},
		{key: "created_at", want: "created_delta", ok: true},
		{key: "updated", want: "updated_delta", ok: true},
		{key: "updated_at", want: "updated_delta", ok: true},
		{key: GeneratedFieldAt, want: "generated_delta", ok: true},
		{key: "status", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got, ok := frontmatterDeltaFieldName(tt.key)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("frontmatterDeltaFieldName(%q) = %q, %v; want %q, %v", tt.key, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func assertModelOutputDeltaContract(t *testing.T, name, got string) {
	t.Helper()

	var payload any
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("%s output is not JSON: %v\n%s", name, err, got)
	}

	forbidden := map[string]struct{}{
		"modified_at":      {},
		"last_modified_at": {},
		"created_at":       {},
		"updated_at":       {},
		"checked_at":       {},
		"modified_after":   {},
		"modified_before":  {},
		"created":          {},
		"updated":          {},
		GeneratedFieldAt:   {},
	}
	if key, ok := firstForbiddenJSONKey(payload, forbidden); ok {
		t.Fatalf("%s output exposes raw timestamp key %q: %s", name, key, got)
	}
	if !hasModelDeltaSignal(payload) {
		t.Fatalf("%s output = %s, want at least one model-facing delta field", name, got)
	}
}

func firstForbiddenJSONKey(value any, forbidden map[string]struct{}) (string, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if _, ok := forbidden[key]; ok {
				return key, true
			}
			if found, ok := firstForbiddenJSONKey(child, forbidden); ok {
				return found, true
			}
		}
	case []any:
		for _, child := range typed {
			if found, ok := firstForbiddenJSONKey(child, forbidden); ok {
				return found, true
			}
		}
	}
	return "", false
}

func hasModelDeltaSignal(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if strings.HasSuffix(key, "_delta") {
				return true
			}
			if key == "key" {
				if text, ok := child.(string); ok && strings.HasSuffix(text, "_delta") {
					return true
				}
			}
			if key == "frontmatter_keys" && stringSliceHasDeltaSignal(child) {
				return true
			}
			if hasModelDeltaSignal(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasModelDeltaSignal(child) {
				return true
			}
		}
	}
	return false
}

func stringSliceHasDeltaSignal(value any) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		text, ok := item.(string)
		if ok && strings.HasSuffix(text, "_delta") {
			return true
		}
	}
	return false
}

func TestToolsSectionFailsWhenResultTooLarge(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	largeBody := strings.Repeat("Large section body.\n", 5000)
	writeFile(t, filepath.Join(kbDir, "large.md"), "# Large Document\n\n"+largeBody)

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewStore(db, map[string]string{"kb": kbDir}, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	tools := NewTools(store)
	got, err := tools.Section(context.Background(), SectionArgs{Ref: "kb:large.md"})
	if err != nil {
		t.Fatalf("Section() error = %v, want truncated preview payload", err)
	}
	if !strings.Contains(got, `"truncated": true`) || !strings.Contains(got, `"preview":`) {
		t.Fatalf("Section() = %s, want truncated preview envelope", got)
	}
}

func newDocumentToolsTestFixture(t *testing.T) *Tools {
	t.Helper()

	rootDir := t.TempDir()
	kbDir := filepath.Join(rootDir, "kb")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeFile(t, filepath.Join(kbDir, "note.md"), `---
tags: [test]
---

# Test Note

Helpful note.
`)
	writeFile(t, filepath.Join(kbDir, "network", "nested.md"), `# Nested Note

Helpful nested note.
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
	return NewTools(store)
}
