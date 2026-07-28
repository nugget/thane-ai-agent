package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
)

// facetedCoreLoop declares the write interface a curator loop gets: a
// faceted maintained document and the private notes beside it. The
// report exists so an author can see, before a restart, that those two
// declarations produced the two tools they meant.
const facetedCoreLoop = "# Metacognitive\n\n" +
	"## Spec\n\n" +
	"```yaml\n" +
	"name: metacognitive\n" +
	"parent_name: self\n" +
	"enabled: true\n" +
	"operation: service\n" +
	"sleep_min: 15m\n" +
	"sleep_max: 1h\n" +
	"sleep_default: 30m\n" +
	"jitter: 0.2\n" +
	"outputs:\n" +
	"    - name: metacognitive_state\n" +
	"      type: maintained_document\n" +
	"      ref: core:metacognitive.md\n" +
	"      facets:\n" +
	"        - name: status_line\n" +
	"          format: plain\n" +
	"        - digest\n" +
	"    - name: metacognitive_notes\n" +
	"      type: working_notes\n" +
	"      ref: core:metacognitive-notes.md\n" +
	"```\n\n" +
	"## Task\n\nWatch.\n"

// coreLoopConfig builds the config as boot sees it, with the core root
// already resolved into the path map. The other half — a config that has
// only been loaded, where the report derives the path itself — is
// covered end to end in cmd/thane.
func coreLoopConfig(core string) *config.Config {
	return &config.Config{Paths: map[string]string{"core": core}}
}

func findCoreLoopReport(t *testing.T, reports []CoreLoopDefinition, file string) CoreLoopDefinition {
	t.Helper()
	for _, report := range reports {
		if report.File == file {
			return report
		}
	}
	t.Fatalf("%s is absent from the report: %#v", file, reports)
	return CoreLoopDefinition{}
}

// TestCoreLoopReportNamesWhatTheDocumentProduces is the point of the
// pre-flight check: the declaration an author writes is one thing and
// the tools the loop gets is another, and only the second one is
// evidence the facet block landed.
func TestCoreLoopReportNamesWhatTheDocumentProduces(t *testing.T) {
	core := writeCoreLoop(t, map[string]string{"metacognitive.md": facetedCoreLoop})

	report := findCoreLoopReport(t, CheckCoreLoopDefinitions(coreLoopConfig(core)), "metacognitive.md")
	if !report.OK() {
		t.Fatalf("report.Err = %v, want a clean load", report.Err)
	}
	if report.Name != "metacognitive" || report.ParentName != "self" {
		t.Errorf("name/parent = %q/%q, want metacognitive/cognition", report.Name, report.ParentName)
	}
	want := []string{"publish_output_metacognitive_state", "replace_output_metacognitive_notes"}
	if strings.Join(report.Tools, ",") != strings.Join(want, ",") {
		t.Errorf("tools = %v, want %v — a faceted output publishes and its notes replace", report.Tools, want)
	}
	if len(report.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", report.Warnings)
	}
}

// TestCoreLoopReportCoversEveryDocument is where the report and the
// loader deliberately differ. The loader stops at the first bad file
// because booting with a definition silently absent is worse than
// refusing; the report keeps going because an operator repairing a
// directory should not have to rerun it once per mistake.
func TestCoreLoopReportCoversEveryDocument(t *testing.T) {
	core := writeCoreLoop(t, map[string]string{
		"aaa_broken.md": "# Broken\n\n## Spec\n\n```yaml\nname: broken\nnot_a_field: 1\n```\n",
		"zzz_good.md":   facetedCoreLoop,
	})

	if _, err := loadCoreLoopDefinitions(core); err == nil {
		t.Fatal("loadCoreLoopDefinitions accepted a malformed document")
	}

	reports := CheckCoreLoopDefinitions(coreLoopConfig(core))
	if len(reports) != 2 {
		t.Fatalf("reported %d documents, want both: %#v", len(reports), reports)
	}
	broken := findCoreLoopReport(t, reports, "aaa_broken.md")
	if broken.OK() {
		t.Error("the malformed document reports as loadable")
	}
	if !strings.Contains(broken.Error, "not_a_field") {
		t.Errorf("error = %q, want it to name the unrecognized key", broken.Error)
	}
	if good := findCoreLoopReport(t, reports, "zzz_good.md"); !good.OK() {
		t.Errorf("a valid document was reported bad because another failed: %v", good.Err)
	}
}

// TestCoreLoopReportWarnsOnAnUnparentedCoreLoop covers the trap the
// report was written for. The built-in path parents ego, metacognitive,
// and archivist under cognition after building the spec, and a document
// replaces that path entirely — so a document that says nothing about
// its parent does not inherit the default, it moves the loop to the
// root, and nothing about a successful boot says so.
func TestCoreLoopReportWarnsOnAnUnparentedCoreLoop(t *testing.T) {
	core := writeCoreLoop(t, map[string]string{
		"metacognitive.md": strings.Replace(facetedCoreLoop, "parent_name: self\n", "", 1),
	})

	report := findCoreLoopReport(t, CheckCoreLoopDefinitions(coreLoopConfig(core)), "metacognitive.md")
	if !report.OK() {
		t.Fatalf("report.Err = %v; the document is valid, just unparented", report.Err)
	}
	if len(report.Warnings) == 0 {
		t.Fatal("an unparented core service loop produced no warning")
	}
	if !strings.Contains(report.Warnings[0], selfContainerName) {
		t.Errorf("warning = %q, want it to name the parent that was not inherited", report.Warnings[0])
	}
}

