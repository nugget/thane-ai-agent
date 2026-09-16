package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/app"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
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

	if err := runValidate(&buf, path, "", "text"); err != nil {
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
	err := runValidate(&buf, "/nonexistent/path/config.yaml", "", "text")
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

	err := runValidate(&buf, path, "", "text")
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

	err := runValidate(&buf, path, "", "text")
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

	if err := runValidate(&buf, path, "", "json"); err != nil {
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

// TestRunValidate_JSONDiscoveryFailure guards the stable-schema
// promise: even when config discovery fails before a path is
// resolved, the JSON report still includes the path field (populated
// from the operator's explicit -insecure-config value) so scripts piping into
// jq don't have to handle two schema shapes.
func TestRunValidate_JSONDiscoveryFailure(t *testing.T) {
	const explicit = "/nonexistent/never-going-to-exist.yaml"
	var buf bytes.Buffer

	err := runValidate(&buf, explicit, "", "json")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}

	var got map[string]any
	if jerr := json.Unmarshal(buf.Bytes(), &got); jerr != nil {
		t.Fatalf("json output is not valid JSON: %v\nbody:\n%s", jerr, buf.String())
	}
	pathField, present := got["path"]
	if !present {
		t.Errorf("path field must be present in JSON output even on discovery failure; got keys: %v", keysOf(got))
	}
	if got, _ := pathField.(string); got != explicit {
		t.Errorf("path = %q, want %q (operator's -insecure-config value)", got, explicit)
	}
	if v, _ := got["valid"].(bool); v {
		t.Error("valid should be false on discovery failure")
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestRunValidate_JSONFailure(t *testing.T) {
	body := minimalValidConfig + `
channel_tags:
  signal:
    - definitely_not_a_real_tag
`
	path := writeConfig(t, body)
	var buf bytes.Buffer

	err := runValidate(&buf, path, "", "json")
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

func TestRunValidate_IntegrityReportSkippedForUnrelatedExplicitConfig(t *testing.T) {
	// An explicit -config outside any core names a file, not an
	// instance. Reporting on the default workspace would answer a
	// question the operator did not ask.
	path := writeConfig(t, "listen:\n  port: 8080\n")
	var buf bytes.Buffer

	if err := runValidate(&buf, path, "", "text"); err != nil {
		t.Fatalf("runValidate: %v", err)
	}
	if strings.Contains(buf.String(), "Core integrity") {
		t.Fatalf("integrity report should not appear for an unrelated explicit config:\n%s", buf.String())
	}
}

func TestRunValidate_IntegrityReportForWorkspaceInstance(t *testing.T) {
	workspace := t.TempDir()
	coreDir := filepath.Join(workspace, "core")
	if err := os.MkdirAll(coreDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(coreDir, "config.yaml"), []byte("listen:\n  port: 8080\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	var buf bytes.Buffer

	// core is not a git repository here, so validate must fail the same
	// way serve would refuse — otherwise `validate && serve` certifies
	// an instance that is about to be rejected.
	err := runValidate(&buf, "", workspace, "text")
	if err == nil {
		t.Fatal("validate should fail when core integrity fails")
	}
	if exitCodeFor(err) != ExitTerminal {
		t.Fatalf("integrity failure should exit %d, got %d", ExitTerminal, exitCodeFor(err))
	}
	out := buf.String()
	if !strings.Contains(out, "Core integrity") {
		t.Fatalf("a workspace instance should get an integrity report:\n%s", out)
	}
	// core is not a git repository here, so the report must say so and
	// give the command that fixes it.
	if !strings.Contains(out, "core_repository") || !strings.Contains(out, "git -C") {
		t.Fatalf("report should name the failing check and its fix:\n%s", out)
	}
}

// TestAdmissionReporting covers the mapping from admission outcomes to what
// an operator sees and what the exit code says. The decision itself is tested
// against the real boot path in internal/app; what matters here is that a
// required failure stops `validate && serve` while a warn failure does not,
// since that is exactly how serve treats them.
func TestAdmissionReporting(t *testing.T) {
	refused := errors.New("root commit is not signed by a declared seed signer")

	tests := []struct {
		name      string
		results   []app.RootAdmission
		wantErr   bool
		wantLines []string
	}{
		{
			name: "all admitted",
			results: []app.RootAdmission{
				{Root: "projects", Mode: documents.VerificationRequired, Applicable: true},
				{Root: "kb", Mode: documents.VerificationRequired, Applicable: true},
			},
			wantLines: []string{"✓ Root admission: kb, projects"},
		},
		{
			name: "required failure is fatal",
			results: []app.RootAdmission{
				{Root: "kb", Mode: documents.VerificationRequired, Applicable: true, Err: refused},
				{Root: "projects", Mode: documents.VerificationRequired, Applicable: true},
			},
			wantErr:   true,
			wantLines: []string{"✗ Root admission", "✗ kb (required)", "✓ projects", refused.Error()},
		},
		{
			name: "warn failure reports without refusing",
			results: []app.RootAdmission{
				{Root: "kb", Mode: documents.VerificationWarn, Applicable: true, Err: refused},
			},
			wantLines: []string{"! kb (warn)", refused.Error()},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := admissionError(tc.results)
			if (err != nil) != tc.wantErr {
				t.Fatalf("admissionError = %v, want error: %v", err, tc.wantErr)
			}
			if err != nil && exitCodeFor(err) != ExitTerminal {
				t.Fatalf("admission failure should exit %d, got %d", ExitTerminal, exitCodeFor(err))
			}

			var buf bytes.Buffer
			writeAdmissionText(&buf, tc.results)
			out := buf.String()
			for _, want := range tc.wantLines {
				if !strings.Contains(out, want) {
					t.Fatalf("report missing %q:\n%s", want, out)
				}
			}
		})
	}
}

// TestCoreLoopReporting covers the mapping from what a definition
// document did to what an operator sees and what the exit code says.
// The parsing itself is tested against the real loader in internal/app;
// what matters here is that a document which will not load stops
// `validate && serve` while a document that merely hangs in the wrong
// place does not — since that is exactly how serve treats them.
func TestCoreLoopReporting(t *testing.T) {
	broken := errors.New("metacognitive.md: field not_a_field not found in type loop.Spec")

	tests := []struct {
		name        string
		definitions []app.CoreLoopDefinition
		wantErr     bool
		wantOut     []string
	}{
		{
			name: "loadable documents report their generated tools",
			definitions: []app.CoreLoopDefinition{{
				File:       "metacognitive.md",
				Name:       "metacognitive",
				ParentName: "cognition",
				Tools:      []string{"publish_output_metacognitive_state"},
			}},
			wantOut: []string{"✓ Core loop definitions: 1", "metacognitive → cognition", "publish_output_metacognitive_state"},
		},
		{
			name: "a document that will not load fails the guard",
			definitions: []app.CoreLoopDefinition{{
				File:  "metacognitive.md",
				Err:   broken,
				Error: broken.Error(),
			}},
			wantErr: true,
			wantOut: []string{"✗ Core loop definitions: 1 of 1 failed", "not_a_field"},
		},
		{
			name: "a warning is printed but certifies",
			definitions: []app.CoreLoopDefinition{{
				File:     "metacognitive.md",
				Name:     "metacognitive",
				Warnings: []string{"declares no parent_name, so this core service loop will hang at the graph root"},
			}},
			wantOut: []string{"metacognitive → (root)", "! declares no parent_name"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			writeCoreLoopText(&buf, tt.definitions)
			err := coreLoopError(tt.definitions)

			switch {
			case tt.wantErr && err == nil:
				t.Fatal("a document that will not load must fail validate; serve refuses over it")
			case tt.wantErr && exitCodeFor(err) != ExitTerminal:
				t.Fatalf("exit code = %d, want %d — editing the document is the only fix", exitCodeFor(err), ExitTerminal)
			case !tt.wantErr && err != nil:
				t.Fatalf("coreLoopError = %v, want nil", err)
			}
			for _, want := range tt.wantOut {
				if !strings.Contains(buf.String(), want) {
					t.Errorf("output missing %q:\n%s", want, buf.String())
				}
			}
		})
	}
}

// TestRunValidate_ReportsCoreLoopDefinitions is the end-to-end half:
// a definition document in the workspace's core root reaches the report
// without the operator naming it. This is the check that was missing —
// before it, a malformed document was discovered by a boot on the host
// it was installed to.
func TestRunValidate_ReportsCoreLoopDefinitions(t *testing.T) {
	workspace := t.TempDir()
	loopsDir := filepath.Join(workspace, "core", "loops")
	if err := os.MkdirAll(loopsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "core", "config.yaml"), []byte(minimalValidConfig), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	document := "# Ranch\n\n## Spec\n\n```yaml\nname: ranch_watch\nenabled: true\noperation: service\nsleep_min: 15m\nsleep_max: 12h\nsleep_default: 1h\njitter: 0.2\n```\n\n## Task\n\nWatch.\n"
	if err := os.WriteFile(filepath.Join(loopsDir, "ranch.md"), []byte(document), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var buf bytes.Buffer
	// core is not a git repository here, so integrity fails and validate
	// returns that error first. The loop report is still printed, which
	// is what this asserts: sections report independently.
	_ = runValidate(&buf, "", workspace, "text")

	out := buf.String()
	if !strings.Contains(out, "Core loop definitions") {
		t.Fatalf("a workspace instance with core/loops should get a definition report:\n%s", out)
	}
	if !strings.Contains(out, "ranch_watch") {
		t.Fatalf("report should name the loop the document defines:\n%s", out)
	}
}

// TestIndentBlockPreservesInteriorStructure guards against re-trimming.
// A yaml decode error indents its detail lines under the message they
// belong to; flattening them discards structure the error author put
// there, which is the same mistake as trimming porcelain columns.
func TestIndentBlockPreservesInteriorStructure(t *testing.T) {
	got := indentBlock("yaml: unmarshal errors:\n  line 40: field sleep_mim not found\n", "      ")
	want := "      yaml: unmarshal errors:\n        line 40: field sleep_mim not found"
	if got != want {
		t.Errorf("indentBlock() =\n%q\nwant\n%q", got, want)
	}
}

// A reason wrapping a git failure carries git's stderr, which is often an
// error line followed by usage advice. Printed raw, those continuation lines
// start at column zero and the report loses the indentation that tells a reader
// which root each reason belongs to — precisely when they are scanning it to
// find out what broke.
func TestAdmissionReportIndentsEveryLineOfAReason(t *testing.T) {
	multiline := errors.New("list root commits of /w/core: fatal: ambiguous argument 'HEAD': both revision and filename\n" +
		"Use '--' to separate paths from revisions, like this:\n" +
		"'git <command> [<revision>...] -- [<file>...]': exit status 128")

	var buf bytes.Buffer
	writeAdmissionText(&buf, []app.RootAdmission{
		{Root: "core", Mode: documents.VerificationRequired, Applicable: true, Err: multiline},
		{Root: "kb", Mode: documents.VerificationRequired, Applicable: true},
	})

	// The section header sits at column zero by design; everything under it
	// belongs to a root and must stay indented beneath one.
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, "  ") {
			t.Errorf("line escapes the report's indentation: %q", line)
		}
	}
	// The detail must still be present in full — indenting it is not a licence
	// to drop the part that says what to do.
	if !strings.Contains(buf.String(), "Use '--' to separate paths from revisions") {
		t.Error("continuation lines were lost rather than indented")
	}
}

// A reason that indents its own detail — a yaml decode error nesting lines
// under the message they belong to — must keep that structure. Prefixing adds
// depth; it must not flatten what the error's author put there.
func TestAdmissionReportPreservesAReasonsOwnIndentation(t *testing.T) {
	nested := errors.New("parse config: yaml: unmarshal errors:\n  line 12: cannot unmarshal !!str into int\n  line 19: field roots not found")

	var buf bytes.Buffer
	writeAdmissionText(&buf, []app.RootAdmission{
		{Root: "core", Mode: documents.VerificationRequired, Applicable: true, Err: nested},
	})

	if !strings.Contains(buf.String(), "        line 12: cannot unmarshal") {
		t.Errorf("the reason's own two-space nesting was flattened:\n%s", buf.String())
	}
}

// TestRunValidate_JSONReportsTLSPreflight pins that -o json runs the
// front-door preflight: a config whose DNS provider is not registered
// is reported invalid with the reason, not as a valid file.
func TestRunValidate_JSONReportsTLSPreflight(t *testing.T) {
	storage := t.TempDir()
	body := minimalValidConfig + `
tls:
  enabled: true
  hostnames:
    thane.example.net: native
  certmagic:
    agreed: true
    storage: ` + storage + `
    dns:
      provider: nosuchprovider
`
	path := writeConfig(t, body)
	var buf bytes.Buffer

	err := runValidate(&buf, path, "", "json")
	if err == nil {
		t.Fatal("expected error from runValidate, got nil")
	}
	var got struct {
		Valid bool   `json:"valid"`
		Error string `json:"error"`
		TLS   struct {
			Enabled bool   `json:"enabled"`
			OK      bool   `json:"ok"`
			Error   string `json:"error"`
		} `json:"tls"`
	}
	if jerr := json.Unmarshal(buf.Bytes(), &got); jerr != nil {
		t.Fatalf("json output is not valid JSON: %v\nbody:\n%s", jerr, buf.String())
	}
	if got.Valid {
		t.Error("expected valid=false when the front-door preflight fails")
	}
	if !got.TLS.Enabled || got.TLS.OK || !strings.Contains(got.TLS.Error, "not registered") {
		t.Errorf("tls report = %+v, want enabled, not ok, naming the unregistered provider", got.TLS)
	}
	if !strings.Contains(got.Error, "not registered") {
		t.Errorf("top-level error should carry the preflight failure, got %q", got.Error)
	}
}
