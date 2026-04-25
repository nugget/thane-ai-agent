package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/email"
	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// emailPollHandler returns a loop handler that checks IMAP accounts for
// new messages and dispatches an agent conversation when new mail is
// found. When no new messages are detected, the handler returns nil
// without invoking the LLM, avoiding unnecessary token spend.
func emailPollHandler(poller *email.Poller, runner agentRunner, logger *slog.Logger) func(context.Context, any) error {
	return func(ctx context.Context, _ any) error {
		wakeMsg, err := poller.CheckNewMessages(ctx)
		if err != nil {
			return fmt.Errorf("check new messages: %w", err)
		}
		if wakeMsg == "" {
			return nil // nothing new — no LLM call
		}

		// Dispatch agent conversation for the new mail.
		msg := prompts.EmailPollWakePrompt(wakeMsg)
		convID := fmt.Sprintf("email-poll-%d", time.Now().UnixMilli())

		logger.Info("new email detected, dispatching agent",
			"conv_id", convID,
			"wake_msg_len", len(wakeMsg),
		)

		resp, err := runner.Run(ctx, &agent.Request{
			ConversationID: convID,
			Messages:       []agent.Message{{Role: "user", Content: msg}},
			Hints: map[string]string{
				"source":                    "email_poll",
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

		logger.Info("email triage complete",
			"conv_id", convID,
			"result_len", len(resp.Content),
		)
		return nil
	}
}
