package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalValidConfig returns the smallest config.yaml body that parses
// and survives semantic validation. Tests use it as a known-good base
// to layer breakages on top of.
const minimalValidConfig = `
listen:
  port: 8080
models:
  default: test-model
  available:
    - name: test-model
      provider: ollama
      supports_tools: true
      context_window: 4096
      speed: 5
      quality: 5
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestRunValidate_HappyPath(t *testing.T) {
	path := writeConfig(t, minimalValidConfig)
	var buf bytes.Buffer

	if err := runValidate(&buf, path, "text"); err != nil {
		t.Fatalf("runValidate: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "✓ Config valid") {
		t.Errorf("expected success marker, got:\n%s", out)
	}
	if !strings.Contains(out, path) {
		t.Errorf("expected output to mention config path, got:\n%s", out)
	}
	if !strings.Contains(out, "Default model:") {
		t.Errorf("expected summary section, got:\n%s", out)
	}
}

func TestRunValidate_MissingFile(t *testing.T) {
	var buf bytes.Buffer
	err := runValidate(&buf, "/nonexistent/path/config.yaml", "text")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	// loadConfig is the call that fails; the error path should mention
	// what was tried so the operator knows what to fix.
	if buf.Len() != 0 {
		t.Errorf("expected no stdout on failure (text mode), got:\n%s", buf.String())
	}
}

func TestRunValidate_ParseError(t *testing.T) {
	path := writeConfig(t, "models:\n  default: [this isn't a string\n")
	var buf bytes.Buffer

	err := runValidate(&buf, path, "text")
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

// TestRunValidate_SemanticError exercises the cross-key validation hook
// that catches dangling references between channel_tags and the union
// of canonical + operator-defined capability tags. The same hook is
// what caught the signal_channel orphan in the v0.9.3 deploy draft.
func TestRunValidate_SemanticError(t *testing.T) {
	body := minimalValidConfig + `
channel_tags:
  signal:
    - definitely_not_a_real_tag
`
	path := writeConfig(t, body)
	var buf bytes.Buffer

	err := runValidate(&buf, path, "text")
	if err == nil {
		t.Fatal("expected semantic error for undefined tag reference, got nil")
	}
	if !strings.Contains(err.Error(), "definitely_not_a_real_tag") {
		t.Errorf("error should mention the offending tag name, got: %v", err)
	}
}

func TestRunValidate_JSONHappyPath(t *testing.T) {
	path := writeConfig(t, minimalValidConfig)
	var buf bytes.Buffer

	if err := runValidate(&buf, path, "json"); err != nil {
		t.Fatalf("runValidate: %v", err)
	}

	var got struct {
		Path    string         `json:"path"`
		Valid   bool           `json:"valid"`
		Error   string         `json:"error"`
		Summary map[string]any `json:"summary"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json output is not valid JSON: %v\nbody:\n%s", err, buf.String())
	}
	if !got.Valid {
		t.Errorf("expected valid=true, got false (error=%q)", got.Error)
	}
	if got.Path != path {
		t.Errorf("path = %q, want %q", got.Path, path)
	}
	if got.Summary == nil {
		t.Error("expected summary on success")
	}
	if model, _ := got.Summary["default_model"].(string); model != "test-model" {
		t.Errorf("default_model in summary = %q, want %q", model, "test-model")
	}
}

func TestRunValidate_JSONFailure(t *testing.T) {
	body := minimalValidConfig + `
channel_tags:
  signal:
    - definitely_not_a_real_tag
`
	path := writeConfig(t, body)
	var buf bytes.Buffer

	err := runValidate(&buf, path, "json")
	if err == nil {
		t.Fatal("expected error from runValidate, got nil")
	}

	// JSON should be emitted to the writer even on failure — the
	// structured report is the whole point of -o json.
	var got struct {
		Valid bool   `json:"valid"`
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal(buf.Bytes(), &got); jerr != nil {
		t.Fatalf("json output is not valid JSON: %v\nbody:\n%s", jerr, buf.String())
	}
	if got.Valid {
		t.Error("expected valid=false on semantic error")
	}
	if !strings.Contains(got.Error, "definitely_not_a_real_tag") {
		t.Errorf("json error field should name the offending tag, got: %q", got.Error)
	}
}
