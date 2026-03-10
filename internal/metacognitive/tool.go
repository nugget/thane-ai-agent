package metacognitive

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// minStateContentLen is the minimum content length for
// update_metacognitive_state. Rejects trivially short updates.
const minStateContentLen = 50

// RegisterTools registers metacognitive-specific tools on the given
// registry: set_next_sleep, update_metacognitive_state, and (when
// EgoFile is configured) append_ego_observation. The LLM calls these
// during iterations to control sleep timing, persist its state file,
// and contribute observations to ego.md.
//
// The handler captures the [Loop] pointer via closure so it can
// communicate the chosen duration back to the loop goroutine. This
// follows the same pattern as session_working_memory tools.
func (l *Loop) RegisterTools(registry *tools.Registry) {
	registry.Register(&tools.Tool{
		Name: "set_next_sleep",
		Description: "Set how long the metacognitive loop should sleep before the next iteration. " +
			"Call this at the end of your analysis to control your attention cycle. " +
			"Short sleep (2–5m) for active situations needing monitoring. " +
			"Long sleep (15–30m) for quiet periods. " +
			"If you don't call this, a default sleep duration is used.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"duration": map[string]any{
					"type":        "string",
					"description": "Sleep duration as a Go duration string (e.g., '5m', '15m', '2m30s'). Clamped to configured min/max bounds.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Brief explanation of why this duration was chosen (logged for debugging).",
				},
			},
			"required": []string{"duration"},
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			durStr, _ := args["duration"].(string)
			if durStr == "" {
				// Local models often pass integers meaning minutes.
				if numVal, ok := args["duration"].(float64); ok {
					durStr = fmt.Sprintf("%dm", int(numVal))
				}
			}
			if durStr == "" {
				return "", fmt.Errorf("duration is required")
			}

			d, err := time.ParseDuration(durStr)
			if err != nil {
				return "", fmt.Errorf("invalid duration %q: %w", durStr, err)
			}

			reason, _ := args["reason"].(string)

			// Clamp to configured bounds.
			if d < l.config.MinSleep {
				d = l.config.MinSleep
			}
			if d > l.config.MaxSleep {
				d = l.config.MaxSleep
			}

			l.setNextSleep(d)
			l.deps.Logger.Info("metacognitive sleep set",
				"duration", d.Round(time.Second),
				"reason", reason,
			)

			return fmt.Sprintf("Next sleep set to %s (bounds: %s–%s).",
				d, l.config.MinSleep, l.config.MaxSleep), nil
		},
	})

	registry.Register(&tools.Tool{
		Name: "update_metacognitive_state",
		Description: "Write the metacognitive state file (metacognitive.md). " +
			"Call this each iteration with your complete updated observations, " +
			"active concerns, recent actions, and sleep reasoning. " +
			"This is the ONLY way to persist state between iterations — " +
			"each iteration is a fresh conversation.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{
					"type":        "string",
					"description": "Full markdown content for the state file. Must be at least 50 characters.",
				},
			},
			"required": []string{"content"},
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			content, _ := args["content"].(string)
			if len(content) < minStateContentLen {
				return "", fmt.Errorf("content too short (%d chars, minimum %d)", len(content), minStateContentLen)
			}

			statePath := l.stateFilePath()

			// Save previous version as .prev backup.
			if existing, err := os.ReadFile(statePath); err == nil {
				prevPath := statePath + ".prev"
				if writeErr := os.WriteFile(prevPath, existing, 0o644); writeErr != nil {
					l.deps.Logger.Warn("failed to save previous state file",
						"error", writeErr,
						"path", prevPath,
					)
				}
			}

			// Append metadata footer.
			convID := l.getCurrentConvID()
			footer := fmt.Sprintf("\n\n<!-- metacognitive: iteration=%s updated=%s -->\n",
				convID, time.Now().UTC().Format(time.RFC3339))
			fullContent := content + footer

			// Ensure parent directory exists.
			if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
				return "", fmt.Errorf("create state directory: %w", err)
			}

			if err := os.WriteFile(statePath, []byte(fullContent), 0o644); err != nil {
				return "", fmt.Errorf("write state file: %w", err)
			}

			l.deps.Logger.Info("metacognitive state updated",
				"path", statePath,
				"bytes", len(fullContent),
				"conversation_id", convID,
			)

			return fmt.Sprintf("State file updated (%d bytes) at %s.", len(fullContent), statePath), nil
		},
	})

	// append_ego_observation: append-only shim for ego.md, available
	// when EgoFile is configured. The metacog loop excludes general
	// file tools, so this provides controlled write access to
	// core:ego.md without granting full file_write.
	if l.deps.EgoFile != "" {
		registry.Register(&tools.Tool{
			Name: "append_ego_observation",
			Description: "Append a metacognitive observation to core:ego.md. " +
				"Use this when you notice significant behavioral patterns, breakthroughs, " +
				"or persistent struggles that would matter to long-term self-understanding. " +
				"Your observation is appended — existing content is never overwritten.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"observation": map[string]any{
						"type":        "string",
						"description": "The observation to append. Should focus on patterns revealing how the agent is evolving. Must be at least 50 characters.",
					},
				},
				"required": []string{"observation"},
			},
			Handler: func(_ context.Context, args map[string]any) (string, error) {
				observation, _ := args["observation"].(string)
				if len(observation) < minStateContentLen {
					return "", fmt.Errorf("observation too short (%d chars, minimum %d)", len(observation), minStateContentLen)
				}

				egoPath := l.deps.EgoFile

				// Read existing content (empty file is fine).
				existing, err := os.ReadFile(egoPath)
				if err != nil && !os.IsNotExist(err) {
					return "", fmt.Errorf("read ego file: %w", err)
				}

				// Build the appended block with metadata.
				convID := l.getCurrentConvID()
				block := fmt.Sprintf("\n\n### Metacognitive Observation\n"+
					"<!-- metacognitive: iteration=%s observed=%s -->\n\n%s\n",
					convID, time.Now().UTC().Format(time.RFC3339), observation)

				fullContent := string(existing) + block

				// Ensure parent directory exists.
				if err := os.MkdirAll(filepath.Dir(egoPath), 0o755); err != nil {
					return "", fmt.Errorf("create ego directory: %w", err)
				}

				if err := os.WriteFile(egoPath, []byte(fullContent), 0o644); err != nil {
					return "", fmt.Errorf("write ego file: %w", err)
				}

				l.deps.Logger.Info("ego observation appended",
					"path", egoPath,
					"bytes", len(block),
					"conversation_id", convID,
				)

				return fmt.Sprintf("Observation appended to core:ego.md (%d bytes).", len(block)), nil
			},
		})
	}
}
