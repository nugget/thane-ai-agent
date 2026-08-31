package app

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
)

// foundAgentRoot establishes a signed root the way Thane does — born and
// signed by the agent's own key — and returns where it lives plus the keys
// involved. Every case below starts from this same repository, so what varies
// between them is only the config being judged, never the history.
func foundAgentRoot(t *testing.T) (rootPath, signingKey string, agentSeed, strangerSeed []config.AllowedSigner) {
	t.Helper()
	rootPath = t.TempDir()
	signingKey, agentPublicKey := writeTestSigningKey(t)
	_, strangerPublicKey := writeTestSigningKey(t)

	agentSeed = []config.AllowedSigner{{Principal: provenance.AgentPrincipal, Key: agentPublicKey}}
	strangerSeed = []config.AllowedSigner{{Principal: "stranger@example.com", Key: strangerPublicKey}}

	resolver := paths.New(map[string]string{"kb": rootPath})
	founder := &App{
		logger: slog.Default(),
		cfg: &config.Config{
			Paths: map[string]string{"kb": rootPath},
			DocRoots: map[string]config.DocumentRootConfig{
				"kb": {
					SeedSigners: agentSeed,
					Git: config.DocumentRootGitConfig{
						Enabled:          true,
						SignCommits:      true,
						VerifySignatures: "required",
						SigningKey:       signingKey,
					},
				},
			},
		},
	}
	if _, err := founder.buildDocumentStoreOptions(buildDocumentRoots(resolver), resolver); err != nil {
		t.Fatalf("founding the fixture root: %v", err)
	}
	return rootPath, signingKey, agentSeed, strangerSeed
}

// TestRootAdmissionReportMatchesBoot is the anti-drift trap for this feature.
//
// `thane validate` exists to answer "will serve start?", and it earns that
// only while its report and the boot gate reach the same verdict. They share
// the predicate, the policy derivation, and the check itself — but sharing is
// a property of today's code, not a guarantee about tomorrow's. So rather than
// assert what each path returns, this drives both real paths over the same
// repository and asserts they agree. Edit the eligibility rule in one place
// and forget the other, and this fails.
func TestRootAdmissionReportMatchesBoot(t *testing.T) {
	rootPath, signingKey, agentSeed, strangerSeed := foundAgentRoot(t)

	tests := []struct {
		name string
		root config.DocumentRootConfig
		// wantRefused is what both paths must conclude: boot returns an
		// error and the report marks the root unadmitted, or neither does.
		wantRefused bool
	}{
		{
			name: "founder declared",
			root: config.DocumentRootConfig{
				SeedSigners: agentSeed,
				Git: config.DocumentRootGitConfig{
					Enabled: true, SignCommits: true,
					VerifySignatures: "required", SigningKey: signingKey,
				},
			},
		},
		{
			name: "founder not declared",
			root: config.DocumentRootConfig{
				SeedSigners: strangerSeed,
				Git: config.DocumentRootGitConfig{
					Enabled: true, SignCommits: true,
					VerifySignatures: "required", SigningKey: signingKey,
				},
			},
			wantRefused: true,
		},
		{
			name: "verify only, founder not declared",
			root: config.DocumentRootConfig{
				SeedSigners: strangerSeed,
				Git: config.DocumentRootGitConfig{
					Enabled: true, VerifySignatures: "required",
				},
			},
			wantRefused: true,
		},
		{
			name: "no seed signers declared",
			root: config.DocumentRootConfig{
				Git: config.DocumentRootGitConfig{
					Enabled: true, VerifySignatures: "required",
				},
			},
		},
		{
			name: "verification disabled",
			root: config.DocumentRootConfig{
				SeedSigners: strangerSeed,
				Git: config.DocumentRootGitConfig{
					Enabled: true, VerifySignatures: "none",
				},
			},
		},
		{
			name: "git disabled",
			root: config.DocumentRootConfig{
				SeedSigners: strangerSeed,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				Paths:    map[string]string{"kb": rootPath},
				DocRoots: map[string]config.DocumentRootConfig{"kb": tc.root},
			}
			instance := &App{logger: slog.Default(), cfg: cfg}
			resolver := paths.New(cfg.Paths)

			_, bootErr := instance.buildDocumentStoreOptions(buildDocumentRoots(resolver), resolver)
			bootRefused := bootErr != nil

			reportRefused := false
			for _, result := range CheckRootAdmission(t.Context(), cfg) {
				if !result.Admitted() {
					reportRefused = true
				}
			}

			if bootRefused != reportRefused {
				t.Fatalf("validate and boot disagree: boot refused=%v (%v), report refused=%v",
					bootRefused, bootErr, reportRefused)
			}
			if bootRefused != tc.wantRefused {
				t.Fatalf("refused = %v, want %v (boot error: %v)", bootRefused, tc.wantRefused, bootErr)
			}
		})
	}
}

