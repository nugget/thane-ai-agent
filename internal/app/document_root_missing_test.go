package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
)

// A derived root that is not on disk has to be reported, not skipped. Serve
// would create it and sign its birth commit with the agent key, and admission
// then refuses that commit because the root's seed signers name the operator —
// so silence here would let `thane validate && thane serve` report ready for an
// instance that cannot start. The operator's only other signal is noticing an
// absence from a list of what is present, which is not a signal.
func TestCheckRootAdmissionReportsMissingDerivedRoots(t *testing.T) {
	workspace := t.TempDir()
	_, operatorKey := writeTestSigningKey(t)

	signedRoot := func() config.DocumentRootConfig {
		return config.DocumentRootConfig{
			SeedSigners: []config.AllowedSigner{
				{Principal: "operator@example.com", Key: operatorKey},
			},
			Git: config.DocumentRootGitConfig{
				Enabled:          true,
				SignCommits:      true,
				VerifySignatures: "required",
			},
		}
	}

	cfg := &config.Config{
		Workspace: config.WorkspaceConfig{Path: workspace},
		DocRoots: map[string]config.DocumentRootConfig{
			config.CoreRootName:     signedRoot(),
			config.SelfRootName:     signedRoot(),
			config.ContactsRootName: signedRoot(),
		},
	}

	results := CheckRootAdmission(context.Background(), cfg)

	byRoot := make(map[string]RootAdmission, len(results))
	for _, r := range results {
		byRoot[r.Root] = r
	}

	for _, root := range []string{config.CoreRootName, config.SelfRootName, config.ContactsRootName} {
		t.Run(root, func(t *testing.T) {
			got, ok := byRoot[root]
			if !ok {
				t.Fatalf("%s is missing from disk but absent from the admission report", root)
			}
			if got.Admitted() {
				t.Errorf("%s does not exist yet but was reported admitted", root)
			}
			if !got.Fatal() {
				t.Errorf("%s is verify_signatures=required, so its absence must refuse serve", root)
			}
			// The report is what an operator repairs from, so it has to name
			// the path and a way out rather than only stating the problem.
			if want := filepath.Join(workspace, root); !strings.Contains(got.Err.Error(), want) {
				t.Errorf("error should name the derived path %s, got: %v", want, got.Err)
			}
			if !strings.Contains(got.Err.Error(), "git clone") || !strings.Contains(got.Err.Error(), "thane init") {
				t.Errorf("error should teach both recovery paths, got: %v", got.Err)
			}
			// RepoPath reports the repository admission judged, and admission
			// never ran. Populating it with a directory that does not exist
			// would tell a script reading validate --json otherwise.
			if got.RepoPath != "" {
				t.Errorf("RepoPath = %q, want empty for a root that was never judged", got.RepoPath)
			}
			// cfg.DocRoots is fed by either the current roots: shape or the
			// legacy doc_roots: one, so a fully-qualified key would be wrong
			// for half of readers.
			if strings.Contains(got.Err.Error(), "roots."+root) {
				t.Errorf("error should name config fields without a top-level prefix, got: %v", got.Err)
			}
		})
	}
}

// A declared root is different: validate creates nothing, serve bootstraps and
// births it, and the birth is admissible because nothing has narrowed who may
// sign it. Reporting that would be a false alarm about a root that does not
// exist to judge, so the existing silence stays.
func TestCheckRootAdmissionStaysQuietAboutMissingDeclaredRoots(t *testing.T) {
	workspace := t.TempDir()
	_, operatorKey := writeTestSigningKey(t)

	cfg := &config.Config{
		Workspace: config.WorkspaceConfig{Path: workspace},
		Paths:     map[string]string{"kb": filepath.Join(workspace, "does-not-exist")},
		DocRoots: map[string]config.DocumentRootConfig{
			"kb": {
				SeedSigners: []config.AllowedSigner{
					{Principal: "operator@example.com", Key: operatorKey},
				},
				Git: config.DocumentRootGitConfig{
					Enabled:          true,
					SignCommits:      true,
					VerifySignatures: "required",
				},
			},
		},
	}

	for _, r := range CheckRootAdmission(context.Background(), cfg) {
		if r.Root == "kb" {
			t.Errorf("a missing declared root should not be reported, got: %+v", r)
		}
	}
}
