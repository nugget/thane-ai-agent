package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/opstate"
)

const (
	lensNamespace = "lenses"
	lensActiveKey = "active"
)

// LensStore manages persistent behavioral lenses via opstate.
// Lenses are global — they apply to all conversations and survive
// restarts. Use activate_capability for per-conversation tool access.
type LensStore struct {
	state *opstate.Store
}

// NewLensStore creates a lens store backed by opstate.
func NewLensStore(state *opstate.Store) *LensStore {
	return &LensStore{state: state}
}

// ActiveLenses returns the currently active lens tags.
func (s *LensStore) ActiveLenses() ([]string, error) {
	raw, err := s.state.Get(lensNamespace, lensActiveKey)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	var lenses []string
	if err := json.Unmarshal([]byte(raw), &lenses); err != nil {
		return nil, fmt.Errorf("parse active lenses: %w", err)
	}
	return lenses, nil
}

func (s *LensStore) setLenses(lenses []string) error {
	sort.Strings(lenses)
	data, err := json.Marshal(lenses)
	if err != nil {
		return err
	}
	return s.state.Set(lensNamespace, lensActiveKey, string(data))
}

// Add activates a lens. Duplicates are ignored.
func (s *LensStore) Add(lens string) error {
	lenses, err := s.ActiveLenses()
	if err != nil {
		return err
	}
	for _, l := range lenses {
		if l == lens {
			return nil // already active
		}
	}
	return s.setLenses(append(lenses, lens))
}

// Remove deactivates a lens.
func (s *LensStore) Remove(lens string) error {
	lenses, err := s.ActiveLenses()
	if err != nil {
		return err
	}
	filtered := make([]string, 0, len(lenses))
	for _, l := range lenses {
		if l != lens {
			filtered = append(filtered, l)
		}
	}
	return s.setLenses(filtered)
}

// SetLensTools registers activate_lens, deactivate_lens, and list_lenses
// tools. These manage persistent behavioral lenses that apply globally
// across all conversations. Lenses use the same tag system as
// capabilities — when a lens is active, KB articles and talents tagged
// with that lens name are loaded into every conversation.
func (r *Registry) SetLensTools(store *LensStore) {
	if store == nil {
		return
	}
	r.lensStore = store

	r.Register(&Tool{
		Name:            "activate_lens",
		AlwaysAvailable: true,
		Description: "Activate a behavioral lens globally. Lenses are persistent context modes that apply to ALL conversations " +
			"and survive restarts. They change how you perceive and respond — like switching between different attentional states. " +
			"Any KB articles or talents tagged with the lens name will load automatically.\n\n" +
			"Examples: night_quiet (gentle tone, higher notification thresholds), everyone_away (security focus), storm_watch (weather-focused).\n\n" +
			"Unlike activate_capability (per-conversation tools), lenses are environmental — they reflect the household's state, not a task.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tag": map[string]any{
					"type":        "string",
					"description": "The lens tag to activate (e.g., \"night_quiet\", \"everyone_away\"). Same tag used in KB article frontmatter.",
				},
			},
			"required": []string{"tag"},
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			lens := extractLensArg(args)
			if lens == "" {
				return "", fmt.Errorf("tag is required (e.g., activate_lens(tag: \"night_quiet\"))")
			}

			if err := store.Add(lens); err != nil {
				return "", fmt.Errorf("activate lens: %w", err)
			}

			lenses, _ := store.ActiveLenses()
			return fmt.Sprintf("Lens **%s** activated globally. Active lenses: %s.", lens, formatLensList(lenses)), nil
		},
	})

	r.Register(&Tool{
		Name:            "deactivate_lens",
		AlwaysAvailable: true,
		Description:     "Deactivate a behavioral lens globally. The lens and its associated context will be removed from all conversations.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tag": map[string]any{
					"type":        "string",
					"description": "The lens tag to deactivate",
				},
			},
			"required": []string{"tag"},
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			lens := extractLensArg(args)
			if lens == "" {
				return "", fmt.Errorf("tag is required")
			}

			if err := store.Remove(lens); err != nil {
				return "", fmt.Errorf("deactivate lens: %w", err)
			}

			lenses, _ := store.ActiveLenses()
			if len(lenses) == 0 {
				return fmt.Sprintf("Lens **%s** deactivated. No active lenses.", lens), nil
			}
			return fmt.Sprintf("Lens **%s** deactivated. Remaining lenses: %s.", lens, formatLensList(lenses)), nil
		},
	})

	r.Register(&Tool{
		Name:            "list_lenses",
		AlwaysAvailable: true,
		Description:     "List all currently active behavioral lenses.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			lenses, err := store.ActiveLenses()
			if err != nil {
				return "", fmt.Errorf("list lenses: %w", err)
			}
			if len(lenses) == 0 {
				return "No active lenses.", nil
			}
			return fmt.Sprintf("Active lenses: %s.", formatLensList(lenses)), nil
		},
	})
}

func extractLensArg(args map[string]any) string {
	if v, ok := args["tag"].(string); ok && v != "" {
		return strings.TrimSpace(v)
	}
	// Accept "lens" and "name" as aliases.
	if v, ok := args["lens"].(string); ok && v != "" {
		return strings.TrimSpace(v)
	}
	if v, ok := args["name"].(string); ok && v != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

func formatLensList(lenses []string) string {
	if len(lenses) == 0 {
		return "none"
	}
	return strings.Join(lenses, ", ")
}
