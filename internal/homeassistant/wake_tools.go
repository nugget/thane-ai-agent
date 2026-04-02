package homeassistant

import (
	"fmt"
	"math"
	"time"

	"github.com/nugget/thane-ai-agent/internal/router"
)

// WakeTools provides wake subscription management tools for the agent.
type WakeTools struct {
	store *WakeStore
}

// NewWakeTools creates wake tools backed by the given store.
func NewWakeTools(store *WakeStore) *WakeTools {
	return &WakeTools{store: store}
}

// Execute dispatches a wake tool call by name.
func (t *WakeTools) Execute(name string, args map[string]any) (string, error) {
	switch name {
	case "create_anticipation":
		return t.create(args)
	case "list_anticipations":
		return t.list()
	case "update_anticipation":
		return t.update(args)
	case "cancel_anticipation":
		return t.cancel(args)
	default:
		return "", fmt.Errorf("unknown wake tool: %s", name)
	}
}

// IsWakeTool reports whether the named tool is a wake subscription tool.
func IsWakeTool(name string) bool {
	switch name {
	case "create_anticipation", "list_anticipations", "update_anticipation", "cancel_anticipation":
		return true
	}
	return false
}

func (t *WakeTools) create(args map[string]any) (string, error) {
	topic, _ := args["topic"].(string)
	name, _ := args["name"].(string)
	kbRef, _ := args["kb_ref"].(string)
	context, _ := args["context"].(string)

	if topic == "" {
		return "", fmt.Errorf("topic is required")
	}
	if name == "" {
		return "", fmt.Errorf("name is required")
	}

	seed := router.LoopSeed{
		Source:           "wake",
		Mission:          "anticipation",
		DelegationGating: "disabled",
		Context:          context,
	}

	// Parse optional seed fields.
	if v, ok := args["model"].(string); ok {
		seed.Model = v
	}
	if v, ok := args["local_only"].(bool); ok {
		seed.LocalOnly = &v
	}
	if v, ok := args["quality_floor"].(float64); ok {
		q := int(v)
		if q < 1 || q > 10 {
			return "", fmt.Errorf("quality_floor must be between 1 and 10, got %d", q)
		}
		seed.QualityFloor = q
	}
	if v, ok := args["context_entities"].([]any); ok {
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				seed.ContextEntities = append(seed.ContextEntities, s)
			}
		}
		const maxContextEntities = 10
		if len(seed.ContextEntities) > maxContextEntities {
			seed.ContextEntities = seed.ContextEntities[:maxContextEntities]
		}
	}
	if v, ok := args["kb_refs"].([]any); ok {
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				seed.KBRefs = append(seed.KBRefs, s)
			}
		}
	}

	w := &WakeSubscription{
		Topic:   topic,
		Name:    name,
		KBRef:   kbRef,
		Context: context,
		Seed:    seed,
		Enabled: true,
	}

	if err := t.store.Create(w); err != nil {
		return "", err
	}

	result := fmt.Sprintf("Created wake subscription: %s\nID: %s\nTopic: %s\n", w.Name, w.ID, w.Topic)
	if w.KBRef != "" {
		result += fmt.Sprintf("KB ref: %s\n", w.KBRef)
	}
	if len(seed.ContextEntities) > 0 {
		result += fmt.Sprintf("Context entities: %v\n", seed.ContextEntities)
	}

	return result, nil
}

func (t *WakeTools) list() (string, error) {
	active, err := t.store.Active()
	if err != nil {
		return "", err
	}

	if len(active) == 0 {
		return "No active anticipations.", nil
	}

	now := time.Now()
	result := fmt.Sprintf("Active anticipations: %d\n\n", len(active))
	for _, w := range active {
		status := "enabled"
		if !w.Enabled {
			status = "disabled"
		}
		result += fmt.Sprintf("**%s** (ID: %s)\n", w.Name, w.ID)
		result += fmt.Sprintf("  Topic: %s  |  %s\n", w.Topic, status)

		// Fire stats.
		lastFired := "never"
		if w.LastFiredAt != nil {
			lastFired = formatDelta(*w.LastFiredAt, now)
		}
		age := now.Sub(w.CreatedAt)
		ageDays := age.Hours() / 24
		rate := float64(0)
		if ageDays > 0 {
			rate = float64(w.FireCount) / ageDays
		}
		result += fmt.Sprintf("  Last fired: %s  |  Fires: %d  |  Rate: %.1f/day  |  Age: %s\n",
			lastFired, w.FireCount, rate, formatAge(age))

		if w.KBRef != "" {
			result += fmt.Sprintf("  KB ref: %s\n", w.KBRef)
		}
		if w.Context != "" {
			result += fmt.Sprintf("  Context: %s\n", truncateStr(w.Context, 100))
		}
		result += "\n"
	}

	return result, nil
}

func (t *WakeTools) update(args map[string]any) (string, error) {
	id, _ := args["id"].(string)
	if id == "" {
		return "", fmt.Errorf("id is required")
	}

	w, err := t.store.Get(id)
	if err != nil {
		return "", err
	}
	if w == nil {
		return "", fmt.Errorf("anticipation not found: %s", id)
	}

	// Apply optional updates.
	if v, ok := args["topic"].(string); ok && v != "" {
		w.Topic = v
	}
	if v, ok := args["name"].(string); ok && v != "" {
		w.Name = v
	}
	if v, ok := args["kb_ref"].(string); ok {
		w.KBRef = v
	}
	if v, ok := args["context"].(string); ok {
		w.Context = v
		w.Seed.Context = v
	}
	if v, ok := args["enabled"].(bool); ok {
		w.Enabled = v
	}

	if err := t.store.Update(w); err != nil {
		return "", err
	}

	return fmt.Sprintf("Updated anticipation: %s (ID: %s)", w.Name, w.ID), nil
}

func (t *WakeTools) cancel(args map[string]any) (string, error) {
	id, _ := args["id"].(string)
	if id == "" {
		return "", fmt.Errorf("id is required")
	}

	w, err := t.store.Get(id)
	if err != nil {
		return "", err
	}
	if w == nil {
		return "", fmt.Errorf("anticipation not found: %s", id)
	}

	if err := t.store.Delete(id); err != nil {
		return "", err
	}

	return fmt.Sprintf("Cancelled anticipation: %s", w.Name), nil
}

// formatAge produces a human-readable age string.
func formatAge(d time.Duration) string {
	days := int(math.Floor(d.Hours() / 24))
	if days > 0 {
		return fmt.Sprintf("%dd", days)
	}
	hours := int(math.Floor(d.Hours()))
	if hours > 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dm", int(math.Floor(d.Minutes())))
}

// formatDelta formats a timestamp as a relative delta from now (e.g., "-45s", "+3600s").
// Follows the convention from awareness.FormatDeltaOnly for model-facing output.
func formatDelta(t time.Time, now time.Time) string {
	secs := int64(t.Sub(now).Truncate(time.Second) / time.Second)
	if secs <= 0 {
		return fmt.Sprintf("-%ds", -secs)
	}
	return fmt.Sprintf("+%ds", secs)
}

// truncateStr truncates a string to maxLen, appending "..." if truncated.
// Uses rune-safe truncation to avoid splitting multi-byte characters.
func truncateStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}
