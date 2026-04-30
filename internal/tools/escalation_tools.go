package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/notifications"
)

// defaultEscalationTimeout is the default time to wait for a human
// response before the escalation tool returns a timeout.
const defaultEscalationTimeout = 10 * time.Minute

// EscalationDeps holds the dependencies for escalation tools.
type EscalationDeps struct {
	Router     *notifications.NotificationRouter
	Records    *notifications.RecordStore
	Dispatcher *notifications.CallbackDispatcher
	Waiter     *notifications.ResponseWaiter
}

// SetEscalationTools registers the request_human_escalation and
// request_ai_escalation tools. Requires notification routing, record
// tracking, callback dispatch, and synchronous response waiting.
func (r *Registry) SetEscalationTools(deps EscalationDeps) {
	if deps.Router == nil || deps.Records == nil || deps.Dispatcher == nil || deps.Waiter == nil {
		return
	}
	r.registerHumanEscalation(deps)
	r.registerAIEscalation(deps)
}

// registerHumanEscalation adds the request_human_escalation tool.
// This tool sends a question to the best available channel and blocks
// until the human responds or the timeout expires.
func (r *Registry) registerHumanEscalation(deps EscalationDeps) {
	r.Register(&Tool{
		Name: "request_human_escalation",
		Description: "Request a decision from a human. Sends the question to the best available channel " +
			"(Signal if active, HA push otherwise) and waits for a response. " +
			"Use this when you need human judgment to proceed — ambiguous requirements, " +
			"risky actions, or decisions outside your authority.\n\n" +
			"The tool BLOCKS until the human responds or the timeout expires. " +
			"Returns the chosen action ID or a timeout indicator.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"recipient": map[string]any{
					"type":        "string",
					"description": "Contact name to ask (e.g., \"nugget\")",
				},
				"question": map[string]any{
					"type":        "string",
					"description": "The question or decision needed",
				},
				"context": map[string]any{
					"type":        "string",
					"description": "Relevant context to help the human decide",
				},
				"actions": map[string]any{
					"type":        "array",
					"description": "Structured choices. Each must have 'id' and 'label'.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":    map[string]any{"type": "string"},
							"label": map[string]any{"type": "string"},
						},
						"required": []string{"id", "label"},
					},
				},
				"timeout": map[string]any{
					"type":        "string",
					"description": "How long to wait (Go duration, e.g., \"10m\"). Default: 10m.",
				},
				"urgency": map[string]any{
					"type":        "string",
					"description": "Notification priority: low, normal, urgent",
					"enum":        []string{"low", "normal", "urgent"},
				},
			},
			"required": []string{"recipient", "question", "actions"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			recipient, _ := args["recipient"].(string)
			question, _ := args["question"].(string)
			escalationCtx, _ := args["context"].(string)

			if recipient == "" || question == "" {
				return "", fmt.Errorf("recipient and question are required")
			}

			// Parse options.
			actions := parseActionsArg(args)
			if len(actions) == 0 {
				return "", fmt.Errorf("at least one option is required")
			}

			// Parse timeout.
			timeout := defaultEscalationTimeout
			if t, ok := args["timeout"].(string); ok && t != "" {
				parsed, err := time.ParseDuration(t)
				if err != nil {
					return "", fmt.Errorf("invalid timeout %q: %w", t, err)
				}
				timeout = parsed
			}

			// Map urgency to priority.
			priority := "normal"
			if u, ok := args["urgency"].(string); ok && u != "" {
				priority = u
			}

			// Build the notification message.
			var msg strings.Builder
			msg.WriteString(question)
			if escalationCtx != "" {
				msg.WriteString("\n\nContext: ")
				msg.WriteString(escalationCtx)
			}

			// Send via the notification router.
			req := notifications.ActionableRequest{
				NotificationRequest: notifications.NotificationRequest{
					Recipient: recipient,
					Title:     "Decision Needed",
					Message:   msg.String(),
					Priority:  priority,
				},
				Actions:       actions,
				Timeout:       timeout,
				TimeoutAction: "cancel",
				Context:       escalationCtx,
			}

			requestID, err := deps.Router.SendActionable(
				ctx, req,
				SessionIDFromContext(ctx),
				ConversationIDFromContext(ctx),
			)
			if err != nil {
				return "", fmt.Errorf("send escalation: %w", err)
			}

			// Register a synchronous waiter and block.
			ch := deps.Waiter.Register(requestID)
			resp, err := deps.Waiter.WaitWithTimeout(ctx, requestID, ch, timeout)
			if err != nil {
				// Propagate context cancellation (run shutting down)
				// instead of masking it as a timeout.
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				return "", fmt.Errorf("wait for escalation response: %w", err)
			}

			if resp.TimedOut {
				return fmt.Sprintf("Escalation %s: human did not respond within %s.", requestID, timeout), nil
			}

			// Find the label for the chosen action.
			label := resp.ActionID
			for _, a := range actions {
				if a.ID == resp.ActionID {
					label = a.Label
					break
				}
			}

			return fmt.Sprintf("Human responded to escalation %s: chose %q (%s).",
				requestID, resp.ActionID, label), nil
		},
	})
}

// registerAIEscalation adds the request_ai_escalation tool.
// This tool sends a question to a frontier model for judgment.
func (r *Registry) registerAIEscalation(deps EscalationDeps) {
	// AI escalation uses the LLM directly — it doesn't need the
	// notification router. For now, register a placeholder that
	// documents the intent. The actual implementation requires
	// access to the LLM client with a high quality floor, which
	// will be wired in a follow-up.
	r.Register(&Tool{
		Name: "request_ai_escalation",
		Description: "Request a decision from a frontier AI model. Use this when you need " +
			"sophisticated reasoning beyond your current capability — complex trade-offs, " +
			"nuanced judgment calls, or domain expertise.\n\n" +
			"The question and context are sent to a high-capability model. " +
			"Returns the model's judgment and reasoning.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{
					"type":        "string",
					"description": "The question or decision needed",
				},
				"context": map[string]any{
					"type":        "string",
					"description": "Relevant context for the decision",
				},
				"constraints": map[string]any{
					"type":        "array",
					"description": "Specific constraints or requirements",
					"items":       map[string]any{"type": "string"},
				},
			},
			"required": []string{"question"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "", fmt.Errorf("request_ai_escalation is not yet implemented; use request_human_escalation or thane_now with a high quality_floor hint")
		},
	})
}
