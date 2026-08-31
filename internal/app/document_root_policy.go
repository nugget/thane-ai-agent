package app

import (
	"strings"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

func documentRootPolicyFromConfig(rootCfg config.DocumentRootConfig) documents.RootPolicy {
	policy := documents.RootPolicy{
		Indexing:  true,
		Authoring: documents.AuthoringManaged,
		Git: documents.RootGitPolicy{
			VerifySignatures: documents.VerificationNone,
		},
	}
	if rootCfg.Indexing != nil {
		policy.Indexing = *rootCfg.Indexing
	}
	if authoring := strings.TrimSpace(rootCfg.Authoring); authoring != "" {
		policy.Authoring = documents.AuthoringMode(authoring)
	}
	gitCfg := rootCfg.Git
	policy.Git.Enabled = gitCfg.Enabled
	policy.Git.SignCommits = gitCfg.SignCommits
	if verify := strings.TrimSpace(gitCfg.VerifySignatures); verify != "" {
		policy.Git.VerifySignatures = documents.VerificationMode(verify)
	}
	policy.Git.RepoPath = strings.TrimSpace(gitCfg.RepoPath)
	policy.Context = documents.RootContextPolicy{
		Inject:      strings.TrimSpace(rootCfg.Context.Inject),
		Search:      strings.TrimSpace(rootCfg.Context.Search),
		Advertise:   strings.TrimSpace(rootCfg.Context.Advertise),
		RequiresTag: strings.TrimSpace(rootCfg.Context.RequiresTag),
		Untagged:    strings.TrimSpace(rootCfg.Context.Untagged),
	}
	return policy
}
