package tools

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
)

func (r *Registry) registerForgeSubscriptionTools() {
	if r.forgeTools == nil {
		return
	}

	r.Register(&Tool{
		Name: "forge_repo_follow",
		Description: "Follow a code forge repository for new releases and/or commits, delivering structured event-source wakes to an existing loop. " +
			"Use this after creating or identifying a thane_curate loop that owns the output document/corpus strategy.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo": map[string]any{
					"type":        "string",
					"description": "Repository name — 'owner/repo' or just 'repo' (uses default owner)",
				},
				"account": map[string]any{
					"type":        "string",
					"description": "Forge account name (default: primary account)",
				},
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
				"wake_loop": forgeWakeLoopDefinition(),
			},
			"required": []string{"repo", "wake_loop"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandleRepoFollow(ctx, args)
		},
	})

	r.Register(&Tool{
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
			return r.forgeTools.HandleRepoUnfollow(ctx, args)
		},
	})

	r.Register(&Tool{
		Name:        "forge_repo_subscriptions",
		Description: "List code forge repository event subscriptions with tracking settings, target loop, and latest observed release/commit.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return r.forgeTools.HandleRepoSubscriptions(ctx, args)
		},
	})
}

func forgeWakeLoopDefinition() map[string]any {
	return messages.LoopWakeTargetSchema("Existing loop to wake when repository events are detected. Usually a thane_curate loop that owns the managed document and tagging strategy.")
}
