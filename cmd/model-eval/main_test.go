package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/modeleval"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
)

func TestSnapshotAndReplayWorkflow(t *testing.T) {
	t.Parallel()

	privateDir := t.TempDir()
	logsPath := filepath.Join(privateDir, "logs.db")
	seedRetainedModelCall(t, logsPath)

	snapshotPath := filepath.Join(privateDir, "production.thane-eval.json")
	if err := cmdSnapshot([]string{
		"-logs-db", logsPath,
		"-output", snapshotPath,
		"-request-id", "r_eval_workflow",
	}); err != nil {
		t.Fatal(err)
	}
	assertPrivateFile(t, snapshotPath)

	snapshot, err := loadSnapshot(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Cases) != 1 || snapshot.Cases[0].Expected.ToolCalls[0].Function.Name != "search" {
		t.Fatalf("snapshot cases = %#v", snapshot.Cases)
	}

	requestBodies := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requestBodies <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl_eval",
			"model":"candidate",
			"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{
				"id":"candidate_call","type":"function","function":{"name":"search","arguments":"{\"query\":\"weather\"}"}
			}]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":50,"completion_tokens":8}
		}`)
	}))
	defer server.Close()

	reportPath := filepath.Join(privateDir, "candidate.thane-eval-report.json")
	if err := cmdRun([]string{
		"-snapshot", snapshotPath,
		"-base-url", server.URL,
		"-model", "candidate",
		"-stream=false",
		"-tool-contract=exact",
		"-output", reportPath,
	}); err != nil {
		t.Fatal(err)
	}
	assertPrivateFile(t, reportPath)

	body := <-requestBodies
	if body["model"] != "candidate" {
		t.Fatalf("request model = %#v", body["model"])
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("request tools = %#v", body["tools"])
	}

	file, err := os.Open(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var report evalReport
	if err := json.NewDecoder(file).Decode(&report); err != nil {
		t.Fatal(err)
	}
	if report.Summary.ReferenceMatches != 1 || report.Summary.Errors != 0 {
		t.Fatalf("report summary = %#v", report.Summary)
	}
}

func seedRetainedModelCall(t *testing.T, path string) {
	t.Helper()
	db, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := logging.Migrate(db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	writer, err := logging.NewContentWriter(db, 64<<10, logger)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}

	messages := []llm.Message{
		{Role: "system", Content: "You are a tool-using assistant."},
		{Role: "user", Content: "Check the weather."},
	}
	tools := []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name":        "search",
			"description": "Search for current information.",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"query": map[string]any{"type": "string"}},
				"required":   []string{"query"},
			},
		},
	}}
	call := logging.NewModelCallContent(0, "reference", messages, tools)
	call.Complete(&llm.ChatResponse{
		Model:      "reference",
		Message:    llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{newToolCall("reference_call", "search", map[string]any{"query": "weather"})}},
		StopReason: "tool_calls",
	})
	writer.WriteRequest(context.Background(), logging.RequestContent{
		RequestID:    "r_eval_workflow",
		SystemPrompt: messages[0].Content,
		Model:        "reference",
		Messages:     messages,
		ModelCalls:   []logging.ModelCallContent{call},
	})
	if err := writer.Close(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func newToolCall(id, name string, arguments map[string]any) llm.ToolCall {
	call := llm.ToolCall{ID: id}
	call.Function.Name = name
	call.Function.Arguments = arguments
	return call
}

func assertPrivateFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("permissions = %o, want 600", got)
	}
}

func TestLoadSnapshotRejectsTrailingJSON(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "bad.thane-eval.json")
	snapshot := modeleval.Snapshot{
		SchemaVersion: modeleval.SnapshotSchemaVersion,
		Cases: []modeleval.Case{{
			ID:       "r/0",
			Messages: []llm.Message{{Role: "user", Content: "hi"}},
		}},
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte(` {}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSnapshot(path); err == nil {
		t.Fatal("snapshot with trailing JSON passed validation")
	}
}

func TestWritePrivateJSONSecuresOverwrittenFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "report.thane-eval-report.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateJSON(path, map[string]any{"private": true}, true); err != nil {
		t.Fatal(err)
	}
	assertPrivateFile(t, path)
}