// TestCheckRootAdmissionSkipsUnestablishedRoots pins the one place validate
// deliberately differs from boot: a signing root whose directory does not
// exist yet is one serve would create and birth-commit, so reporting it as
// unadmitted would be a false alarm about a root there is nothing to judge.
func TestCheckRootAdmissionSkipsUnestablishedRoots(t *testing.T) {
	signingKey, agentPublicKey := writeTestSigningKey(t)
	missing := t.TempDir() + "/not-created-yet"

	cfg := &config.Config{
		Paths: map[string]string{"kb": missing},
		DocRoots: map[string]config.DocumentRootConfig{
			"kb": {
				SeedSigners: []config.AllowedSigner{{
					Principal: provenance.AgentPrincipal, Key: agentPublicKey,
				}},
				Git: config.DocumentRootGitConfig{
					Enabled: true, SignCommits: true,
					VerifySignatures: "required", SigningKey: signingKey,
				},
			},
		},
	}
	if results := CheckRootAdmission(t.Context(), cfg); len(results) != 0 {
		t.Fatalf("CheckRootAdmission = %+v, want no results for a root that does not exist yet", results)
	}
}

// TestRootAdmissionCoversTheReservedCoreRoot guards the root that is easiest
// to lose. core carries policy under roots.core but never a path, so it is
// absent from cfg.Paths as loaded and appears only after being derived from
// workspace.path. An enumeration that skips that derivation reports on every
// root except the one holding the config — and reports success while doing it.
func TestRootAdmissionCoversTheReservedCoreRoot(t *testing.T) {
	workspace := t.TempDir()
	signingKey, agentPublicKey := writeTestSigningKey(t)
	_, strangerPublicKey := writeTestSigningKey(t)

	coreRoot := func(seeds []config.AllowedSigner) *config.Config {
		return &config.Config{
			Workspace: config.WorkspaceConfig{Path: workspace},
			DocRoots: map[string]config.DocumentRootConfig{
				"core": {
					SeedSigners: seeds,
					Git: config.DocumentRootGitConfig{
						Enabled:          true,
						SignCommits:      true,
						VerifySignatures: "required",
						SigningKey:       signingKey,
					},
				},
			},
		}
	}
	bootstrap := func(cfg *config.Config) error {
		instance := &App{logger: slog.Default(), cfg: cfg}
		resolver := paths.New(documentRootPaths(cfg, nil))
		_, err := instance.buildDocumentStoreOptions(buildDocumentRoots(resolver), resolver)
		return err
	}

	agentSeed := []config.AllowedSigner{{Principal: provenance.AgentPrincipal, Key: agentPublicKey}}
	founded := coreRoot(agentSeed)
	if err := bootstrap(founded); err != nil {
		t.Fatalf("founding core: %v", err)
	}

	results := CheckRootAdmission(t.Context(), coreRoot(agentSeed))
	if len(results) != 1 || results[0].Root != "core" {
		t.Fatalf("CheckRootAdmission = %+v, want one entry for core", results)
	}
	if !results[0].Admitted() {
		t.Fatalf("core founded by its declared seed should be admitted: %v", results[0].Err)
	}

	// Now disagree with history, and confirm both paths say so.
	stranger := coreRoot([]config.AllowedSigner{{Principal: "stranger@example.com", Key: strangerPublicKey}})
	if err := bootstrap(stranger); err == nil {
		t.Fatal("boot should refuse a core whose founder is not a declared seed")
	}
	results = CheckRootAdmission(t.Context(), stranger)
	if len(results) != 1 || results[0].Admitted() {
		t.Fatalf("validate should report core unadmitted, got %+v", results)
	}
}

// TestSelfRootIsDerivedLikeCore pins what makes self usable by a shipped
// document. The core loops write self: on every install, so the root has
// to exist without an operator declaring it — and its path has to come
// from the workspace, not from roots:, or two installs could disagree
// about where self:metacognitive.md is while running the same document.
func TestSelfRootIsDerivedLikeCore(t *testing.T) {
	workspace := t.TempDir()
	cfg := &config.Config{Workspace: config.WorkspaceConfig{Path: workspace}}

	paths := documentRootPaths(cfg, nil)
	if got, want := paths[config.SelfRootName], filepath.Join(workspace, "self"); got != want {
		t.Errorf("self root = %q, want %q", got, want)
	}
	if got, want := paths[config.CoreRootName], filepath.Join(workspace, "core"); got != want {
		t.Errorf("core root = %q, want %q", got, want)
	}
}

// TestSelfRootIgnoresAConfiguredPath mirrors core's rule. A derived root
// whose path could also be declared would let roots: and workspace.path
// disagree, and the shipped documents name self: with no way to ask
// which one won.
func TestSelfRootIgnoresAConfiguredPath(t *testing.T) {
	workspace := t.TempDir()
	cfg := &config.Config{
		Workspace: config.WorkspaceConfig{Path: workspace},
		Paths:     map[string]string{config.SelfRootName: "/somewhere/else"},
	}

	if got, want := documentRootPaths(cfg, nil)[config.SelfRootName], filepath.Join(workspace, "self"); got != want {
		t.Errorf("self root = %q, want the derived %q; a configured path must not win", got, want)
	}
}

func TestContactsRootIsDerivedOnlyWhenDeclared(t *testing.T) {
	workspace := t.TempDir()
	cfg := &config.Config{Workspace: config.WorkspaceConfig{Path: workspace}}
	if _, ok := documentRootPaths(cfg, nil)[config.ContactsRootName]; ok {
		t.Fatal("contacts root was registered without an explicit policy declaration")
	}

	cfg.DocRoots = map[string]config.DocumentRootConfig{
		config.ContactsRootName: {Authoring: "managed"},
	}
	paths := documentRootPaths(cfg, nil)
	if got, want := paths[config.ContactsRootName], filepath.Join(workspace, config.ContactsRootName); got != want {
		t.Fatalf("contacts root = %q, want derived %q", got, want)
	}
}
