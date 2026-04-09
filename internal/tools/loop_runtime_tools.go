package tools

import (
	"context"
	"fmt"
	"strings"

	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
)

const (
	defaultLoopStatusLimit = 25
	maxLoopStatusLimit     = 200
)

// LoopRuntimeToolDeps wires the live loop registry and ad hoc launch path
// into the tool registry so the model can inspect and control currently
// running loops.
type LoopRuntimeToolDeps struct {
	Registry   *looppkg.Registry
	LaunchLoop func(context.Context, looppkg.Launch) (looppkg.LaunchResult, error)
}

// ConfigureLoopRuntimeTools stores the runtime dependencies needed by the
// live loop tool family and registers the tools.
func (r *Registry) ConfigureLoopRuntimeTools(deps LoopRuntimeToolDeps) {
	r.liveLoopRegistry = deps.Registry
	r.launchLoop = deps.LaunchLoop
	r.registerLoopRuntimeTools()
}

func (r *Registry) registerLoopRuntimeTools() {
	if r.liveLoopRegistry == nil {
		return
	}

	r.Register(&Tool{
		Name:        "loop_status",
		Description: "Inspect the live loop registry. Returns compact structured status for currently running loops, with optional filters by query text, state, operation, and a result limit.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Optional case-insensitive substring match against loop ID, name, parent ID, and metadata values.",
				},
				"state": map[string]any{
					"type":        "string",
					"description": "Optional exact lifecycle state filter such as pending, sleeping, waiting, processing, error, or stopped.",
				},
				"operation": map[string]any{
					"type":        "string",
					"enum":        []string{"request_reply", "background_task", "service"},
					"description": "Optional exact operation filter.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("Maximum loops to return (default %d, max %d).", defaultLoopStatusLimit, maxLoopStatusLimit),
				},
			},
		},
		Handler: r.handleLoopStatus,
	})

	r.Register(&Tool{
		Name:        "spawn_loop",
		Description: "Launch an ad hoc loop immediately using the loops-ng launch contract without persisting a loop definition. Use this for temporary services, detached background research that should report back later, or one-shot request/reply runs that do not belong in the durable loop-definition registry. For async work, prefer operation=background_task with completion=conversation or completion=channel.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"launch": map[string]any{
					"type":        "object",
					"description": "Loops-ng launch object. Include spec, task, and any per-launch routing or completion overrides. The most common detached shape is a task plus spec.operation=background_task and spec.completion=conversation.",
				},
			},
			"required": []string{"launch"},
		},
		Handler: r.handleSpawnLoop,
	})

	r.Register(&Tool{
		Name:        "stop_loop",
		Description: "Stop one currently running loop by loop_id or exact name. Prefer loop_id when available. If name is ambiguous, the tool returns the candidate IDs so you can retry precisely.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"loop_id": map[string]any{
					"type":        "string",
					"description": "Exact live loop ID to stop. Preferred when you already have it from loop_status.",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Exact live loop name to stop when loop_id is not known.",
				},
			},
		},
		Handler: r.handleStopLoop,
	})
}

func (r *Registry) handleLoopStatus(_ context.Context, args map[string]any) (string, error) {
	if r.liveLoopRegistry == nil {
		return "", fmt.Errorf("live loop registry is not configured")
	}
	query := strings.ToLower(ldStringArg(args, "query"))
	state := strings.ToLower(ldStringArg(args, "state"))
	operation := looppkg.Operation(ldStringArg(args, "operation"))
	limit := clampLoopListLimit(ldIntArg(args, "limit"), defaultLoopStatusLimit, maxLoopStatusLimit)

	statuses := r.liveLoopRegistry.Statuses()
	filtered := make([]looppkg.Status, 0, len(statuses))
	for _, status := range statuses {
		if !matchLoopStatus(status, query, state, operation) {
			continue
		}
		filtered = append(filtered, status)
		if len(filtered) >= limit {
			break
		}
	}

	maxLoops := r.liveLoopRegistry.MaxLoops()
	remaining := 0
	if maxLoops > 0 {
		remaining = maxLoops - r.liveLoopRegistry.ActiveCount()
		if remaining < 0 {
			remaining = 0
		}
	}

	return ldMarshalToolJSON(map[string]any{
		"status":             "ok",
		"active_count":       r.liveLoopRegistry.ActiveCount(),
		"max_loops":          maxLoops,
		"remaining_capacity": remaining,
		"filters": map[string]any{
			"query":     strings.TrimSpace(query),
			"state":     state,
			"operation": operation,
			"limit":     limit,
		},
		"loops": filtered,
	})
}

func (r *Registry) handleSpawnLoop(ctx context.Context, args map[string]any) (string, error) {
	if r.launchLoop == nil {
		return "", fmt.Errorf("loop launch is not configured")
	}
	launch, err := decodeLoopLaunchArg(args, "launch")
	if err != nil {
		return "", fmt.Errorf("launch: %w", err)
	}
	launch = applyAdHocLoopLaunchContextDefaults(ctx, launch)
	result, err := r.launchLoop(ctx, launch)
	if err != nil {
		return "", err
	}
	return ldMarshalToolJSON(map[string]any{
		"status": "ok",
		"result": result,
	})
}

func (r *Registry) handleStopLoop(_ context.Context, args map[string]any) (string, error) {
	if r.liveLoopRegistry == nil {
		return "", fmt.Errorf("live loop registry is not configured")
	}
	loopID := ldStringArg(args, "loop_id")
	name := ldStringArg(args, "name")
	if loopID == "" && name == "" {
		return "", fmt.Errorf("loop_id or name is required")
	}

	var (
		status looppkg.Status
		err    error
	)
	if loopID != "" {
		live := r.liveLoopRegistry.Get(loopID)
		if live == nil {
			return "", fmt.Errorf("loop %q not found", loopID)
		}
		status = live.Status()
		err = r.liveLoopRegistry.StopLoop(loopID)
	} else {
		matches := r.liveLoopRegistry.FindByName(name)
		switch len(matches) {
		case 0:
			return "", fmt.Errorf("loop named %q not found", name)
		case 1:
			status = matches[0].Status()
			err = r.liveLoopRegistry.StopLoopByName(name)
		default:
			ids := make([]string, 0, len(matches))
			for _, live := range matches {
				ids = append(ids, live.ID())
			}
			return "", fmt.Errorf("loop name %q is ambiguous; retry with loop_id from %v", name, ids)
		}
	}
	if err != nil {
		return "", err
	}
	return ldMarshalToolJSON(map[string]any{
		"status": "ok",
		"loop":   status,
	})
}

func clampLoopListLimit(raw, def, max int) int {
	switch {
	case raw <= 0:
		return def
	case raw > max:
		return max
	default:
		return raw
	}
}

func matchLoopStatus(status looppkg.Status, query, state string, operation looppkg.Operation) bool {
	if state != "" && strings.ToLower(string(status.State)) != state {
		return false
	}
	if operation != "" && status.Config.Operation != operation {
		return false
	}
	if query == "" {
		return true
	}
	if strings.Contains(strings.ToLower(status.ID), query) || strings.Contains(strings.ToLower(status.Name), query) || strings.Contains(strings.ToLower(status.ParentID), query) {
		return true
	}
	for _, value := range status.Config.Metadata {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}
