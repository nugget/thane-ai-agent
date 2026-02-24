package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/opstate"
	"github.com/nugget/thane-ai-agent/internal/paths"
)

func testContentResolver(t *testing.T) (*ContentResolver, *TempFileStore, string) {
	t.Helper()

	state, err := opstate.NewStore(filepath.Join(t.TempDir(), "opstate_test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { state.Close() })

	baseDir := filepath.Join(t.TempDir(), ".tmp")
	tfs := NewTempFileStore(baseDir, state, nil)

	kbDir := filepath.Join(t.TempDir(), "kb")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	pr := paths.New(map[string]string{"kb": kbDir})

	cr := NewContentResolver(pr, tfs, nil)
	return cr, tfs, kbDir
}

func TestContentResolver_ResolveArgs(t *testing.T) {
	t.Run("temp_label_resolved", func(t *testing.T) {
		cr, tfs, _ := testContentResolver(t)
		ctx := WithConversationID(context.Background(), "conv-1")

		_, err := tfs.Create(ctx, "conv-1", "draft", "Hello from temp file")
		if err != nil {
			t.Fatalf("Create temp: %v", err)
		}

		args := map[string]any{"body": "temp:draft"}
		if err := cr.ResolveArgs(ctx, args); err != nil {
			t.Fatalf("ResolveArgs: %v", err)
		}

		if got := args["body"].(string); got != "Hello from temp file" {
			t.Errorf("body = %q, want %q", got, "Hello from temp file")
		}
	})

	t.Run("kb_prefix_resolved", func(t *testing.T) {
		cr, _, kbDir := testContentResolver(t)
		ctx := WithConversationID(context.Background(), "conv-1")

		content := "# Knowledge Base Entry\n\nSome content here."
		if err := os.WriteFile(filepath.Join(kbDir, "notes.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		args := map[string]any{"body": "kb:notes.md"}
		if err := cr.ResolveArgs(ctx, args); err != nil {
			t.Fatalf("ResolveArgs: %v", err)
		}

		if got := args["body"].(string); got != content {
			t.Errorf("body = %q, want %q", got, content)
		}
	})

	t.Run("bare_reference_only", func(t *testing.T) {
		cr, tfs, _ := testContentResolver(t)
		ctx := WithConversationID(context.Background(), "conv-1")

		_, err := tfs.Create(ctx, "conv-1", "label", "resolved")
		if err != nil {
			t.Fatalf("Create temp: %v", err)
		}

		// Bare reference resolves.
		args := map[string]any{"body": "temp:label"}
		if err := cr.ResolveArgs(ctx, args); err != nil {
			t.Fatalf("ResolveArgs: %v", err)
		}
		if got := args["body"].(string); got != "resolved" {
			t.Errorf("bare ref: body = %q, want %q", got, "resolved")
		}

		// String with spaces does NOT resolve (not a bare reference).
		args = map[string]any{"body": "see temp:label for details"}
		if err := cr.ResolveArgs(ctx, args); err != nil {
			t.Fatalf("ResolveArgs: %v", err)
		}
		if got := args["body"].(string); got != "see temp:label for details" {
			t.Errorf("spaced ref: body = %q, want original unchanged", got)
		}
	})

	t.Run("missing_temp_label_errors", func(t *testing.T) {
		cr, _, _ := testContentResolver(t)
		ctx := WithConversationID(context.Background(), "conv-1")

		args := map[string]any{"body": "temp:nonexistent"}
		err := cr.ResolveArgs(ctx, args)
		if err == nil {
			t.Fatal("expected error for unknown temp label")
		}
		if !strings.Contains(err.Error(), "unknown temp label") {
			t.Errorf("error = %q, want it to contain 'unknown temp label'", err.Error())
		}
	})

	t.Run("missing_kb_file_passthrough", func(t *testing.T) {
		cr, _, _ := testContentResolver(t)
		ctx := WithConversationID(context.Background(), "conv-1")

		args := map[string]any{"body": "kb:does_not_exist.md"}
		if err := cr.ResolveArgs(ctx, args); err != nil {
			t.Fatalf("ResolveArgs should not error for missing kb file: %v", err)
		}

		if got := args["body"].(string); got != "kb:does_not_exist.md" {
			t.Errorf("body = %q, want original unchanged", got)
		}
	})

	t.Run("non_string_values_untouched", func(t *testing.T) {
		cr, _, _ := testContentResolver(t)
		ctx := WithConversationID(context.Background(), "conv-1")

		args := map[string]any{
			"count":   float64(42),
			"enabled": true,
		}
		if err := cr.ResolveArgs(ctx, args); err != nil {
			t.Fatalf("ResolveArgs: %v", err)
		}

		if args["count"] != float64(42) {
			t.Errorf("count changed")
		}
		if args["enabled"] != true {
			t.Errorf("enabled changed")
		}
	})

	t.Run("nil_resolver_noop", func(t *testing.T) {
		var cr *ContentResolver
		ctx := WithConversationID(context.Background(), "conv-1")

		args := map[string]any{"body": "temp:label"}
		if err := cr.ResolveArgs(ctx, args); err != nil {
			t.Fatalf("nil ResolveArgs should not error: %v", err)
		}

		if got := args["body"].(string); got != "temp:label" {
			t.Errorf("body = %q, want original unchanged", got)
		}
	})

	t.Run("multiple_args_resolved", func(t *testing.T) {
		cr, tfs, kbDir := testContentResolver(t)
		ctx := WithConversationID(context.Background(), "conv-1")

		_, err := tfs.Create(ctx, "conv-1", "msg", "temp content")
		if err != nil {
			t.Fatalf("Create temp: %v", err)
		}
		if err := os.WriteFile(filepath.Join(kbDir, "info.md"), []byte("kb content"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		args := map[string]any{
			"body":    "temp:msg",
			"message": "kb:info.md",
			"title":   "plain text",
		}
		if err := cr.ResolveArgs(ctx, args); err != nil {
			t.Fatalf("ResolveArgs: %v", err)
		}

		if got := args["body"].(string); got != "temp content" {
			t.Errorf("body = %q, want %q", got, "temp content")
		}
		if got := args["message"].(string); got != "kb content" {
			t.Errorf("message = %q, want %q", got, "kb content")
		}
		if got := args["title"].(string); got != "plain text" {
			t.Errorf("title = %q, want %q", got, "plain text")
		}
	})

	t.Run("empty_string_skipped", func(t *testing.T) {
		cr, _, _ := testContentResolver(t)
		ctx := WithConversationID(context.Background(), "conv-1")

		args := map[string]any{"body": ""}
		if err := cr.ResolveArgs(ctx, args); err != nil {
			t.Fatalf("ResolveArgs: %v", err)
		}

		if got := args["body"].(string); got != "" {
			t.Errorf("body = %q, want empty", got)
		}
	})

	t.Run("bare_temp_prefix_no_label", func(t *testing.T) {
		cr, _, _ := testContentResolver(t)
		ctx := WithConversationID(context.Background(), "conv-1")

		// "temp:" with no label should not resolve or error.
		args := map[string]any{"body": "temp:"}
		if err := cr.ResolveArgs(ctx, args); err != nil {
			t.Fatalf("ResolveArgs: %v", err)
		}

		if got := args["body"].(string); got != "temp:" {
			t.Errorf("body = %q, want original unchanged", got)
		}
	})
}

func TestContentResolver_Execute_Integration(t *testing.T) {
	t.Run("tool_receives_resolved_content", func(t *testing.T) {
		cr, tfs, _ := testContentResolver(t)
		ctx := WithConversationID(context.Background(), "conv-1")

		_, err := tfs.Create(ctx, "conv-1", "body_text", "Full issue body content here")
		if err != nil {
			t.Fatalf("Create temp: %v", err)
		}

		var receivedBody string
		reg := NewEmptyRegistry()
		reg.SetContentResolver(cr)
		reg.Register(&Tool{
			Name:        "test_tool",
			Description: "test",
			Parameters:  map[string]any{"type": "object"},
			Handler: func(_ context.Context, args map[string]any) (string, error) {
				receivedBody = args["body"].(string)
				return "ok", nil
			},
		})

		_, err = reg.Execute(ctx, "test_tool", `{"body":"temp:body_text"}`)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if receivedBody != "Full issue body content here" {
			t.Errorf("handler got body = %q, want resolved content", receivedBody)
		}
	})

	t.Run("skip_content_resolve_preserves_prefix", func(t *testing.T) {
		cr, tfs, _ := testContentResolver(t)
		ctx := WithConversationID(context.Background(), "conv-1")

		_, err := tfs.Create(ctx, "conv-1", "body_text", "Should not see this")
		if err != nil {
			t.Fatalf("Create temp: %v", err)
		}

		var receivedBody string
		reg := NewEmptyRegistry()
		reg.SetContentResolver(cr)
		reg.Register(&Tool{
			Name:               "file_like_tool",
			Description:        "test",
			SkipContentResolve: true,
			Parameters:         map[string]any{"type": "object"},
			Handler: func(_ context.Context, args map[string]any) (string, error) {
				receivedBody = args["body"].(string)
				return "ok", nil
			},
		})

		_, err = reg.Execute(ctx, "file_like_tool", `{"body":"temp:body_text"}`)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if receivedBody != "temp:body_text" {
			t.Errorf("handler got body = %q, want original prefix string", receivedBody)
		}
	})

	t.Run("unknown_temp_label_errors_through_execute", func(t *testing.T) {
		cr, _, _ := testContentResolver(t)
		ctx := WithConversationID(context.Background(), "conv-1")

		reg := NewEmptyRegistry()
		reg.SetContentResolver(cr)
		reg.Register(&Tool{
			Name:        "test_tool",
			Description: "test",
			Parameters:  map[string]any{"type": "object"},
			Handler: func(_ context.Context, _ map[string]any) (string, error) {
				t.Fatal("handler should not be called when content resolution fails")
				return "", nil
			},
		})

		_, err := reg.Execute(ctx, "test_tool", `{"body":"temp:typo_label"}`)
		if err == nil {
			t.Fatal("expected error for unknown temp label")
		}
		if !strings.Contains(err.Error(), "unknown temp label") {
			t.Errorf("error = %q, want it to contain 'unknown temp label'", err.Error())
		}
		if !strings.Contains(err.Error(), "test_tool") {
			t.Errorf("error = %q, want it to contain tool name 'test_tool'", err.Error())
		}
	})
}

func TestContentResolver_FilterPropagation(t *testing.T) {
	cr, _, _ := testContentResolver(t)

	reg := NewEmptyRegistry()
	reg.SetContentResolver(cr)
	reg.Register(&Tool{
		Name:        "tool_a",
		Description: "a",
		Parameters:  map[string]any{"type": "object"},
		Handler:     func(_ context.Context, _ map[string]any) (string, error) { return "", nil },
	})
	reg.Register(&Tool{
		Name:        "tool_b",
		Description: "b",
		Parameters:  map[string]any{"type": "object"},
		Handler:     func(_ context.Context, _ map[string]any) (string, error) { return "", nil },
	})

	t.Run("FilteredCopy", func(t *testing.T) {
		filtered := reg.FilteredCopy([]string{"tool_a"})
		if filtered.contentResolver == nil {
			t.Error("FilteredCopy did not propagate contentResolver")
		}
	})

	t.Run("FilteredCopyExcluding", func(t *testing.T) {
		filtered := reg.FilteredCopyExcluding([]string{"tool_b"})
		if filtered.contentResolver == nil {
			t.Error("FilteredCopyExcluding did not propagate contentResolver")
		}
	})

	t.Run("FilterByTags", func(t *testing.T) {
		// No tags set — returns full copy.
		filtered := reg.FilterByTags(nil)
		if filtered.contentResolver == nil {
			t.Error("FilterByTags (nil tags) did not propagate contentResolver")
		}

		// With tag index.
		reg.SetTagIndex(map[string][]string{"alpha": {"tool_a"}})
		filtered = reg.FilterByTags([]string{"alpha"})
		if filtered.contentResolver == nil {
			t.Error("FilterByTags (with tags) did not propagate contentResolver")
		}
	})
}

func TestNewContentResolver_NilDeps(t *testing.T) {
	cr := NewContentResolver(nil, nil, nil)
	if cr != nil {
		t.Error("NewContentResolver(nil, nil, nil) should return nil")
	}
}