// TestCoreLoopReportLeavesAnOrdinaryLoopUnparented is the other half.
// Only the core service loops have a parent they are expected to
// inherit; warning about every root-level definition would train an
// operator to ignore the one that matters.
func TestCoreLoopReportLeavesAnOrdinaryLoopUnparented(t *testing.T) {
	core := writeCoreLoop(t, map[string]string{"ranch.md": minimalCoreLoop})

	report := findCoreLoopReport(t, CheckCoreLoopDefinitions(coreLoopConfig(core)), "ranch.md")
	for _, warning := range report.Warnings {
		if strings.Contains(warning, "parent_name") {
			t.Errorf("warning = %q, want no parent warning for an ordinary loop", warning)
		}
	}
}

// TestCoreLoopReportFlagsTwoDocumentsForOneLoop names both files. The
// loader refuses this directory, and "one document is one loop" is not
// actionable without knowing which two collided.
func TestCoreLoopReportFlagsTwoDocumentsForOneLoop(t *testing.T) {
	core := writeCoreLoop(t, map[string]string{
		"a_metacognitive.md": facetedCoreLoop,
		"b_metacognitive.md": facetedCoreLoop,
	})

	report := findCoreLoopReport(t, CheckCoreLoopDefinitions(coreLoopConfig(core)), "b_metacognitive.md")
	if len(report.Warnings) == 0 || !strings.Contains(report.Warnings[0], "a_metacognitive.md") {
		t.Errorf("warnings = %v, want the other file named", report.Warnings)
	}
}

// TestCoreLoopReportIsSilentWithoutADirectory keeps the check from
// inventing a finding. An install that defines no loops in its root is
// ordinary.
func TestCoreLoopReportIsSilentWithoutADirectory(t *testing.T) {
	if reports := CheckCoreLoopDefinitions(coreLoopConfig(t.TempDir())); reports != nil {
		t.Errorf("reports = %#v, want nil for a core with no loops directory", reports)
	}
	if reports := CheckCoreLoopDefinitions(&config.Config{}); reports != nil {
		t.Errorf("reports = %#v, want nil when no core root is configured", reports)
	}
}

// TestMalformedDefinitionIsAuthoringNotEnvironment is what stops the
// crash loop. thane-agent-macos restarts the binary when it exits
// non-zero, and a document that does not parse produces the identical
// failure on every attempt — so the error has to be distinguishable
// from the transient kind before the CLI can decide to stop.
func TestMalformedDefinitionIsAuthoringNotEnvironment(t *testing.T) {
	core := writeCoreLoop(t, map[string]string{
		"broken.md": "# Broken\n\n## Spec\n\n```yaml\nname: broken\nnot_a_field: 1\n```\n",
	})
	app := &App{cfg: coreLoopConfig(core)}

	_, err := app.buildLoopDefinitionBaseSpecs()
	if err == nil {
		t.Fatal("buildLoopDefinitionBaseSpecs accepted a malformed document")
	}
	var authoring *CoreAuthoringError
	if !errors.As(err, &authoring) {
		t.Fatalf("err = %v (%T), want a CoreAuthoringError so the CLI can exit terminally", err, err)
	}
	if !strings.Contains(err.Error(), "broken.md") {
		t.Errorf("err = %v, want the file named", err)
	}
}

// TestCoreLoopReportSurfacesAnUnreadableDirectory covers the gap between
// "no loops here" and "cannot tell". The loader refuses to boot over a
// directory it cannot read; a report that returned nothing for the same
// condition would certify an instance about to be rejected, which is the
// exact drift this check exists to close.
func TestCoreLoopReportSurfacesAnUnreadableDirectory(t *testing.T) {
	core := writeCoreLoop(t, map[string]string{"metacognitive.md": facetedCoreLoop})
	dir := filepath.Join(core, coreLoopsDirName)
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Skipf("cannot remove read permission: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if _, err := os.ReadDir(dir); err == nil {
		t.Skip("directory is still readable (running as root?)")
	}

	if _, err := loadCoreLoopDefinitions(core); err == nil {
		t.Fatal("loadCoreLoopDefinitions accepted an unreadable directory")
	}
	reports := CheckCoreLoopDefinitions(coreLoopConfig(core))
	if len(reports) != 1 || reports[0].OK() {
		t.Fatalf("reports = %#v, want one failure for the unreadable directory", reports)
	}
}

// TestCoreLoopReportRefusesASymlinkTheLoaderRefuses mirrors the loader's
// regular-file rule. A symlink here reads content from outside the
// signed root while looking like part of it, which is the one thing a
// definition living in core is meant to rule out — so the check that
// runs before a boot has to refuse exactly what the boot refuses.
func TestCoreLoopReportRefusesASymlinkTheLoaderRefuses(t *testing.T) {
	core := writeCoreLoop(t, map[string]string{"metacognitive.md": facetedCoreLoop})
	outside := filepath.Join(t.TempDir(), "elsewhere.md")
	if err := os.WriteFile(outside, []byte(facetedCoreLoop), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	link := filepath.Join(core, coreLoopsDirName, "smuggled.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	if _, err := loadCoreLoopDefinitions(core); err == nil {
		t.Fatal("loadCoreLoopDefinitions accepted a symlinked definition")
	}
	report := findCoreLoopReport(t, CheckCoreLoopDefinitions(coreLoopConfig(core)), "smuggled.md")
	if report.OK() {
		t.Fatal("the report certifies a symlink the loader refuses")
	}
	if !strings.Contains(report.Error, "not a regular file") {
		t.Errorf("error = %q, want the loader's own refusal", report.Error)
	}
}
