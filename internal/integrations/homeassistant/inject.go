package homeassistant

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

// directiveRe matches <!-- ha-inject: entity_id, ... --> directives.
var directiveRe = regexp.MustCompile(`<!--\s*ha-inject:\s*(.*?)\s*-->`)

// EntityState holds the resolved state of a single HA entity.
type EntityState struct {
	EntityID string
	State    string
}

// StateFetcher retrieves entity state from Home Assistant.
// [Client] satisfies this interface via its [Client.FetchState] method.
type StateFetcher interface {
	// FetchState returns the current state string for an entity.
	FetchState(ctx context.Context, entityID string) (string, error)
}

// FetchState returns the current state string for an entity, satisfying
// the [StateFetcher] interface.
func (c *Client) FetchState(ctx context.Context, entityID string) (string, error) {
	state, err := c.GetState(ctx, entityID)
	if err != nil {
		return "", err
	}
	return state.State, nil
}

// ResolveInject scans content for ha-inject directives, fetches current
// entity state from Home Assistant, and prepends a live-state summary block.
//
// Documents declare dependencies on HA entities using HTML comment
// directives:
//
//	<!-- ha-inject: input_boolean.burn_ban, sensor.pool_temp -->
//
// The caller controls the deadline via ctx; ResolveInject does not apply
// its own timeout. Returns content unchanged when no directives are found,
// fetcher is nil, or every entity ID list is empty. Gracefully degrades
// when HA is unreachable: includes a warning note and the original document.
func ResolveInject(ctx context.Context, content []byte, fetcher StateFetcher, logger *slog.Logger) []byte {
	if logger == nil {
		logger = slog.Default()
	}
	if fetcher == nil {
		return content
	}

	entities := parseDirectives(content)
	if len(entities) == 0 {
		return content
	}

	var succeeded []EntityState
	var failed []string

	for _, id := range entities {
		// If the context is already done (deadline exceeded or canceled),
		// mark all remaining entities as failed and stop fetching.
		if ctx.Err() != nil {
			logger.Warn("ha-inject: context expired, skipping remaining entities",
				"remaining", len(entities)-len(succeeded)-len(failed),
				"error", ctx.Err())
			failed = append(failed, id)
			continue
		}
		state, err := fetcher.FetchState(ctx, id)
		if err != nil {
			logger.Warn("ha-inject: failed to fetch entity state",
				"entity_id", id, "error", err)
			failed = append(failed, id)
			continue
		}
		succeeded = append(succeeded, EntityState{EntityID: id, State: state})
	}

	return formatInjectResult(succeeded, failed, content)
}

// parseDirectives extracts deduplicated entity IDs from all ha-inject
// directives in the document, preserving order of first appearance.
func parseDirectives(content []byte) []string {
	matches := directiveRe.FindAllSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[string]bool)
	var ids []string
	for _, match := range matches {
		parts := strings.Split(string(match[1]), ",")
		for _, p := range parts {
			id := strings.TrimSpace(p)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// formatInjectResult builds the augmented document with a state block prepended.
func formatInjectResult(succeeded []EntityState, failed []string, content []byte) []byte {
	if len(succeeded) == 0 && len(failed) == 0 {
		return content
	}

	var sb strings.Builder

	if len(succeeded) == 0 {
		// All fetches failed — add warning only.
		sb.WriteString("⚠️ HA entity state unavailable — fetch manually if needed\n\n---\n\n")
		sb.Write(content)
		return []byte(sb.String())
	}

	sb.WriteString("## Current HA State (live)\n")
	for _, e := range succeeded {
		fmt.Fprintf(&sb, "- %s: %s\n", e.EntityID, e.State)
	}
	for _, id := range failed {
		fmt.Fprintf(&sb, "- %s: ⚠️ fetch failed\n", id)
	}
	sb.WriteString("\n---\n\n")
	sb.Write(content)
	return []byte(sb.String())
}
