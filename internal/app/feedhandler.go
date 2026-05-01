package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/media"
	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// mediaFeedTurnBuilder checks RSS/Atom feeds for new entries and prepares
// an agent turn when new content is found. When no new content is detected,
// it returns nil so the loop runtime treats the wake as a no-op.
func mediaFeedTurnBuilder(poller *media.FeedPoller, logger *slog.Logger) looppkg.TurnBuilder {
	return func(ctx context.Context, _ looppkg.TurnInput) (*looppkg.AgentTurn, error) {
		wakeMsg, err := poller.CheckFeeds(ctx)
		if err != nil {
			return nil, fmt.Errorf("check feeds: %w", err)
		}
		if wakeMsg == "" {
			return nil, nil // nothing new — no LLM call
		}

		msg := prompts.MediaFeedPollWakePrompt(wakeMsg)
		convID := fmt.Sprintf("media-feed-%d", time.Now().UnixMilli())

		logger.Info("new feed content detected, preparing agent turn",
			"conv_id", convID,
			"wake_msg_len", len(wakeMsg),
		)

		return &looppkg.AgentTurn{
			Request: looppkg.Request{
				ConversationID: convID,
				Messages:       []looppkg.Message{{Role: "user", Content: msg}},
				Hints: map[string]string{
					"source":                    "media_feed_poll",
					router.HintLocalOnly:        "false",
					router.HintQualityFloor:     "5",
					router.HintMission:          "automation",
					router.HintDelegationGating: "disabled",
				},
			},
			Summary: map[string]any{
				"wake_msg_len": len(wakeMsg),
			},
		}, nil
	}
}
