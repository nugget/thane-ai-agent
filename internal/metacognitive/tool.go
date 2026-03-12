package metacognitive

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nugget/thane-ai-agent/internal/logging"
	"github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// minStateContentLen is the minimum content length for
// update_metacognitive_state. Rejects trivially short updates.
const minStateContentLen = 50

// RegisterTools registers metacognitive-specific tools on the given
// registry: set_next_sleep, update_metacognitive_state, and (when
// egoFile is non-empty) append_ego_observation. The LLM calls these
// during iterations to control sleep timing, persist its state file,
// and contribute observations to ego.md.
//
// stateFilePath is the resolved absolute path to the state file (either
// inside the provenance store or the workspace, depending on config).
//
// When store is non-nil, file writes go through the provenance store
// (auto-committed with SSH signatures). When nil, files are written
// directly via os.WriteFile (backward compatible).
//
// Tool handlers capture theLoop via closure to communicate with the
// running loop goroutine (e.g., setting sleep durations, reading the
// current conversation ID).
func RegisterTools(registry *tools.Registry, theLoop *loop.Loop, cfg Config, stateFilePath, egoFile string, store ProvenanceWriter) {
	statePath := stateFilePath

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
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
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
			if d < cfg.MinSleep {
				d = cfg.MinSleep
			}
			if d > cfg.MaxSleep {
				d = cfg.MaxSleep
			}

			theLoop.SetNextSleep(d)
			logging.Logger(ctx).Info("metacognitive sleep set",
				"duration", d.Round(time.Second),
				"reason", reason,
			)

			return fmt.Sprintf("Next sleep set to %s (bounds: %s–%s).",
				d, cfg.MinSleep, cfg.MaxSleep), nil
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
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			content, _ := args["content"].(string)
			if len(content) < minStateContentLen {
				return "", fmt.Errorf("content too short (%d chars, minimum %d)", len(content), minStateContentLen)
			}

			log := logging.Logger(ctx)
			convID := theLoop.CurrentConvID()

			// Append metadata footer.
			footer := fmt.Sprintf("\n\n<!-- metacognitive: iteration=%s updated=%s -->\n",
				convID, time.Now().UTC().Format(time.RFC3339))
			fullContent := content + footer

			if store != nil {
				// Write through provenance store — auto-committed
				// with SSH signature. Use filepath.Base to normalize
				// paths like "Thane/metacognitive.md" to a flat layout.
				storeFilename := filepath.Base(cfg.StateFile)
				if err := store.Write(ctx, storeFilename, fullContent, convID); err != nil {
					return "", fmt.Errorf("write state via provenance: %w", err)
				}
				log.Info("metacognitive state committed to provenance",
					"file", storeFilename,
					"bytes", len(fullContent),
				)
				return fmt.Sprintf("State file committed (%d bytes) to provenance store.", len(fullContent)), nil
			}

			// Fallback: direct file I/O (no provenance store configured).

			// Save previous version as .prev backup.
			if existing, err := os.ReadFile(statePath); err == nil {
				prevPath := statePath + ".prev"
				if writeErr := os.WriteFile(prevPath, existing, 0o644); writeErr != nil {
					log.Warn("failed to save previous state file",
						"error", writeErr,
						"path", prevPath,
					)
				}
			}

			// Ensure parent directory exists.
			if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
				return "", fmt.Errorf("create state directory: %w", err)
			}

			if err := os.WriteFile(statePath, []byte(fullContent), 0o644); err != nil {
				return "", fmt.Errorf("write state file: %w", err)
			}

			log.Info("metacognitive state updated",
				"path", statePath,
				"bytes", len(fullContent),
			)

			return fmt.Sprintf("State file updated (%d bytes) at %s.", len(fullContent), statePath), nil
		},
	})

	// append_ego_observation: append-only shim for ego.md, available
	// when egoFile is configured. The metacog loop excludes general
	// file tools, so this provides controlled write access to
	// core:ego.md without granting full file_write.
	if egoFile != "" {
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
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				observation, _ := args["observation"].(string)
				if len(observation) < minStateContentLen {
					return "", fmt.Errorf("observation too short (%d chars, minimum %d)", len(observation), minStateContentLen)
				}

				convID := theLoop.CurrentConvID()

				// Build the appended block with metadata.
				block := fmt.Sprintf("\n\n### Metacognitive Observation\n"+
					"<!-- metacognitive: iteration=%s observed=%s -->\n\n%s\n",
					convID, time.Now().UTC().Format(time.RFC3339), observation)

				if store != nil {
					// Read existing from provenance store, append, write back.
					existing, err := store.Read("ego.md")
					if err != nil && !os.IsNotExist(err) {
						return "", fmt.Errorf("read ego from provenance: %w", err)
					}
					fullContent := existing + block
					if err := store.Write(ctx, "ego.md", fullContent, convID); err != nil {
						return "", fmt.Errorf("write ego via provenance: %w", err)
					}
					logging.Logger(ctx).Info("ego observation committed to provenance",
						"file", "ego.md",
						"bytes", len(block),
					)
					return fmt.Sprintf("Observation committed to provenance ego.md (%d bytes).", len(block)), nil
				}

				// Fallback: direct file I/O.
				existing, err := os.ReadFile(egoFile)
				if err != nil && !os.IsNotExist(err) {
					return "", fmt.Errorf("read ego file: %w", err)
				}
				fullContent := string(existing) + block

				// Ensure parent directory exists.
				if err := os.MkdirAll(filepath.Dir(egoFile), 0o755); err != nil {
					return "", fmt.Errorf("create ego directory: %w", err)
				}

				if err := os.WriteFile(egoFile, []byte(fullContent), 0o644); err != nil {
					return "", fmt.Errorf("write ego file: %w", err)
				}

				logging.Logger(ctx).Info("ego observation appended",
					"path", egoFile,
					"bytes", len(block),
				)

				return fmt.Sprintf("Observation appended to core:ego.md (%d bytes).", len(block)), nil
			},
		})
	}
}
