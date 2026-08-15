package forge

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	toolpkg "github.com/nugget/thane-ai-agent/internal/tools"
)

func (t *Tools) subscriptionToolDefinitions() []*toolpkg.Tool {
	if t == nil {
		return nil
	}
	return []*toolpkg.Tool{

		{
			Name: "forge_repo_follow",
			Description: "Follow a code forge repository for new releases and/or commits, delivering structured event-source wakes to an existing loop. " +
				"Use this after creating or identifying a thane_loop_create service loop that owns the output document/corpus strategy. Set repo_root when the loop needs source access; omit it for event-only tracking. Thane creates and maintains requested checkouts without exposing a host filesystem path.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo": map[string]any{
						"type":        "string",
						"description": "Repository name — 'owner/repo' or just 'repo' (uses default owner)",
					},
					"account": accountParameter(),
					"name": map[string]any{
						"type":        "string",
						"description": "Optional friendly name for subscription listings",
					},
					"branch": map[string]any{
						"type":        "string",
						"description": "Branch/ref to track for commits. Defaults to the repository default branch.",
					},
					"track_releases": map[string]any{
						"type":        "boolean",
						"description": "Whether to report new releases. Defaults to true.",
					},
					"track_commits": map[string]any{
						"type":        "boolean",
						"description": "Whether to report new commits on branch/ref. Defaults to true.",
					},
					"repo_root": map[string]any{
						"type":        "string",
						"description": "Optional named root handle for a read-only source mirror, such as 'thanecode'. Omit it to follow events without creating a checkout. A loop with a repo_root binding may name only that root; omission remains event-only and does not synthesize a checkout request. Use lowercase ASCII letters, digits, '.', '_', or '-', starting with a letter or digit; Thane canonicalizes uppercase input to lowercase so handles remain portable across filesystems. Thane chooses the checkout path under its workspace, clones it before returning, keeps it current before wakes, and leaves the checkout on disk when unfollowed. Address files as '<repo_root>:path'; never supply a host path.",
					},
					"wake_loop": forgeWakeLoopDefinition(),
				},
				"required": []string{"repo", "wake_loop"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandleRepoFollow(ctx, args)
			},
		},

		{
			Name:        "forge_repo_unfollow",
			Description: "Stop following a code forge repository. Use forge_repo_subscriptions to find the subscription_id.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"subscription_id": map[string]any{
						"type":        "string",
						"description": "The subscription identifier returned by forge_repo_follow or forge_repo_subscriptions.",
					},
				},
				"required": []string{"subscription_id"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandleRepoUnfollow(ctx, args)
			},
		},

		{
			Name:        "forge_repo_subscriptions",
			Description: "List code forge repository event subscriptions with tracking settings, target loop, named repository root, latest observed release/commit, and checkout freshness.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return t.HandleRepoSubscriptions(ctx, args)
			},
		},
	}
}

func forgeWakeLoopDefinition() map[string]any {
	return messages.LoopWakeTargetSchema("Existing loop to wake when repository events are detected. Usually a thane_loop_create service loop that owns the managed document and tagging strategy.")
}
