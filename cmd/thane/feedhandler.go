package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/media"
	"github.com/nugget/thane-ai-agent/internal/prompts"
	"github.com/nugget/thane-ai-agent/internal/router"
)

// mediaFeedHandler returns a loop handler that checks RSS/Atom feeds
// for new entries and dispatches an agent conversation when new content
// is found. When no new content is detected, the handler returns nil
// without invoking the LLM, avoiding unnecessary token spend.
func mediaFeedHandler(poller *media.FeedPoller, runner agentRunner, logger *slog.Logger) func(context.Context, any) error {
	return func(ctx context.Context, _ any) error {
		wakeMsg, err := poller.CheckFeeds(ctx)
		if err != nil {
			return fmt.Errorf("check feeds: %w", err)
		}
		if wakeMsg == "" {
			return nil // nothing new — no LLM call
		}

		// Dispatch agent conversation for the new content.
		msg := prompts.MediaFeedPollWakePrompt(wakeMsg)
		convID := fmt.Sprintf("media-feed-%d", time.Now().UnixMilli())

		logger.Info("new feed content detected, dispatching agent",
			"conv_id", convID,
			"wake_msg_len", len(wakeMsg),
		)

		resp, err := runner.Run(ctx, &agent.Request{
			ConversationID: convID,
			Messages:       []agent.Message{{Role: "user", Content: msg}},
			Hints: map[string]string{
				"source":                    "media_feed_poll",
				router.HintLocalOnly:        "false",
				router.HintQualityFloor:     "5",
				router.HintMission:          "automation",
				router.HintDelegationGating: "disabled",
			},
		}, nil)
		if err != nil {
			return fmt.Errorf("agent dispatch: %w", err)
		}

		if summary := looppkg.IterationSummary(ctx); summary != nil && resp != nil {
			summary["request_id"] = resp.RequestID
		}

		logger.Info("media feed analysis complete",
			"conv_id", convID,
			"result_len", len(resp.Content),
		)
		return nil
	}
}
