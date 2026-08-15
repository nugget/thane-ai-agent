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
				"Use this after creating or identifying a thane_loop_create service loop that owns the output document/corpus strategy.",
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
					"local_checkout": map[string]any{
						"type":        "string",
						"description": "Optional path to keep as a read-only mirror checkout. Absolute, working-directory-relative, or ~-prefixed. A leading ~ is expanded against the account Thane runs as, so prefer the ~ form over guessing a home directory: an absolute path under the wrong user is not an error, it is a real location nobody intended. The follow clones into this path before returning, so the working tree exists when the call succeeds and a failed clone fails the call. The poller keeps it current afterwards; with polling disabled it is created but never refreshed. The path must be empty or an existing Thane-owned mirror checkout; non-empty directories and unmarked git checkouts are refused. The subscription poller syncs this path before waking the loop and leaves it on disk when unfollowed.",
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
			Description: "List code forge repository event subscriptions with tracking settings, target loop, and latest observed release/commit.",
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
