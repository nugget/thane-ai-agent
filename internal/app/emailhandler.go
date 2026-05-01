package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/email"
	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// emailPollTurnBuilder checks IMAP accounts for new messages and prepares
// an agent turn when new mail is found. When no new messages are detected,
// it returns nil so the loop runtime treats the wake as a no-op.
func emailPollTurnBuilder(poller *email.Poller, logger *slog.Logger) looppkg.TurnBuilder {
	return func(ctx context.Context, _ looppkg.TurnInput) (*looppkg.AgentTurn, error) {
		wakeMsg, err := poller.CheckNewMessages(ctx)
		if err != nil {
			return nil, fmt.Errorf("check new messages: %w", err)
		}
		if wakeMsg == "" {
			return nil, nil // nothing new — no LLM call
		}

		msg := prompts.EmailPollWakePrompt(wakeMsg)
		convID := fmt.Sprintf("email-poll-%d", time.Now().UnixMilli())

		logger.Info("new email detected, preparing agent turn",
			"conv_id", convID,
			"wake_msg_len", len(wakeMsg),
		)

		return &looppkg.AgentTurn{
			Request: looppkg.Request{
				ConversationID: convID,
				Messages:       []looppkg.Message{{Role: "user", Content: msg}},
				Hints: map[string]string{
					"source":                    "email_poll",
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
