package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/forge"
	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// forgeSubscriptionTurnBuilder checks followed code forge repositories
// for new releases/commits and prepares an agent turn when updates exist.
func forgeSubscriptionTurnBuilder(poller *forge.SubscriptionPoller, logger *slog.Logger) looppkg.TurnBuilder {
	return func(ctx context.Context, _ looppkg.TurnInput) (*looppkg.AgentTurn, error) {
		wakeMsg, err := poller.CheckSubscriptions(ctx)
		if err != nil {
			return nil, fmt.Errorf("check forge subscriptions: %w", err)
		}
		if wakeMsg == "" {
			return nil, nil
		}

		msg := prompts.ForgeSubscriptionWakePrompt(wakeMsg)
		convID := fmt.Sprintf("forge-subscription-%d", time.Now().UnixMilli())

		logger.Info("new forge subscription updates detected, preparing agent turn",
			"conv_id", convID,
			"wake_msg_len", len(wakeMsg),
		)

		return &looppkg.AgentTurn{
			Request: looppkg.Request{
				ConversationID: convID,
				Messages:       []looppkg.Message{{Role: "user", Content: msg}},
				InitialTags:    []string{"forge"},
				Hints: map[string]string{
					"source":                    "forge_subscription_poll",
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
